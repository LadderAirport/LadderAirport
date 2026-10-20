package control

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ladderairport/agent/internal/frpcbridge"
	"github.com/ladderairport/pkg/nodedns"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
)

// SingboxVersion is the upstream sing-box base version this agent is built against.
// Prefer constant.Version when set via -ldflags; fall back to the pinned tag.
func SingboxVersion() string {
	if constant.Version != "" && constant.Version != "unknown" {
		return constant.Version
	}
	return "1.12.22"
}

// BoxRuntime drives a single in-process sing-box instance (二开 adapter).
//
// Lifecycle (strict single-instance):
//   - At most one *box.Box is ever started at a time in this process.
//   - Apply serializes via applyMu; concurrent Apply/Start wait in line.
//   - Config update: stop old completely → start new (brief downtime is OK).
//   - Same config_hash while already running → no-op (idempotent).
//   - If the new box fails to start, attempt to restore the previous config.
type BoxRuntime struct {
	mu            sync.Mutex
	dataDir       string
	instance      *box.Box
	frpcRuntime   *frpcbridge.Runtime
	cancel        context.CancelFunc
	configJSON    string
	configHash    string
	startedAtUnix int64
	lastError     string
	state         State

	// Traffic stats from ConnectionTracker (current instance + rolled-up previous).
	tracker      *trafficTracker
	prevUplink   int64
	prevDownlink int64

	// applyMu serializes all lifecycle transitions (Apply/Start/Stop).
	applyMu sync.Mutex

	// Traffic persist loop lifecycle (guarded by applyMu; nil when not running).
	stopTrafficPersist chan struct{}
	trafficPersistDone chan struct{}
}

// NewBoxRuntime creates a BoxRuntime. dataDir, if non-empty, receives current.json
// snapshots of the last successfully applied config.
func NewBoxRuntime(dataDir string) *BoxRuntime {
	r := &BoxRuntime{
		dataDir: dataDir,
		state:   StateStopped,
	}
	r.loadTraffic()
	r.startTrafficPersistLoopLocked()
	return r
}

func (r *BoxRuntime) Apply(ctx context.Context, configJSON string, hash string) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	return r.applyLocked(ctx, configJSON, hash)
}

// applyLocked requires applyMu held.
func (r *BoxRuntime) applyLocked(ctx context.Context, configJSON string, hash string) error {
	// Restart the traffic persist loop if a previous Stop shut it down.
	r.startTrafficPersistLoopLocked()

	prepared, err := prepareConfigJSON(configJSON)
	if err != nil {
		r.setLastError(err.Error())
		return err
	}

	// Idempotent: already running this exact config — do not restart.
	// Compare against the prepared JSON so platform rewrites (default DNS /
	// Android sanitize) do not force a restart when the panel re-pushes the
	// same source config + hash.
	r.mu.Lock()
	same := r.state == StateRunning && r.instance != nil && hash != "" && hash == r.configHash && prepared == r.configJSON
	r.mu.Unlock()
	if same {
		return nil
	}

	// Parse/validate BEFORE tearing down the current instance.
	opts, err := r.parseOptions(prepared)
	if err != nil {
		r.setLastError(err.Error())
		return err
	}

	// Snapshot previous config for restore-on-failure.
	r.mu.Lock()
	prevJSON, prevHash := r.configJSON, r.configHash
	r.mu.Unlock()

	// Strict single-instance: stop old completely before starting new.
	// Brief disconnect is acceptable; avoids dual listen on the same ports.
	r.stopInstanceLocked()

	if err := r.startInstanceLocked(opts, prepared, hash); err != nil {
		// Best-effort restore of previous config when reload fails mid-way.
		if prevJSON != "" && prevJSON != prepared {
			if restErr := r.startInstanceFromJSONLocked(prevJSON, prevHash); restErr != nil {
				r.setLastError(fmt.Sprintf("启动失败：%v；恢复旧配置也失败：%v", err, restErr))
				return fmt.Errorf("启动代理实例失败：%w（恢复旧配置也失败：%v）", err, restErr)
			}
			r.setLastError("启动失败，已恢复旧配置：" + err.Error())
			return fmt.Errorf("启动代理实例失败：%w（已恢复旧配置）", err)
		}
		r.setLastError(err.Error())
		return fmt.Errorf("启动代理实例失败：%w", err)
	}

	if r.dataDir != "" {
		if err := r.writeCurrent(r.dataDir, prepared); err != nil {
			r.setLastError("代理实例正在运行，但写入 current.json 失败：" + err.Error())
		}
	}

	select {
	case <-ctx.Done():
		// Config already applied; do not roll back.
	default:
	}
	return nil
}

// stopInstanceLocked closes the current box if any. Caller must hold applyMu.
func (r *BoxRuntime) stopInstanceLocked() {
	r.mu.Lock()
	old := r.instance
	oldFRPC := r.frpcRuntime
	oldCancel := r.cancel
	oldTracker := r.tracker
	if oldTracker != nil {
		_, up, down := oldTracker.Snapshot()
		r.prevUplink += up
		r.prevDownlink += down
	}
	r.instance = nil
	r.frpcRuntime = nil
	r.cancel = nil
	r.tracker = nil
	r.state = StateStopped
	r.startedAtUnix = 0
	// Keep configJSON/hash so Start can re-apply after Stop.
	r.mu.Unlock()

	r.saveTraffic()

	if oldCancel != nil {
		oldCancel()
	}
	oldFRPC.Close()
	if old != nil {
		_ = old.Close()
	}
}

// startInstanceFromJSONLocked parses and starts. Caller holds applyMu; no current instance.
func (r *BoxRuntime) startInstanceFromJSONLocked(configJSON, hash string) error {
	prepared, err := prepareConfigJSON(configJSON)
	if err != nil {
		return err
	}
	opts, err := r.parseOptions(prepared)
	if err != nil {
		return err
	}
	return r.startInstanceLocked(opts, prepared, hash)
}

// startInstanceLocked creates and starts a box. Caller holds applyMu; instance must be nil.
func (r *BoxRuntime) startInstanceLocked(opts option.Options, configJSON, hash string) error {
	boxCtx := include.Context(context.Background())
	boxCtx, cancel := context.WithCancel(boxCtx)

	instance, err := box.New(box.Options{
		Context: boxCtx,
		Options: opts,
	})
	if err != nil {
		cancel()
		return fmt.Errorf("创建代理实例失败：%w", err)
	}

	tracker := newTrafficTracker()
	instance.Router().AppendTracker(tracker)

	if err := instance.Start(); err != nil {
		_ = instance.Close()
		cancel()
		return err
	}
	_, frpcConfigurations, err := splitFRPCConfig(configJSON)
	if err != nil {
		_ = instance.Close()
		cancel()
		return err
	}
	frpcRuntime, err := frpcbridge.Start(boxCtx, frpcConfigurations)
	if err != nil {
		_ = instance.Close()
		cancel()
		return err
	}

	r.mu.Lock()
	r.instance = instance
	r.frpcRuntime = frpcRuntime
	r.cancel = cancel
	r.tracker = tracker
	r.configJSON = configJSON
	r.configHash = hash
	r.startedAtUnix = time.Now().Unix()
	r.lastError = ""
	r.state = StateRunning
	r.mu.Unlock()
	return nil
}

func (r *BoxRuntime) setLastError(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastError = msg
	if r.instance == nil {
		r.state = StateStopped
	} else {
		r.state = StateRunning
	}
}

func (r *BoxRuntime) Start(ctx context.Context) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()

	r.mu.Lock()
	if r.state == StateRunning && r.instance != nil {
		r.mu.Unlock()
		return nil
	}
	cfg := r.configJSON
	hash := r.configHash
	r.mu.Unlock()

	if cfg == "" {
		return fmt.Errorf("没有可启动的配置，请先下发配置")
	}
	// Re-apply under same lock (idempotent if already mid-start elsewhere).
	return r.applyLocked(ctx, cfg, hash)
}

func (r *BoxRuntime) Stop(_ context.Context) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	r.stopTrafficPersistLoopLocked()
	r.stopInstanceLocked()
	return nil
}

func (r *BoxRuntime) Status(_ context.Context) Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Status{
		State:         r.state,
		ConfigHash:    r.configHash,
		StartedAtUnix: r.startedAtUnix,
		LastError:     r.lastError,
	}
}

// Metrics returns live connection count, cumulative traffic (survives hot-reload),
// and process RSS (Linux /proc; MemStats.Sys fallback elsewhere).
// CPU percent is sampled coarsely (0 if unavailable).
func (r *BoxRuntime) Metrics(_ context.Context) Metrics {
	r.mu.Lock()
	var conns, up, down int64
	if r.tracker != nil {
		c, u, d := r.tracker.Snapshot()
		conns, up, down = c, u, d
	}
	up += r.prevUplink
	down += r.prevDownlink
	r.mu.Unlock()

	return Metrics{
		Connections:    conns,
		UplinkBytes:    up,
		DownlinkBytes:  down,
		CPUPercent:     sampleCPUPercent(),
		MemoryRSSBytes: processRSSBytes(),
	}
}

// ProbeOutbound performs a real HTTP URL test through a running outbound. The
// instance reference is taken under the lifecycle lock, which is released
// before the network dial so a slow URL test does not block Apply/Start/Stop.
// The whole probe is bounded by a 10s timeout.
func (r *BoxRuntime) ProbeOutbound(ctx context.Context, outboundTag, targetURL string) (uint32, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return 0, fmt.Errorf("探测 URL 必须是完整的 HTTP/HTTPS 地址")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r.applyMu.Lock()
	r.mu.Lock()
	instance := r.instance
	state := r.state
	r.mu.Unlock()
	r.applyMu.Unlock()
	if instance == nil || state != StateRunning {
		return 0, fmt.Errorf("sing-box 尚未运行")
	}
	outbound, ok := instance.Outbound().Outbound(outboundTag)
	if !ok {
		return 0, fmt.Errorf("未找到出站：%s", outboundTag)
	}
	delay, err := urltest.URLTest(ctx, targetURL, outbound)
	if err != nil {
		return 0, err
	}
	return uint32(delay), nil
}

// ConfigJSON returns the last successfully applied config JSON (for tests).
func (r *BoxRuntime) ConfigJSON() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.configJSON
}

func (r *BoxRuntime) parseOptions(configJSON string) (option.Options, error) {
	configJSON, _, err := splitFRPCConfig(configJSON)
	if err != nil {
		return option.Options{}, err
	}
	ctx := include.Context(context.Background())
	opts, err := json.UnmarshalExtendedContext[option.Options](ctx, []byte(configJSON))
	if err != nil {
		return option.Options{}, fmt.Errorf("解析配置失败：%w", err)
	}
	return opts, nil
}

// prepareConfigJSON applies platform-neutral and platform-specific rewrites
// before parse/start. Safe to call on already-prepared JSON (idempotent).
func prepareConfigJSON(configJSON string) (string, error) {
	configJSON, err := ensureDefaultDNS(configJSON)
	if err != nil {
		return "", err
	}
	return sanitizePlatformConfig(configJSON)
}

func ensureDefaultDNS(configJSON string) (string, error) {
	var document map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal([]byte(configJSON), &document); err != nil {
		return "", fmt.Errorf("解析节点配置失败：%w", err)
	}
	rawDNS, hasDNS := document["dns"]
	needsDefault := !hasDNS
	if hasDNS {
		var dnsObj struct {
			Servers []stdjson.RawMessage `json:"servers"`
		}
		if err := stdjson.Unmarshal(rawDNS, &dnsObj); err != nil || len(dnsObj.Servers) == 0 {
			needsDefault = true
		}
	}
	if !needsDefault {
		return configJSON, nil
	}
	emitted, err := nodedns.Default().Emit(nodedns.EmitOptions{BindInterface: directBindInterface(document)})
	if err != nil {
		return "", fmt.Errorf("编码默认 DNS 失败：%w", err)
	}
	data, err := stdjson.Marshal(emitted.DNS)
	if err != nil {
		return "", fmt.Errorf("编码默认 DNS 失败：%w", err)
	}
	document["dns"] = data
	if emitted.RuleSet != nil {
		if err := mergeRuleSet(document, emitted.RuleSet); err != nil {
			return "", err
		}
	}
	if err := ensureChainDomainResolvers(document, emitted.ChainResolver); err != nil {
		return "", err
	}
	updated, err := stdjson.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("编码节点配置失败：%w", err)
	}
	return string(updated), nil
}

func directBindInterface(document map[string]stdjson.RawMessage) string {
	raw, ok := document["outbounds"]
	if !ok {
		return ""
	}
	var outbounds []map[string]any
	if err := stdjson.Unmarshal(raw, &outbounds); err != nil {
		return ""
	}
	for _, outbound := range outbounds {
		if outbound["tag"] != "direct" && outbound["type"] != "direct" {
			continue
		}
		bind, _ := outbound["bind_interface"].(string)
		return strings.TrimSpace(bind)
	}
	return ""
}

func mergeRuleSet(document map[string]stdjson.RawMessage, ruleSet map[string]any) error {
	tag, _ := ruleSet["tag"].(string)
	route := map[string]any{}
	if raw, ok := document["route"]; ok {
		if err := stdjson.Unmarshal(raw, &route); err != nil {
			return fmt.Errorf("解析路由配置失败：%w", err)
		}
	}
	if sets, ok := route["rule_set"].([]any); ok {
		for _, item := range sets {
			entry, _ := item.(map[string]any)
			if tag != "" && entry["tag"] == tag {
				return nil
			}
		}
		route["rule_set"] = append(sets, ruleSet)
	} else {
		route["rule_set"] = []any{ruleSet}
	}
	data, err := stdjson.Marshal(route)
	if err != nil {
		return fmt.Errorf("编码路由配置失败：%w", err)
	}
	document["route"] = data
	return nil
}

func ensureChainDomainResolvers(document map[string]stdjson.RawMessage, resolver map[string]any) error {
	raw, ok := document["outbounds"]
	if !ok {
		return nil
	}
	var outbounds []map[string]any
	if err := stdjson.Unmarshal(raw, &outbounds); err != nil {
		return fmt.Errorf("解析出站配置失败：%w", err)
	}
	changed := false
	for _, outbound := range outbounds {
		kind, _ := outbound["type"].(string)
		switch kind {
		case "direct", "block", "dns", "":
			continue
		}
		if _, exists := outbound["domain_resolver"]; exists {
			continue
		}
		cloned := make(map[string]any, len(resolver))
		for key, value := range resolver {
			cloned[key] = value
		}
		outbound["domain_resolver"] = cloned
		changed = true
	}
	if !changed {
		return nil
	}
	data, err := stdjson.Marshal(outbounds)
	if err != nil {
		return fmt.Errorf("编码出站配置失败：%w", err)
	}
	document["outbounds"] = data
	return nil
}

// stripDNSServerBinds removes bind_interface from dns.servers. Android node
// builds cannot honor it; called from the Android sanitizer so a pushed config
// cannot fail dial.
func stripDNSServerBinds(document map[string]stdjson.RawMessage) (bool, error) {
	raw, ok := document["dns"]
	if !ok {
		return false, nil
	}
	var dns map[string]any
	if err := stdjson.Unmarshal(raw, &dns); err != nil {
		return false, nil
	}
	servers, ok := dns["servers"].([]any)
	if !ok {
		return false, nil
	}
	changed := false
	for _, item := range servers {
		server, ok := item.(map[string]any)
		if !ok {
			continue
		}
		bind, has := server["bind_interface"]
		if !has || bind == nil || bind == "" {
			continue
		}
		delete(server, "bind_interface")
		changed = true
	}
	if !changed {
		return false, nil
	}
	data, err := stdjson.Marshal(dns)
	if err != nil {
		return false, fmt.Errorf("编码 DNS 配置失败：%w", err)
	}
	document["dns"] = data
	return true, nil
}

func splitFRPCConfig(configJSON string) (string, map[string]string, error) {
	var document map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal([]byte(configJSON), &document); err != nil {
		return "", nil, fmt.Errorf("解析节点配置失败：%w", err)
	}
	var configurations map[string]string
	if raw, ok := document["ladder_frpc"]; ok {
		if err := stdjson.Unmarshal(raw, &configurations); err != nil {
			return "", nil, fmt.Errorf("解析 FRPC 配置失败：%w", err)
		}
		delete(document, "ladder_frpc")
	}
	clean, err := stdjson.Marshal(document)
	if err != nil {
		return "", nil, fmt.Errorf("编码 sing-box 配置失败：%w", err)
	}
	return string(clean), configurations, nil
}

func (r *BoxRuntime) writeCurrent(dataDir, configJSON string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dataDir, "current.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(configJSON), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type trafficState struct {
	Uplink   int64 `json:"uplink"`
	Downlink int64 `json:"downlink"`
}

func (r *BoxRuntime) loadTraffic() {
	if r.dataDir == "" {
		return
	}
	path := filepath.Join(r.dataDir, "traffic.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var state trafficState
	if err := stdjson.Unmarshal(data, &state); err == nil {
		r.mu.Lock()
		r.prevUplink = state.Uplink
		r.prevDownlink = state.Downlink
		r.mu.Unlock()
	}
}

func (r *BoxRuntime) saveTraffic() {
	if r.dataDir == "" {
		return
	}
	r.mu.Lock()
	var up, down int64
	if r.tracker != nil {
		_, u, d := r.tracker.Snapshot()
		up, down = u, d
	}
	totalUp := up + r.prevUplink
	totalDown := down + r.prevDownlink
	r.mu.Unlock()

	state := trafficState{
		Uplink:   totalUp,
		Downlink: totalDown,
	}
	data, err := stdjson.Marshal(state)
	if err != nil {
		return
	}
	path := filepath.Join(r.dataDir, "traffic.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// startTrafficPersistLoopLocked launches the periodic traffic saver if needed.
// Caller must hold applyMu (or be the constructor).
func (r *BoxRuntime) startTrafficPersistLoopLocked() {
	if r.dataDir == "" || r.stopTrafficPersist != nil {
		return
	}
	r.stopTrafficPersist = make(chan struct{})
	r.trafficPersistDone = make(chan struct{})
	go r.trafficPersistLoop(r.stopTrafficPersist, r.trafficPersistDone)
}

// stopTrafficPersistLoopLocked stops the periodic saver and waits for it to
// exit. Caller must hold applyMu.
func (r *BoxRuntime) stopTrafficPersistLoopLocked() {
	if r.stopTrafficPersist == nil {
		return
	}
	close(r.stopTrafficPersist)
	<-r.trafficPersistDone
	r.stopTrafficPersist = nil
	r.trafficPersistDone = nil
}

func (r *BoxRuntime) trafficPersistLoop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			r.saveTraffic()
		}
	}
}
