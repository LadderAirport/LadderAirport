package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ladderairport/panel/internal/frpconnection"
	"github.com/ladderairport/panel/internal/nodeconfig"
	"github.com/ladderairport/panel/internal/store"
)

// nodeBootstrapRequest creates a node and returns a one-click agent install command.
type nodeBootstrapRequest struct {
	Name          string   `json:"name"`
	Address       string   `json:"address"` // optional control dial host; fill after install if empty
	GRPCPort      int      `json:"grpc_port"`
	PublicAddress string   `json:"public_address"` // optional subscription client host
	Token         string   `json:"token"`          // optional; auto-generated when empty
	Labels        []string `json:"labels"`
	AgentVersion  string   `json:"agent_version"`
	InstallScript string   `json:"install_script_url"` // optional override
	ControlMode   string   `json:"control_mode"`
}

type nodeCreateRequest struct {
	Name            string              `json:"name"`
	Address         string              `json:"address"`
	GRPCPort        int                 `json:"grpc_port"`
	Token           string              `json:"token"`
	Labels          []string            `json:"labels"`
	PublicAddress   string              `json:"public_address"`
	PortMappings    []store.PortMapping `json:"port_mappings"`
	EgressInterface string              `json:"egress_interface"`
	ControlMode     string              `json:"control_mode"`
}

// nodeInstallResponse is returned after bootstrap or when regenerating the install command.
type nodeInstallResponse struct {
	Node             store.Node `json:"node"`
	Token            string     `json:"token"`
	InstallCommand   string     `json:"install_command"`
	UpgradeCommand   string     `json:"upgrade_command,omitempty"`
	UninstallCommand string     `json:"uninstall_command,omitempty"`
	Steps            []string   `json:"steps"`
	PanelBaseURL     string     `json:"panel_base_url,omitempty"`
	// RecommendedAgentVersion is the latest known release tag (may be empty).
	RecommendedAgentVersion string `json:"recommended_agent_version,omitempty"`
	// Outdated is true when the node's core version looks behind recommended.
	// Wrapper versions are not compared.
	Outdated bool `json:"outdated,omitempty"`
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Annotate uplink nodes with their live WS channel state so the UI can show
	// whether a node currently supports full real-time (gRPC-parity) ops.
	for i := range list {
		list[i].UplinkWSConnected = s.uplinkWSConnected(list[i])
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	var req nodeCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "必须提供名称")
		return
	}
	mode := strings.TrimSpace(req.ControlMode)
	if mode != "" && mode != store.ControlModePush && mode != store.ControlModeUplink {
		writeError(w, http.StatusBadRequest, "control_mode 只能是 push 或 uplink")
		return
	}
	n := store.Node{
		Name:            req.Name,
		Address:         strings.TrimSpace(req.Address),
		GRPCPort:        req.GRPCPort,
		Token:           strings.TrimSpace(req.Token),
		Labels:          req.Labels,
		PublicAddress:   strings.TrimSpace(req.PublicAddress),
		PortMappings:    req.PortMappings,
		EgressInterface: strings.TrimSpace(req.EgressInterface),
		DDNSEnabled:     true,
		ControlMode:     mode,
	}
	// Address may be empty when the operator will install first and fill IP later.
	if n.GRPCPort == 0 && n.ControlMode != store.ControlModeUplink {
		n.GRPCPort = 50051
	}
	if n.Status == "" {
		if strings.TrimSpace(n.Address) == "" {
			n.Status = "pending"
		} else {
			n.Status = "unknown"
		}
	}
	if err := s.Store.CreateNode(&n); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

// handleBootstrapNode creates a node with an auto token and returns a one-click install command.
// POST /api/v1/nodes/bootstrap
func (s *Server) handleBootstrapNode(w http.ResponseWriter, r *http.Request) {
	var req nodeBootstrapRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "必须提供名称")
		return
	}
	mode := strings.TrimSpace(req.ControlMode)
	if mode == "" {
		mode = store.ControlModePush
	}
	if mode != store.ControlModePush && mode != store.ControlModeUplink {
		writeError(w, http.StatusBadRequest, "control_mode 只能是 push 或 uplink")
		return
	}
	if mode != store.ControlModeUplink && s.PKI == nil {
		writeError(w, http.StatusServiceUnavailable, "管理 PKI 不可用")
		return
	}
	panelBase := ""
	if st, err := s.Store.GetSettings(); err == nil {
		panelBase = panelBaseFromSettings(st.PublicBaseURL)
	}
	if panelBase == "" {
		writeError(w, http.StatusBadRequest, "创建节点前请先配置 Public Base URL")
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		var err error
		token, err = randomAgentToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "生成令牌失败："+err.Error())
			return
		}
	}
	port := req.GRPCPort
	if port == 0 && mode != store.ControlModeUplink {
		port = 50051
	}
	addr := strings.TrimSpace(req.Address)
	status := "unknown"
	if addr == "" {
		status = "pending"
	}
	n := store.Node{
		Name:          req.Name,
		Address:       addr,
		GRPCPort:      port,
		PublicAddress: strings.TrimSpace(req.PublicAddress),
		Token:         token,
		Labels:        req.Labels,
		Status:        status,
		DDNSEnabled:   true,
		ControlMode:   mode,
	}
	if err := s.Store.CreateNode(&n); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Re-read for assigned id/timestamps.
	created, err := s.Store.GetNode(n.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	enrollmentToken, err := s.Store.CreatePKIEnrollmentToken(created.ID, 15*time.Minute)
	if err != nil {
		_ = s.Store.DeleteNode(created.ID)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	agentVer := strings.TrimSpace(req.AgentVersion)
	cmd := buildInstallCommand(installCommandOpts{
		ScriptURL:       req.InstallScript,
		EnrollmentToken: enrollmentToken,
		AgentVersion:    agentVer,
		PanelBaseURL:    panelBase,
		NodeID:          created.ID,
		GRPCPort:        port,
		ReportAddress:   addr,
		Uplink:          created.ControlMode == store.ControlModeUplink,
	})
	rec, _, _ := resolveRecommendedAgentVersion()
	upgradeVer := agentVer
	if upgradeVer == "" || upgradeVer == "latest" {
		upgradeVer = rec
	}
	writeJSON(w, http.StatusCreated, nodeInstallResponse{
		Node:                    *created,
		Token:                   token,
		InstallCommand:          cmd,
		UpgradeCommand:          buildUpgradeCommand(installCommandOpts{ScriptURL: req.InstallScript, AgentVersion: upgradeVer}),
		UninstallCommand:        buildUninstallCommand(installCommandOpts{ScriptURL: req.InstallScript}, false),
		Steps:                   installSteps(addr, port, created.ControlMode == store.ControlModeUplink),
		PanelBaseURL:            panelBase,
		RecommendedAgentVersion: rec,
		Outdated:                isAgentVersionOutdated(nodeCoreVersion(*created), rec),
	})
}

// handleNodeInstallCommand regenerates the one-click install command for an existing node.
// POST /api/v1/nodes/{id}/install-command
// It may create a PKI enrollment token, so it is not a safe/read-only GET.
func (s *Server) handleNodeInstallCommand(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	n, err := s.Store.GetNode(id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	enrolled, enrolledErr := s.Store.IsAgentEnrolled(n.ID)
	if enrolledErr != nil {
		writeError(w, http.StatusInternalServerError, enrolledErr.Error())
		return
	}
	if nodeAwaitingRegistration(*n, enrolled) && n.ControlMode != store.ControlModeUplink && s.PKI == nil {
		writeError(w, http.StatusServiceUnavailable, "管理 PKI 不可用")
		return
	}
	q := r.URL.Query()
	version := q.Get("version")
	panelBase := ""
	if st, err := s.Store.GetSettings(); err == nil {
		panelBase = panelBaseFromSettings(st.PublicBaseURL)
	}
	if v := strings.TrimSpace(q.Get("panel")); v != "" {
		panelBase = panelBaseFromSettings(v)
	}
	if panelBase == "" {
		writeError(w, http.StatusBadRequest, "生成命令前请先配置 Public Base URL")
		return
	}
	scriptURL := q.Get("script_url")
	cmd := ""
	if nodeAwaitingRegistration(*n, enrolled) {
		enrollmentToken, tokenErr := s.Store.CreatePKIEnrollmentToken(n.ID, 15*time.Minute)
		if tokenErr != nil {
			writeError(w, http.StatusInternalServerError, tokenErr.Error())
			return
		}
		cmd = buildInstallCommand(installCommandOpts{
			ScriptURL:       scriptURL,
			EnrollmentToken: enrollmentToken,
			AgentVersion:    version,
			PanelBaseURL:    panelBase,
			NodeID:          n.ID,
			GRPCPort:        n.GRPCPort,
			ReportAddress:   n.Address,
			Uplink:          n.ControlMode == store.ControlModeUplink,
		})
	}
	rec, _, _ := resolveRecommendedAgentVersion()
	upgradeVer := strings.TrimSpace(version)
	if upgradeVer == "" || upgradeVer == "latest" {
		upgradeVer = rec
	}
	upgradeCmd := ""
	if linuxReleaseUpgradeAllowed(n.ClientType) {
		upgradeCmd = buildUpgradeCommand(installCommandOpts{ScriptURL: scriptURL, AgentVersion: upgradeVer})
	}
	writeJSON(w, http.StatusOK, nodeInstallResponse{
		Node:                    *n,
		Token:                   n.Token,
		InstallCommand:          cmd,
		UpgradeCommand:          upgradeCmd,
		UninstallCommand:        buildUninstallCommand(installCommandOpts{ScriptURL: scriptURL}, false),
		Steps:                   installSteps(n.Address, n.GRPCPort, n.ControlMode == store.ControlModeUplink),
		PanelBaseURL:            panelBase,
		RecommendedAgentVersion: rec,
		Outdated:                isAgentVersionOutdated(nodeCoreVersion(*n), rec),
	})
}

func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_, err := s.Store.GetNode(id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body store.NodeOperatorUpdate
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err)
		return
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "名称不能为空")
			return
		}
		body.Name = &name
	}
	if body.Address != nil {
		address := strings.TrimSpace(*body.Address)
		body.Address = &address
	}
	if body.GRPCPort != nil {
		if *body.GRPCPort < 1 || *body.GRPCPort > 65535 {
			writeError(w, http.StatusBadRequest, "grpc_port 必须在 1 到 65535 之间")
			return
		}
	}
	if body.Token != nil {
		token := strings.TrimSpace(*body.Token)
		body.Token = &token
	}
	if body.PublicAddress != nil {
		publicAddress := strings.TrimSpace(*body.PublicAddress)
		body.PublicAddress = &publicAddress
	}
	if body.EgressInterface != nil {
		egressInterface := strings.TrimSpace(*body.EgressInterface)
		body.EgressInterface = &egressInterface
	}
	if body.DNS != nil {
		cfg := *body.DNS
		if err := cfg.Normalize(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		body.DNS = &cfg
	}
	if body.ControlMode != nil {
		mode := strings.TrimSpace(*body.ControlMode)
		body.ControlMode = &mode
		current, getErr := s.Store.GetNode(id)
		if getErr != nil {
			writeError(w, http.StatusInternalServerError, getErr.Error())
			return
		}
		if mode == store.ControlModeUplink {
			if !hasCapability(current.Capabilities, "uplink-v1") && current.ControlMode != store.ControlModeUplink {
				writeError(w, http.StatusBadRequest, "节点未上报 uplink-v1，请先升级 Agent")
				return
			}
		}
		if mode == store.ControlModePush && current.ControlMode == store.ControlModeUplink && current.PKICertSerial == "" {
			enrolled, enrolledErr := s.Store.IsAgentEnrolled(current.ID)
			if enrolledErr != nil {
				writeError(w, http.StatusInternalServerError, enrolledErr.Error())
				return
			}
			// A node that has not registered yet can still switch; the next
			// install command will include PKI. One that already enrolled
			// without a certificate cannot serve mTLS.
			if enrolled {
				writeError(w, http.StatusBadRequest, "该节点没有管理面证书，不能直接切回 push")
				return
			}
		}
	}
	if err := s.Store.UpdateNodeOperatorFields(id, body); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, err := s.Store.GetNode(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if err := s.Store.DeleteNode(id); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListNodeInbounds(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if _, err := s.Store.GetNode(id); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	list, err := s.Store.ListNodeInboundAttachments(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type setNodeInboundsBody struct {
	// InboundIDs is the legacy field (no per-inbound NAT). Prefer Bindings.
	InboundIDs []string `json:"inbound_ids"`
	// Bindings attaches inbounds with optional public_address / public_port overrides.
	// When non-nil, takes precedence over InboundIDs.
	Bindings []store.NodeInboundBinding `json:"bindings"`
	// SkipDeploy when true only updates association (tests / advanced). Default: auto apply+start.
	SkipDeploy bool `json:"skip_deploy"`
}

// setNodeInboundsResponse is returned after attaching inbounds; deploy is implicit.
type setNodeInboundsResponse struct {
	// Inbounds keeps legacy shape for older clients (without NAT fields).
	Inbounds []store.InboundConfig `json:"inbounds"`
	// Attachments includes per-inbound public_address / public_port.
	Attachments   []store.NodeInboundAttachment `json:"attachments"`
	Deployed      bool                          `json:"deployed"`
	DeployMessage string                        `json:"deploy_message,omitempty"`
	ApplyTask     *store.Task                   `json:"apply_task,omitempty"`
	StartTask     *store.Task                   `json:"start_task,omitempty"`
}

func (s *Server) handleSetNodeInbounds(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body setNodeInboundsBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err)
		return
	}
	bindings := body.Bindings
	if bindings == nil {
		// legacy: inbound_ids only
		if body.InboundIDs == nil {
			body.InboundIDs = []string{}
		}
		bindings = make([]store.NodeInboundBinding, 0, len(body.InboundIDs))
		for _, iid := range body.InboundIDs {
			bindings = append(bindings, store.NodeInboundBinding{InboundID: iid})
		}
	}
	for _, binding := range bindings {
		inbound, err := s.Store.GetInbound(binding.InboundID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_, active, err := frpconnection.ParseParams(map[string]any{
			"frp_enabled": binding.FRPEnabled, "frpc_config": binding.FRPCConfig,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if active && (inbound.Protocol == "hysteria2" || inbound.Protocol == "tuic") {
			writeError(w, http.StatusBadRequest, "当前 FRP 仅支持 TCP 入站")
			return
		}
	}
	if err := s.Store.SetNodeInboundBindings(id, bindings); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	atts, err := s.Store.ListNodeInboundAttachments(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	list := make([]store.InboundConfig, 0, len(atts))
	for _, a := range atts {
		list = append(list, a.InboundConfig)
	}

	out := setNodeInboundsResponse{
		Inbounds:    list,
		Attachments: atts,
	}
	if body.SkipDeploy {
		out.DeployMessage = "关联已保存（未下发）"
		writeJSON(w, http.StatusOK, out)
		return
	}

	// Auto deploy via Apply only (agent starts core inside Apply; no separate Start).
	node, err := s.Store.GetNode(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node.ControlMode == store.ControlModeUplink {
		out.DeployMessage = "关联已保存；uplink 节点将在下次拉取时应用"
		writeJSON(w, http.StatusOK, out)
		return
	}
	if strings.TrimSpace(node.Address) == "" {
		out.DeployMessage = "关联已保存；节点地址未就绪，上线后由 bootstrap 同步"
		writeJSON(w, http.StatusOK, out)
		return
	}
	if s.Runner == nil {
		out.DeployMessage = "关联已保存；runner 未配置，未下发"
		writeJSON(w, http.StatusOK, out)
		return
	}

	applyTask, depMsg := s.deployNodeInbounds(r.Context(), id)
	out.ApplyTask = applyTask
	out.DeployMessage = depMsg
	if applyTask != nil {
		for _, res := range applyTask.Results {
			if res.OK {
				out.Deployed = true
				break
			}
		}
		if applyTask.Status == "success" {
			out.Deployed = true
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// deployNodeInbounds runs a single apply for one node (core starts with Apply).
func (s *Server) deployNodeInbounds(ctx context.Context, nodeID string) (applyTask *store.Task, msg string) {
	applyTask = &store.Task{
		Type:    "apply",
		Status:  "pending",
		NodeIDs: []string{nodeID},
	}
	if err := s.Store.CreateTask(applyTask); err != nil {
		return nil, "关联已保存；创建下发任务失败: " + err.Error()
	}
	if err := s.Runner.RunTask(ctx, applyTask.ID); err != nil {
		t, _ := s.Store.GetTask(applyTask.ID)
		return t, "关联已保存；下发配置失败: " + err.Error()
	}
	applyTask, _ = s.Store.GetTask(applyTask.ID)
	return applyTask, deployMsgFromApply(applyTask)
}

func deployMsgFromApply(applyTask *store.Task) string {
	if applyTask == nil {
		return "关联已保存；下发结果未知"
	}
	if applyTask.Status == "success" {
		return "已关联并下发配置（核心已启动）"
	}
	for _, res := range applyTask.Results {
		if res.OK {
			return "已关联并下发配置（核心已启动）"
		}
		if res.Message != "" {
			return "关联已保存；下发: " + res.Message
		}
	}
	return "关联已保存；下发状态=" + applyTask.Status
}

func (s *Server) handleNodePreview(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	node, err := s.Store.GetNode(id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	builder := &nodeconfig.Builder{Store: s.Store}
	if s.Runner != nil && s.Runner.ConfigBuilder != nil {
		builder = s.Runner.ConfigBuilder
	}
	built, err := builder.Build(node.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var cfg any
	if err := json.Unmarshal([]byte(built.JSON), &cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleNodeApply(w http.ResponseWriter, r *http.Request) {
	s.runSingleNodeTask(w, r, "apply")
}

func (s *Server) handleNodeStart(w http.ResponseWriter, r *http.Request) {
	s.runSingleNodeTask(w, r, "start")
}

func (s *Server) handleNodeStop(w http.ResponseWriter, r *http.Request) {
	s.runSingleNodeTask(w, r, "stop")
}

func (s *Server) runSingleNodeTask(w http.ResponseWriter, r *http.Request, taskType string) {
	id := pathID(r)
	node, err := s.Store.GetNode(id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node.ControlMode == store.ControlModeUplink {
		s.finishUplinkIntent(w, id, taskType)
		return
	}
	if s.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "批处理执行器尚未配置")
		return
	}
	task := &store.Task{
		Type:    taskType,
		Status:  "pending",
		NodeIDs: []string{id},
	}
	if err := s.Store.CreateTask(task); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Runner.RunTask(r.Context(), task.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, err := s.Store.GetTask(task.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) finishUplinkIntent(w http.ResponseWriter, nodeID, taskType string) {
	message := "已记录，节点将在下次拉取时应用"
	switch taskType {
	case "start":
		state := store.DesiredRuntimeRunning
		if err := s.Store.UpdateNodeOperatorFields(nodeID, store.NodeOperatorUpdate{DesiredRuntime: &state}); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	case "stop":
		state := store.DesiredRuntimeStopped
		if err := s.Store.UpdateNodeOperatorFields(nodeID, store.NodeOperatorUpdate{DesiredRuntime: &state}); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	task := &store.Task{
		Type:    taskType,
		Status:  "success",
		NodeIDs: []string{nodeID},
		Results: []store.TaskNodeResult{{NodeID: nodeID, OK: true, Message: message}},
	}
	if err := s.Store.CreateTask(task); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, err := s.Store.GetTask(task.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleProbeNode(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	node, err := s.Store.GetNode(id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node.ControlMode == store.ControlModeUplink {
		// Prefer the live WS uplink so probe hits the agent in real time, at
		// parity with a push node's gRPC control plane. Fall back to the last
		// report only when no socket is connected.
		if _, connected := s.uplinkClient(node.ID); !connected {
			writeJSON(w, http.StatusOK, cachedProbeResponse(node, "uplink 节点未建立实时通道，使用最近一次上报"))
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.opTimeout())
	defer cancel()

	client, live, err := s.liveClientFor(ctx, node)
	if err != nil {
		node.Status = "unreachable"
		_ = s.Store.UpdateNode(node)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("连接节点失败：%v", err))
		return
	}
	if !live {
		// Uplink node lost its socket between the check above and now.
		writeJSON(w, http.StatusOK, cachedProbeResponse(node, "uplink 节点未建立实时通道，使用最近一次上报"))
		return
	}
	defer func() { _ = client.Close() }()

	resp, err := client.Ping(ctx)
	if err != nil {
		node.Status = classifyRPCError(err)
		node.LastError = err.Error()
		_ = s.Store.UpdateNode(node)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	node.Status = "online"
	node.LastSeenUnix = time.Now().Unix()
	applyPingIdentity(node, resp)
	node.Capabilities = append([]string{}, resp.GetCapabilities()...)
	node.LastError = ""
	// Best-effort status/metrics on single probe.
	if st, err := client.GetStatus(ctx); err == nil {
		node.RuntimeState = st.GetState()
		if st.GetConfigHash() != "" {
			node.ConfigHash = st.GetConfigHash()
		}
	}
	if m, err := client.GetMetrics(ctx); err == nil {
		node.Connections = m.GetConnections()
		node.UplinkBytes = m.GetUplinkBytes()
		node.DownlinkBytes = m.GetDownlinkBytes()
		node.CPUPercent = m.GetCpuPercent()
		node.MemoryRSSBytes = m.GetMemoryRssBytes()
		node.MetricsAtUnix = time.Now().Unix()
	}
	if err := s.Store.UpdateNode(node); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	node.UplinkWSConnected = s.uplinkWSConnected(*node)
	writeJSON(w, http.StatusOK, map[string]any{
		"node":            node,
		"agent_version":   resp.GetAgentVersion(),
		"singbox_version": resp.GetSingboxVersion(),
		"core_version":    resp.GetCoreVersion(),
		"core_commit":     resp.GetCoreCommit(),
		"client_type":     resp.GetClientType(),
		"client_version":  resp.GetClientVersion(),
		"luci_version":    resp.GetLuciVersion(),
		"capabilities":    resp.GetCapabilities(),
	})
}

func (s *Server) handleNodeMetrics(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	node, err := s.Store.GetNode(id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node.ControlMode == store.ControlModeUplink {
		// Serve the cached report unless a live WS socket lets us query the
		// agent directly for fresh counters.
		if _, connected := s.uplinkClient(node.ID); !connected {
			writeJSON(w, http.StatusOK, map[string]any{
				"connections":      node.Connections,
				"uplink_bytes":     node.UplinkBytes,
				"downlink_bytes":   node.DownlinkBytes,
				"cpu_percent":      node.CPUPercent,
				"memory_rss_bytes": node.MemoryRSSBytes,
			})
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.opTimeout())
	defer cancel()

	client, live, err := s.liveClientFor(ctx, node)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("连接节点失败：%v", err))
		return
	}
	if !live {
		writeJSON(w, http.StatusOK, map[string]any{
			"connections":      node.Connections,
			"uplink_bytes":     node.UplinkBytes,
			"downlink_bytes":   node.DownlinkBytes,
			"cpu_percent":      node.CPUPercent,
			"memory_rss_bytes": node.MemoryRSSBytes,
		})
		return
	}
	defer func() { _ = client.Close() }()

	resp, err := client.GetMetrics(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connections":      resp.GetConnections(),
		"uplink_bytes":     resp.GetUplinkBytes(),
		"downlink_bytes":   resp.GetDownlinkBytes(),
		"cpu_percent":      resp.GetCpuPercent(),
		"memory_rss_bytes": resp.GetMemoryRssBytes(),
	})
}

func (s *Server) handleNodeInterfaces(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	node, err := s.Store.GetNode(id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node.ControlMode == store.ControlModeUplink {
		// Prefer the live WS socket; only enqueue a queued command when the node
		// has no real-time channel.
		if _, connected := s.uplinkClient(node.ID); !connected {
			s.enqueueUplinkCommand(w, node.ID, cmdInterfaces, nil)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.opTimeout())
	defer cancel()

	client, live, err := s.liveClientFor(ctx, node)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("连接节点失败：%v", err))
		return
	}
	if !live {
		s.enqueueUplinkCommand(w, node.ID, cmdInterfaces, nil)
		return
	}
	defer func() { _ = client.Close() }()

	resp, err := client.ListInterfaces(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	type ifaceJSON struct {
		Name         string   `json:"name"`
		Addresses    []string `json:"addresses"`
		Up           bool     `json:"up"`
		Loopback     bool     `json:"loopback"`
		MTU          int32    `json:"mtu"`
		HardwareAddr string   `json:"hardware_addr"`
	}
	list := make([]ifaceJSON, 0, len(resp.GetInterfaces()))
	for _, iface := range resp.GetInterfaces() {
		addrs := iface.GetAddresses()
		if addrs == nil {
			addrs = []string{}
		}
		list = append(list, ifaceJSON{
			Name:         iface.GetName(),
			Addresses:    addrs,
			Up:           iface.GetUp(),
			Loopback:     iface.GetLoopback(),
			MTU:          iface.GetMtu(),
			HardwareAddr: iface.GetHardwareAddr(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"interfaces": list})
}

func (s *Server) handleNodeLogs(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	node, err := s.Store.GetNode(id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node.ControlMode == store.ControlModeUplink {
		// Live log streaming requires a real-time channel. The WS uplink provides
		// one at parity with gRPC; without it, uplink has no way to stream.
		if _, connected := s.uplinkClient(node.ID); !connected {
			writeError(w, http.StatusConflict, errUplinkNoLiveRPC)
			return
		}
	}

	level := r.URL.Query().Get("level")
	var tail int32
	if t := r.URL.Query().Get("tail"); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil {
			writeError(w, http.StatusBadRequest, "日志行数参数 tail 无效")
			return
		}
		// Clamp into a sane positive int32 range.
		if n < 1 {
			n = 1
		} else if n > 10000 {
			n = 10000
		}
		tail = int32(n)
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "服务器不支持流式响应")
		return
	}

	ctx := r.Context()
	client, live, err := s.liveClientFor(ctx, node)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("连接节点失败：%v", err))
		return
	}
	if !live {
		writeError(w, http.StatusConflict, errUplinkNoLiveRPC)
		return
	}
	defer func() { _ = client.Close() }()

	stream, err := client.StreamLogs(ctx, level, tail)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Initial comment so clients see the stream open.
	_, _ = fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		line, err := stream.Recv()
		if err != nil {
			// Client disconnect or stream end — stop cleanly.
			return
		}
		payload, err := json.Marshal(map[string]any{
			"level":   line.GetLevel(),
			"message": line.GetMessage(),
			"ts":      line.GetTsUnixMs(),
		})
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return
		}
		flusher.Flush()
	}
}

// handleNodeUpgrade asks the agent to stage a release binary; the node root helper
// replaces the binary and restarts ladder-agent. Panel should re-probe afterwards.
// POST /api/v1/nodes/{id}/upgrade
// Body (optional): {"version":"v0.7.2","repo":"LadderAirport/LadderAirport","download_url":"","sha256":""}
func (s *Server) handleNodeUpgrade(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	node, err := s.Store.GetNode(id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body struct {
		Version     string `json:"version"`
		Repo        string `json:"repo"`
		DownloadURL string `json:"download_url"`
		SHA256      string `json:"sha256"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(w, r, &body); err != nil {
			writeDecodeError(w, err)
			return
		}
	}
	if !linuxReleaseUpgradeAllowed(node.ClientType) {
		writeError(w, http.StatusConflict, "该节点是 "+strings.TrimSpace(node.ClientType)+" 客户端，不能使用 Linux 发布包远程升级")
		return
	}
	version := strings.TrimSpace(body.Version)
	if version == "" {
		if tag, _, _ := resolveRecommendedAgentVersion(); tag != "" {
			version = tag
		} else {
			version = "latest"
		}
	}

	if node.ControlMode == store.ControlModeUplink {
		// Prefer the live WS socket for an immediate staged upgrade; enqueue a
		// queued command only when the node has no real-time channel.
		if _, connected := s.uplinkClient(node.ID); !connected {
			s.enqueueUplinkCommand(w, node.ID, cmdUpgrade, upgradeCommandPayload{
				Version:     version,
				Repo:        strings.TrimSpace(body.Repo),
				DownloadURL: strings.TrimSpace(body.DownloadURL),
				SHA256:      strings.TrimSpace(body.SHA256),
			})
			return
		}
	}

	// Staging + download may take longer than a normal probe.
	timeout := s.opTimeout()
	if timeout < 3*time.Minute {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	client, live, err := s.liveClientFor(ctx, node)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("连接节点失败：%v", err))
		return
	}
	if !live {
		s.enqueueUplinkCommand(w, node.ID, cmdUpgrade, upgradeCommandPayload{
			Version:     version,
			Repo:        strings.TrimSpace(body.Repo),
			DownloadURL: strings.TrimSpace(body.DownloadURL),
			SHA256:      strings.TrimSpace(body.SHA256),
		})
		return
	}
	defer func() { _ = client.Close() }()

	resp, err := client.UpgradeAgent(ctx, version, strings.TrimSpace(body.Repo), strings.TrimSpace(body.DownloadURL), strings.TrimSpace(body.SHA256))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if resp == nil {
		writeError(w, http.StatusBadGateway, "Agent 返回了空的升级响应")
		return
	}
	statusCode := http.StatusOK
	if !resp.GetOk() {
		statusCode = http.StatusBadGateway
	}
	// Best-effort note on the node; do not flip status offline just for staging.
	if msg := resp.GetMessage(); msg != "" {
		node.LastError = ""
		if !resp.GetOk() {
			node.LastError = "升级失败：" + msg
		}
		_ = s.Store.UpdateNode(node)
	}
	writeJSON(w, statusCode, map[string]any{
		"ok":               resp.GetOk(),
		"message":          resp.GetMessage(),
		"version":          resp.GetVersion(),
		"staged_path":      resp.GetStagedPath(),
		"previous_version": resp.GetPreviousVersion(),
		"node_id":          node.ID,
		"hint":             "binary staged; helper restarts agent shortly — click 探测 to refresh agent_version",
	})
}
