// Package uplink reports status to Panel and pulls config over the existing HTTP API.
package uplink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ladderairport/agent/internal/panelhttp"
	agentv1 "github.com/ladderairport/proto/gen/go/agent/v1"
)

// Runtime is the local Agent control surface used to apply pulled config and
// execute queued immediate commands. It is satisfied by *control.Server.
type Runtime interface {
	GetStatus(context.Context, *agentv1.GetStatusRequest) (*agentv1.GetStatusResponse, error)
	GetMetrics(context.Context, *agentv1.GetMetricsRequest) (*agentv1.GetMetricsResponse, error)
	Ping(context.Context, *agentv1.PingRequest) (*agentv1.PingResponse, error)
	ApplyConfig(context.Context, *agentv1.ApplyConfigRequest) (*agentv1.ApplyConfigResponse, error)
	ApplyFRPServerConfig(context.Context, *agentv1.ApplyFRPServerConfigRequest) (*agentv1.ApplyFRPServerConfigResponse, error)
	Start(context.Context, *agentv1.StartRequest) (*agentv1.StartResponse, error)
	Stop(context.Context, *agentv1.StopRequest) (*agentv1.StopResponse, error)

	// Command-queue surface: uplink nodes execute these locally in response to
	// commands pulled from Panel, instead of exposing a live gRPC control port.
	ProbeOutbound(context.Context, *agentv1.ProbeOutboundRequest) (*agentv1.ProbeOutboundResponse, error)
	ListInterfaces(context.Context, *agentv1.ListInterfacesRequest) (*agentv1.ListInterfacesResponse, error)
	UpgradeAgent(context.Context, *agentv1.UpgradeAgentRequest) (*agentv1.UpgradeAgentResponse, error)
	GetNodeMetrics(context.Context, *agentv1.GetNodeMetricsRequest) (*agentv1.GetNodeMetricsResponse, error)
	GetBBRStatus(context.Context, *agentv1.GetBBRStatusRequest) (*agentv1.GetBBRStatusResponse, error)
	SetBBR(context.Context, *agentv1.SetBBRRequest) (*agentv1.SetBBRResponse, error)
	GetFRPServerMappings(context.Context, *agentv1.GetFRPServerMappingsRequest) (*agentv1.GetFRPServerMappingsResponse, error)
	StartFRPServer(context.Context, *agentv1.StartFRPServerRequest) (*agentv1.StartFRPServerResponse, error)
	StopFRPServer(context.Context, *agentv1.StopFRPServerRequest) (*agentv1.StopFRPServerResponse, error)
	GetFRPServerStatus(context.Context, *agentv1.GetFRPServerStatusRequest) (*agentv1.GetFRPServerStatusResponse, error)
}

const (
	Capability         = "uplink-v1"
	headerConfigHash   = "X-Config-Hash"
	headerFRPSHash     = "X-FRPS-Hash"
	headerDesiredState = "X-Desired-State"
)

type configHead struct {
	ConfigHash   string
	FRPSHash     string
	DesiredState string
}

type Config struct {
	PanelURL    string
	NodeID      string
	Token       string
	ReportEvery time.Duration
	ConfigEvery time.Duration
	// CommandWait bounds a single command long-poll. Panel clamps it to <=25s.
	CommandWait time.Duration
	HTTPClient  *http.Client
	Control     Runtime
}

type Client struct {
	cfg Config

	// pollClient is a keep-alive client without a hard request timeout so a
	// held command long-poll is bounded by context, not the 30s client cap.
	pollClient *http.Client

	mu            sync.Mutex
	configHash    string
	frpsHash      string
	failStreak    int
	nextConfigTry time.Time
}

type reportBody struct {
	NodeID          string   `json:"node_id"`
	CollectedAtUnix int64    `json:"collected_at_unix"`
	RuntimeState    string   `json:"runtime_state"`
	ConfigHash      string   `json:"config_hash"`
	LastError       string   `json:"last_error"`
	AgentVersion    string   `json:"agent_version"`
	SingboxVersion  string   `json:"singbox_version"`
	CoreVersion     string   `json:"core_version,omitempty"`
	CoreCommit      string   `json:"core_commit,omitempty"`
	ClientType      string   `json:"client_type,omitempty"`
	ClientVersion   string   `json:"client_version,omitempty"`
	LuciVersion     string   `json:"luci_version,omitempty"`
	Capabilities    []string `json:"capabilities"`
	Connections     int64    `json:"connections"`
	UplinkBytes     int64    `json:"uplink_bytes"`
	DownlinkBytes   int64    `json:"downlink_bytes"`
	CPUPercent      float64  `json:"cpu_percent"`
	MemoryRSSBytes  int64    `json:"memory_rss_bytes"`
}

type syncBody struct {
	NodeID            string `json:"node_id"`
	AppliedConfigHash string `json:"applied_config_hash"`
	AppliedFRPSHash   string `json:"applied_frps_hash"`
}

type syncResponse struct {
	Changed      bool            `json:"changed"`
	DesiredState string          `json:"desired_state"`
	ConfigJSON   string          `json:"config_json"`
	ConfigHash   string          `json:"config_hash"`
	Replace      bool            `json:"replace"`
	FRPS         *frpsSyncConfig `json:"frps"`
	FRPSHash     string          `json:"frps_hash"`
}

type frpsSyncConfig struct {
	Enabled           bool            `json:"enabled"`
	BindAddr          string          `json:"bind_addr"`
	BindPort          int             `json:"bind_port"`
	ProxyBindAddr     string          `json:"proxy_bind_addr"`
	AllowPorts        []frpsPortRange `json:"allow_ports"`
	AuthToken         string          `json:"auth_token"`
	TLSForce          bool            `json:"tls_force"`
	MaxPortsPerClient int64           `json:"max_ports_per_client"`
}

type frpsPortRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.PanelURL) == "" || strings.TrimSpace(cfg.NodeID) == "" || strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("必须提供 Panel URL、节点 ID 和令牌")
	}
	if cfg.ReportEvery <= 0 {
		cfg.ReportEvery = 15 * time.Second
	}
	if cfg.ConfigEvery <= 0 {
		cfg.ConfigEvery = 60 * time.Second
	}
	if cfg.CommandWait <= 0 {
		cfg.CommandWait = 25 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = panelhttp.NewClient()
	}
	// The long-poll client shares the Transport but drops the per-request
	// timeout; each poll is instead bounded by the request context.
	pollClient := &http.Client{Transport: cfg.HTTPClient.Transport}
	return &Client{cfg: cfg, pollClient: pollClient}, nil
}

func (c *Client) Run(ctx context.Context) {
	c.report(ctx)
	c.syncConfig(ctx)
	reportTick := time.NewTicker(c.cfg.ReportEvery)
	defer reportTick.Stop()
	configTick := time.NewTicker(c.jitter(c.cfg.ConfigEvery))
	defer configTick.Stop()
	// The command loop is a self-driving long-poll on its own goroutine so it
	// dispatches queued operations within milliseconds without waiting on the
	// report/config tickers.
	go c.runCommandLoop(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-reportTick.C:
			c.report(ctx)
		case <-configTick.C:
			c.syncConfig(ctx)
			configTick.Reset(c.jitter(c.cfg.ConfigEvery))
		}
	}
}

func (c *Client) report(ctx context.Context) {
	if c.cfg.Control == nil {
		return
	}
	st, err := c.cfg.Control.GetStatus(ctx, &agentv1.GetStatusRequest{})
	if err != nil {
		log.Printf("uplink 读取状态失败：%v", err)
		return
	}
	metrics, _ := c.cfg.Control.GetMetrics(ctx, &agentv1.GetMetricsRequest{})
	ping, _ := c.cfg.Control.Ping(ctx, &agentv1.PingRequest{})
	body := reportBody{
		NodeID:          c.cfg.NodeID,
		CollectedAtUnix: time.Now().Unix(),
		RuntimeState:    st.GetState(),
		ConfigHash:      st.GetConfigHash(),
		LastError:       st.GetLastError(),
	}
	if ping != nil {
		body.AgentVersion = ping.GetAgentVersion()
		body.SingboxVersion = ping.GetSingboxVersion()
		body.CoreVersion = ping.GetCoreVersion()
		body.CoreCommit = ping.GetCoreCommit()
		body.ClientType = ping.GetClientType()
		body.ClientVersion = ping.GetClientVersion()
		body.LuciVersion = ping.GetLuciVersion()
		body.Capabilities = ping.GetCapabilities()
	}
	if metrics != nil {
		body.Connections = metrics.GetConnections()
		body.UplinkBytes = metrics.GetUplinkBytes()
		body.DownlinkBytes = metrics.GetDownlinkBytes()
		body.CPUPercent = metrics.GetCpuPercent()
		body.MemoryRSSBytes = metrics.GetMemoryRssBytes()
	}
	if _, err := c.postJSON(ctx, "/api/v1/agent/report", body); err != nil {
		log.Printf("uplink 上报失败：%v", err)
	}
}

func (c *Client) syncConfig(ctx context.Context) {
	c.mu.Lock()
	if !c.nextConfigTry.IsZero() && time.Now().Before(c.nextConfigTry) {
		c.mu.Unlock()
		return
	}
	applied := c.configHash
	frpsHash := c.frpsHash
	c.mu.Unlock()

	head, err := c.headConfig(ctx)
	if err == nil && head.ConfigHash == applied && head.FRPSHash == frpsHash {
		if err := c.applySync(ctx, syncResponse{Changed: false, DesiredState: head.DesiredState}); err != nil {
			log.Printf("uplink 应用期望状态失败：%v", err)
			c.noteSyncFailure()
			return
		}
		c.clearSyncFailure()
		return
	}

	raw, err := c.postJSON(ctx, "/api/v1/agent/config-sync", syncBody{
		NodeID:            c.cfg.NodeID,
		AppliedConfigHash: applied,
		AppliedFRPSHash:   frpsHash,
	})
	if err != nil {
		log.Printf("uplink 拉取配置失败：%v", err)
		c.noteSyncFailure()
		return
	}
	var resp syncResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		log.Printf("uplink 解析配置响应失败：%v", err)
		c.noteSyncFailure()
		return
	}
	if err := c.applySync(ctx, resp); err != nil {
		log.Printf("uplink 应用配置失败：%v", err)
		c.noteSyncFailure()
		return
	}
	c.clearSyncFailure()
}

func (c *Client) headConfig(ctx context.Context) (configHead, error) {
	endpoint := strings.TrimRight(c.cfg.PanelURL, "/") + "/api/v1/agent/config-sync?node_id=" + url.QueryEscape(c.cfg.NodeID)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return configHead{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return configHead{}, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 300 {
		return configHead{}, fmt.Errorf("Panel 返回 HTTP %d", resp.StatusCode)
	}
	head := configHead{
		ConfigHash:   strings.TrimSpace(resp.Header.Get(headerConfigHash)),
		FRPSHash:     strings.TrimSpace(resp.Header.Get(headerFRPSHash)),
		DesiredState: strings.TrimSpace(resp.Header.Get(headerDesiredState)),
	}
	if head.ConfigHash == "" {
		return configHead{}, fmt.Errorf("HEAD 未返回配置哈希")
	}
	return head, nil
}

func (c *Client) clearSyncFailure() {
	c.mu.Lock()
	c.failStreak = 0
	c.nextConfigTry = time.Time{}
	c.mu.Unlock()
}

func (c *Client) applySync(ctx context.Context, resp syncResponse) error {
	if c.cfg.Control == nil {
		return fmt.Errorf("控制面尚未初始化")
	}
	if resp.Changed && resp.ConfigJSON != "" {
		out, err := c.cfg.Control.ApplyConfig(ctx, &agentv1.ApplyConfigRequest{
			ConfigJson: resp.ConfigJSON,
			ConfigHash: resp.ConfigHash,
			Replace:    resp.Replace,
		})
		if err != nil {
			return err
		}
		if !out.GetOk() {
			return fmt.Errorf("%s", nonempty(out.GetMessage(), "配置下发失败"))
		}
		c.mu.Lock()
		c.configHash = resp.ConfigHash
		c.mu.Unlock()
		log.Printf("uplink 已成功应用最新配置：hash=%s", resp.ConfigHash)
	}
	if resp.Changed && resp.FRPS == nil && resp.ConfigJSON == "" {
		c.mu.Lock()
		c.frpsHash = resp.FRPSHash
		c.mu.Unlock()
	}
	if resp.Changed && resp.FRPS != nil {
		req := &agentv1.ApplyFRPServerConfigRequest{
			ConfigHash: resp.FRPSHash,
			Config: &agentv1.FRPServerConfig{
				Enabled:           resp.FRPS.Enabled,
				BindAddr:          resp.FRPS.BindAddr,
				BindPort:          uint32(resp.FRPS.BindPort),
				ProxyBindAddr:     resp.FRPS.ProxyBindAddr,
				AuthToken:         resp.FRPS.AuthToken,
				TlsForce:          resp.FRPS.TLSForce,
				MaxPortsPerClient: resp.FRPS.MaxPortsPerClient,
			},
		}
		for _, portRange := range resp.FRPS.AllowPorts {
			req.Config.AllowPorts = append(req.Config.AllowPorts, &agentv1.FRPServerPortRange{
				Start: uint32(portRange.Start), End: uint32(portRange.End),
			})
		}
		out, err := c.cfg.Control.ApplyFRPServerConfig(ctx, req)
		if err != nil {
			return err
		}
		if !out.GetOk() {
			return fmt.Errorf("%s", nonempty(out.GetMessage(), "FRPS 配置下发失败"))
		}
		c.mu.Lock()
		c.frpsHash = resp.FRPSHash
		c.mu.Unlock()
	}
	switch strings.TrimSpace(resp.DesiredState) {
	case "stopped":
		if _, err := c.cfg.Control.Stop(ctx, &agentv1.StopRequest{}); err != nil {
			return err
		}
	case "running", "":
		st, err := c.cfg.Control.GetStatus(ctx, &agentv1.GetStatusRequest{})
		if err != nil {
			return err
		}
		if st.GetState() != "running" {
			if _, err := c.cfg.Control.Start(ctx, &agentv1.StartRequest{}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) noteSyncFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failStreak++
	delay := time.Minute
	switch {
	case c.failStreak >= 3:
		delay = 15 * time.Minute
	case c.failStreak == 2:
		delay = 5 * time.Minute
	}
	c.nextConfigTry = time.Now().Add(delay)
}

func (c *Client) postJSON(ctx context.Context, path string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(c.cfg.PanelURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Panel 返回 HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

// getJSON issues a GET bounded by a per-request context deadline. It uses the
// timeout-less pollClient so long-poll waits are not cut short by the shared
// client's 30s cap; callers pass a deadline that exceeds the server wait.
func (c *Client) getJSON(ctx context.Context, path string, timeout time.Duration) ([]byte, error) {
	reqCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	endpoint := strings.TrimRight(c.cfg.PanelURL, "/") + path
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	resp, err := c.pollClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Panel 返回 HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

func (c *Client) jitter(base time.Duration) time.Duration {
	if base <= 0 {
		return time.Second
	}
	delta := time.Duration(rand.Int63n(int64(base/5) + 1))
	if rand.Intn(2) == 0 {
		return base - delta/2
	}
	return base + delta/2
}

func nonempty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
