package api

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ladderairport/panel/internal/nodeconfig"
	"github.com/ladderairport/panel/internal/store"
)

type agentReportRequest struct {
	NodeID          string   `json:"node_id"`
	CollectedAtUnix int64    `json:"collected_at_unix"`
	RuntimeState    string   `json:"runtime_state"`
	ConfigHash      string   `json:"config_hash"`
	LastError       string   `json:"last_error"`
	AgentVersion    string   `json:"agent_version"`
	SingboxVersion  string   `json:"singbox_version"`
	CoreVersion     string   `json:"core_version"`
	CoreCommit      string   `json:"core_commit"`
	ClientType      string   `json:"client_type"`
	ClientVersion   string   `json:"client_version"`
	LuciVersion     string   `json:"luci_version"`
	Capabilities    []string `json:"capabilities"`
	Connections     *int64   `json:"connections"`
	UplinkBytes     *int64   `json:"uplink_bytes"`
	DownlinkBytes   *int64   `json:"downlink_bytes"`
	CPUPercent      *float64 `json:"cpu_percent"`
	MemoryRSSBytes  *int64   `json:"memory_rss_bytes"`
}

type agentConfigSyncRequest struct {
	NodeID            string `json:"node_id"`
	AppliedConfigHash string `json:"applied_config_hash"`
	AppliedFRPSHash   string `json:"applied_frps_hash"`
}

type agentConfigSyncResponse struct {
	Changed      bool               `json:"changed"`
	DesiredState string             `json:"desired_state"`
	ConfigJSON   string             `json:"config_json,omitempty"`
	ConfigHash   string             `json:"config_hash,omitempty"`
	Replace      bool               `json:"replace,omitempty"`
	FRPS         *frpsDesiredConfig `json:"frps,omitempty"`
	FRPSHash     string             `json:"frps_hash,omitempty"`
}

type agentConfigHeadResponse struct {
	ConfigHash   string `json:"config_hash"`
	FRPSHash     string `json:"frps_hash,omitempty"`
	DesiredState string `json:"desired_state"`
}

const (
	headerConfigHash   = "X-Config-Hash"
	headerFRPSHash     = "X-FRPS-Hash"
	headerDesiredState = "X-Desired-State"
)

func (s *Server) authenticateAgentNode(w http.ResponseWriter, r *http.Request, nodeID string) (*store.Node, bool) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		writeError(w, http.StatusBadRequest, "必须提供节点 ID")
		return nil, false
	}
	n, err := s.Store.GetNode(nodeID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "节点不存在")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return nil, false
	}
	token := bearerToken(r)
	want := strings.TrimSpace(n.Token)
	if want == "" || token == "" || subtle.ConstantTimeCompare([]byte(want), []byte(token)) != 1 {
		writeError(w, http.StatusUnauthorized, "节点令牌无效")
		return nil, false
	}
	return n, true
}

func (s *Server) handleAgentReport(w http.ResponseWriter, r *http.Request) {
	var req agentReportRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	if _, ok := s.authenticateAgentNode(w, r, req.NodeID); !ok {
		return
	}
	applied, err := s.Store.ApplyNodeReport(req.NodeID, reportFromRequest(req))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": applied})
}

// reportFromRequest maps a decoded agent report body onto a store.NodeReport.
// It is shared by the HTTP report endpoint and the WebSocket uplink so both
// transports update node state identically.
func reportFromRequest(req agentReportRequest) store.NodeReport {
	report := store.NodeReport{
		CollectedAtUnix: req.CollectedAtUnix,
		Status:          "online",
		RuntimeState:    strings.TrimSpace(req.RuntimeState),
		ConfigHash:      strings.TrimSpace(req.ConfigHash),
		LastError:       req.LastError,
		AgentVersion:    strings.TrimSpace(req.AgentVersion),
		SingboxVersion:  strings.TrimSpace(req.SingboxVersion),
		CoreVersion:     strings.TrimSpace(req.CoreVersion),
		CoreCommit:      strings.TrimSpace(req.CoreCommit),
		ClientType:      strings.TrimSpace(req.ClientType),
		ClientVersion:   strings.TrimSpace(req.ClientVersion),
		LuciVersion:     strings.TrimSpace(req.LuciVersion),
	}
	if req.Capabilities != nil {
		report.HasCapabilities = true
		report.Capabilities = req.Capabilities
	}
	if req.Connections != nil || req.UplinkBytes != nil || req.DownlinkBytes != nil ||
		req.CPUPercent != nil || req.MemoryRSSBytes != nil {
		report.HasMetrics = true
		if req.Connections != nil {
			report.Connections = *req.Connections
		}
		if req.UplinkBytes != nil {
			report.UplinkBytes = *req.UplinkBytes
		}
		if req.DownlinkBytes != nil {
			report.DownlinkBytes = *req.DownlinkBytes
		}
		if req.CPUPercent != nil {
			report.CPUPercent = *req.CPUPercent
		}
		if req.MemoryRSSBytes != nil {
			report.MemoryRSSBytes = *req.MemoryRSSBytes
		}
	}
	return report
}

func (s *Server) handleAgentConfigHead(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticateAgentNode(w, r, r.URL.Query().Get("node_id"))
	if !ok {
		return
	}
	cfg, frpsHash, desired, err := s.agentDesiredMeta(node)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	setAgentConfigHashHeaders(w, cfg.Hash, frpsHash, desired)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, agentConfigHeadResponse{
		ConfigHash:   cfg.Hash,
		FRPSHash:     frpsHash,
		DesiredState: desired,
	})
}

func (s *Server) handleAgentConfigSync(w http.ResponseWriter, r *http.Request) {
	var req agentConfigSyncRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	node, ok := s.authenticateAgentNode(w, r, req.NodeID)
	if !ok {
		return
	}
	cfg, frps, frpsHash, desired, err := s.agentDesiredConfig(node)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	configChanged := strings.TrimSpace(req.AppliedConfigHash) != cfg.Hash
	frpsChanged := strings.TrimSpace(req.AppliedFRPSHash) != frpsHash
	if !configChanged && !frpsChanged {
		writeJSON(w, http.StatusOK, agentConfigSyncResponse{
			Changed:      false,
			DesiredState: desired,
		})
		return
	}

	if configChanged {
		if err := s.Store.SaveSnapshot(&store.ConfigSnapshot{
			NodeID:     node.ID,
			ConfigJSON: cfg.JSON,
			ConfigHash: cfg.Hash,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("保存配置快照失败：%v", err))
			return
		}
	}

	resp := agentConfigSyncResponse{
		Changed:      true,
		DesiredState: desired,
	}
	if configChanged {
		resp.ConfigJSON = cfg.JSON
		resp.ConfigHash = cfg.Hash
		resp.Replace = true
	}
	if frpsChanged {
		resp.FRPS = frps
		resp.FRPSHash = frpsHash
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) agentDesiredMeta(node *store.Node) (nodeconfig.Result, string, string, error) {
	cfg, _, frpsHash, desired, err := s.agentDesiredConfig(node)
	return cfg, frpsHash, desired, err
}

func (s *Server) agentDesiredConfig(node *store.Node) (nodeconfig.Result, *frpsDesiredConfig, string, string, error) {
	desired := node.DesiredRuntime
	if desired == "" {
		desired = store.DesiredRuntimeRunning
	}
	if s.Runner != nil && s.Runner.Coordinator != nil {
		s.Runner.Coordinator.Lock()
		defer s.Runner.Coordinator.Unlock()
	}
	builder := &nodeconfig.Builder{Store: s.Store}
	if s.Runner != nil && s.Runner.ConfigBuilder != nil {
		builder = s.Runner.ConfigBuilder
	}
	cfg, err := builder.Build(node.ID)
	if err != nil {
		return nodeconfig.Result{}, nil, "", "", fmt.Errorf("构建配置失败：%w", err)
	}
	frps, frpsHash, err := s.desiredFRPSForSync(node.ID)
	if err != nil {
		return nodeconfig.Result{}, nil, "", "", err
	}
	return cfg, frps, frpsHash, desired, nil
}

func setAgentConfigHashHeaders(w http.ResponseWriter, configHash, frpsHash, desired string) {
	w.Header().Set(headerConfigHash, configHash)
	w.Header().Set(headerFRPSHash, frpsHash)
	w.Header().Set(headerDesiredState, desired)
}

func (s *Server) desiredFRPSForSync(nodeID string) (*frpsDesiredConfig, string, error) {
	config, err := s.Store.GetFRPServerConfig(nodeID)
	if err != nil {
		if isNotFound(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	desired := frpsDesiredConfig{
		Enabled:           config.Enabled,
		BindAddr:          config.BindAddr,
		BindPort:          config.BindPort,
		ProxyBindAddr:     config.ProxyBindAddr,
		AllowPorts:        config.AllowPorts,
		TLSForce:          config.TLSForce,
		MaxPortsPerClient: config.MaxPortsPerClient,
	}
	if config.AuthTokenCiphertext != "" && s.Secrets != nil {
		raw, decErr := s.Secrets.Decrypt(config.AuthTokenCiphertext, frpsTokenAAD(nodeID))
		if decErr != nil {
			return nil, "", fmt.Errorf("解密 FRPS 令牌失败：%w", decErr)
		}
		desired.AuthToken = string(raw)
	}
	hash := config.DesiredHash
	if hash == "" {
		var hashErr error
		hash, hashErr = hashFRPSDesired(desired)
		if hashErr != nil {
			return nil, "", hashErr
		}
	}
	return &desired, hash, nil
}

func uplinkStaleAfter() time.Duration {
	return 45 * time.Second
}
