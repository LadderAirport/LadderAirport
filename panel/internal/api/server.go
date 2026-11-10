// Package api implements the Panel HTTP JSON API and admin session auth.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	acmeservice "github.com/ladderairport/panel/internal/acme"
	"github.com/ladderairport/panel/internal/batch"
	"github.com/ladderairport/panel/internal/dnsprovider"
	"github.com/ladderairport/panel/internal/nodeclient"
	"github.com/ladderairport/panel/internal/pki"
	"github.com/ladderairport/panel/internal/secretstore"
	"github.com/ladderairport/panel/internal/store"
	"github.com/ladderairport/panel/internal/subscription"
	"github.com/ladderairport/panel/internal/uplinkhub"
	"github.com/ladderairport/panel/web"
	agentv1 "github.com/ladderairport/proto/gen/go/agent/v1"
)

// NodeLive is the subset of nodeclient used for probe/metrics/logs/upgrade.
// *nodeclient.Client implements this interface.
type NodeLive interface {
	Close() error
	Ping(ctx context.Context) (*agentv1.PingResponse, error)
	GetStatus(ctx context.Context) (*agentv1.GetStatusResponse, error)
	GetMetrics(ctx context.Context) (*agentv1.GetMetricsResponse, error)
	ProbeOutbound(ctx context.Context, outboundTag, targetURL string) (*agentv1.ProbeOutboundResponse, error)
	ListInterfaces(ctx context.Context) (*agentv1.ListInterfacesResponse, error)
	UpgradeAgent(ctx context.Context, version, repo, downloadURL, sha256 string) (*agentv1.UpgradeAgentResponse, error)
	StreamLogs(ctx context.Context, level string, tail int32) (agentv1.AgentControl_StreamLogsClient, error)
}

// NodeFRPS is implemented by live clients for Agents with the frps-v1
// capability. It is separate from NodeLive so existing test doubles and older
// integrations remain source-compatible.
type NodeFRPS interface {
	ApplyFRPServerConfig(ctx context.Context, config *agentv1.FRPServerConfig, hash string) (*agentv1.ApplyFRPServerConfigResponse, error)
	StartFRPServer(ctx context.Context) (*agentv1.StartFRPServerResponse, error)
	StopFRPServer(ctx context.Context) (*agentv1.StopFRPServerResponse, error)
	GetFRPServerStatus(ctx context.Context) (*agentv1.GetFRPServerStatusResponse, error)
}

// NodeFRPSMappings is a separately negotiated extension so existing frps-v1
// clients and test doubles remain compatible.
type NodeFRPSMappings interface {
	GetFRPServerMappings(ctx context.Context) (*agentv1.GetFRPServerMappingsResponse, error)
}

// LiveDialFunc dials a node for live RPCs (probe/metrics/logs).
// Tests may inject a fake implementation.
type LiveDialFunc func(ctx context.Context, n store.Node, token string) (NodeLive, error)

// Server is the Panel HTTP API.
type Server struct {
	Store  *store.Store
	Runner *batch.Runner
	Secret []byte // JWT HMAC secret

	// Aggregator merges external subscription sources into /sub output.
	// Optional; when nil, subscriptions only include local endpoints.
	Aggregator *subscription.Aggregator

	// Dial is used for probe/metrics/logs. When nil, defaults to nodeclient.Dial.
	Dial LiveDialFunc

	// Timeout for probe/metrics/logs dials. Defaults to Runner.Timeout or 10s.
	Timeout time.Duration
	PKI     *pki.Manager
	// Secrets encrypts DNS/ACME credentials and FRPS authentication tokens.
	Secrets      *secretstore.Store
	DNSProviders *dnsprovider.Registry
	ACME         *acmeservice.Service

	// loginAttempts tracks failed logins per client IP for brute-force backoff.
	loginMu       sync.Mutex
	loginAttempts map[string]*loginAttempt

	// cmdMu guards cmdWaiters, the per-node long-poll wakeup channels used by
	// the uplink command queue for near-instant dispatch.
	cmdMu      sync.Mutex
	cmdWaiters map[string]chan struct{}

	// Uplink holds live Agent↔Panel WebSocket connections. When a node has a
	// live socket here, real-time operations (probe/logs/protocol certs/DNS
	// probe/FRPS/…) run over it with full gRPC parity; the HTTP report +
	// config-sync + command-queue paths remain as a degraded fallback.
	Uplink *uplinkhub.Hub

	// admissionMu guards admissionTimer, which debounces node re-pushes
	// after proxy-user / admission changes (see scheduleAdmissionReapply).
	admissionMu    sync.Mutex
	admissionTimer *time.Timer
}

// Handler returns an http.Handler with all routes, auth middleware, and embedded SPA.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	s.mountSPA(mux)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Admin APIs require session; /sub/{token} and login are public.
		if isAPIPath(r.URL.Path) && !isPublicAPI(r) {
			if !s.authenticated(r) {
				writeError(w, http.StatusUnauthorized, "未通过身份认证")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// mountSPA serves the embedded React SPA for non-API routes with index.html fallback.
func (s *Server) mountSPA(mux *http.ServeMux) {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		// Dist layout missing; skip SPA (API still works).
		return
	}
	mux.Handle("/", spaHandler(dist))
}

// spaHandler serves static files from root; unknown paths fall back to index.html.
func spaHandler(root fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "不允许使用该请求方法", http.StatusMethodNotAllowed)
			return
		}

		upath := path.Clean(r.URL.Path)
		if upath == "/" || upath == "." {
			http.ServeFileFS(w, r, root, "index.html")
			return
		}
		// Strip leading slash for fs.FS paths.
		rel := strings.TrimPrefix(upath, "/")

		f, err := root.Open(rel)
		if err != nil {
			// SPA client-side route fallback.
			http.ServeFileFS(w, r, root, "index.html")
			return
		}
		defer f.Close()

		st, err := f.Stat()
		if err != nil || st.IsDir() {
			http.ServeFileFS(w, r, root, "index.html")
			return
		}

		http.ServeFileFS(w, r, root, rel)
	})
}

func isAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/")
}

func isPublicAPI(r *http.Request) bool {
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login" {
		return true
	}
	// Logout is idempotent and must be able to clear an expired or invalid cookie.
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/logout" {
		return true
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/pki/agent-certificates" {
		return true
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/enroll" {
		return true
	}
	if r.Method == http.MethodPost && (r.URL.Path == "/api/v1/agent/report" || r.URL.Path == "/api/v1/agent/config-sync") {
		return true
	}
	if (r.Method == http.MethodHead || r.Method == http.MethodGet) && r.URL.Path == "/api/v1/agent/config-sync" {
		return true
	}
	// Uplink command queue: nodes long-poll for commands and post results,
	// authenticated per-node via Bearer token (not the admin session).
	if r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent/commands" {
		return true
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/agent/commands/") {
		return true
	}
	// Uplink WebSocket: nodes authenticate per-node via Bearer token on the
	// handshake, not the admin session.
	if r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent/uplink" {
		return true
	}
	if r.Method == http.MethodGet && r.URL.Path == "/api/v1/pki/bundle" {
		return true
	}
	// Public subscription pull (token in path).
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/sub/") {
		return true
	}
	return false
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Public subscription endpoint (no admin session).
	mux.HandleFunc("GET /sub/{token}", s.handlePublicSubscription)

	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("POST /api/v1/pki/agent-certificates", s.handleIssueAgentCertificate)
	mux.HandleFunc("POST /api/v1/agent/enroll", s.handleAgentEnroll)
	mux.HandleFunc("GET /api/v1/pki/bundle", s.handlePKIBundle)
	mux.HandleFunc("POST /api/v1/agent/report", s.handleAgentReport)
	mux.HandleFunc("HEAD /api/v1/agent/config-sync", s.handleAgentConfigHead)
	mux.HandleFunc("GET /api/v1/agent/config-sync", s.handleAgentConfigHead)
	mux.HandleFunc("POST /api/v1/agent/config-sync", s.handleAgentConfigSync)
	mux.HandleFunc("GET /api/v1/agent/commands", s.handleAgentPollCommands)
	mux.HandleFunc("POST /api/v1/agent/commands/{id}/result", s.handleAgentCommandResult)
	mux.HandleFunc("GET /api/v1/agent/uplink", s.handleAgentUplinkWS)

	mux.HandleFunc("GET /api/v1/templates", s.handleListTemplates)

	mux.HandleFunc("GET /api/v1/inbounds", s.handleListInbounds)
	mux.HandleFunc("POST /api/v1/inbounds", s.handleCreateInbound)
	mux.HandleFunc("PUT /api/v1/inbounds/{id}", s.handleUpdateInbound)
	mux.HandleFunc("DELETE /api/v1/inbounds/{id}", s.handleDeleteInbound)
	mux.HandleFunc("GET /api/v1/inbound-admissions", s.handleListInboundAdmissions)
	mux.HandleFunc("PUT /api/v1/inbounds/{id}/admission", s.handlePutInboundAdmission)
	mux.HandleFunc("GET /api/v1/proxy-users", s.handleListProxyUsers)
	mux.HandleFunc("POST /api/v1/proxy-users", s.handleCreateProxyUser)
	mux.HandleFunc("PUT /api/v1/proxy-users/{id}", s.handleUpdateProxyUser)
	mux.HandleFunc("DELETE /api/v1/proxy-users/{id}", s.handleDeleteProxyUser)
	mux.HandleFunc("POST /api/v1/proxy-users/{id}/reset-credentials", s.handleResetProxyUserCredentials)

	mux.HandleFunc("GET /api/v1/fleet/overview", s.handleFleetOverview)
	mux.HandleFunc("POST /api/v1/fleet/refresh", s.handleFleetRefresh)

	mux.HandleFunc("GET /api/v1/nodes", s.handleListNodes)
	mux.HandleFunc("POST /api/v1/nodes", s.handleCreateNode)
	mux.HandleFunc("POST /api/v1/nodes/bootstrap", s.handleBootstrapNode)
	mux.HandleFunc("PUT /api/v1/nodes/{id}", s.handleUpdateNode)
	mux.HandleFunc("DELETE /api/v1/nodes/{id}", s.handleDeleteNode)
	mux.HandleFunc("POST /api/v1/nodes/{id}/install-command", s.handleNodeInstallCommand)
	mux.HandleFunc("POST /api/v1/nodes/{id}/probe", s.handleProbeNode)
	mux.HandleFunc("GET /api/v1/nodes/{id}/inbounds", s.handleListNodeInbounds)
	mux.HandleFunc("PUT /api/v1/nodes/{id}/inbounds", s.handleSetNodeInbounds)
	mux.HandleFunc("POST /api/v1/nodes/{id}/apply", s.handleNodeApply)
	mux.HandleFunc("POST /api/v1/nodes/{id}/config/preview", s.handleNodePreview)
	mux.HandleFunc("POST /api/v1/nodes/{id}/start", s.handleNodeStart)
	mux.HandleFunc("POST /api/v1/nodes/{id}/stop", s.handleNodeStop)
	mux.HandleFunc("GET /api/v1/nodes/{id}/metrics", s.handleNodeMetrics)
	mux.HandleFunc("GET /api/v1/nodes/{id}/sysmetrics", s.handleNodeSysMetrics)
	mux.HandleFunc("GET /api/v1/nodes/{id}/bbr", s.handleGetNodeBBR)
	mux.HandleFunc("POST /api/v1/nodes/{id}/bbr", s.handleSetNodeBBR)
	mux.HandleFunc("GET /api/v1/nodes/{id}/interfaces", s.handleNodeInterfaces)
	mux.HandleFunc("POST /api/v1/nodes/{id}/upgrade", s.handleNodeUpgrade)
	mux.HandleFunc("GET /api/v1/nodes/{id}/logs", s.handleNodeLogs)
	mux.HandleFunc("GET /api/v1/nodes/{id}/frps", s.handleGetNodeFRPS)
	mux.HandleFunc("PUT /api/v1/nodes/{id}/frps", s.handlePutNodeFRPS)
	mux.HandleFunc("GET /api/v1/nodes/{id}/frps/status", s.handleGetNodeFRPSStatus)
	mux.HandleFunc("GET /api/v1/nodes/{id}/frps/mappings", s.handleGetNodeFRPSMappings)
	mux.HandleFunc("POST /api/v1/nodes/{id}/frps/start", s.handleStartNodeFRPS)
	mux.HandleFunc("POST /api/v1/nodes/{id}/frps/stop", s.handleStopNodeFRPS)
	mux.HandleFunc("POST /api/v1/nodes/{id}/frps/token/reveal", s.handleRevealNodeFRPSToken)

	// Admin-facing read of a queued uplink command's status/result.
	mux.HandleFunc("GET /api/v1/commands/{id}", s.handleGetCommand)

	mux.HandleFunc("POST /api/v1/batch/apply", s.handleBatchApply)
	mux.HandleFunc("POST /api/v1/batch/start", s.handleBatchStart)
	mux.HandleFunc("POST /api/v1/batch/stop", s.handleBatchStop)

	mux.HandleFunc("GET /api/v1/tasks", s.handleListTasks)
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.handleGetTask)

	mux.HandleFunc("GET /api/v1/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/v1/settings", s.handlePutSettings)
	mux.HandleFunc("GET /api/v1/meta", s.handleGetMeta)
	mux.HandleFunc("GET /api/v1/pki/status", s.handlePKIStatus)
	mux.HandleFunc("GET /api/v1/pki/certificates", s.handleListPKICertificates)
	mux.HandleFunc("POST /api/v1/pki/certificates/{serial}/revoke", s.handleRevokePKICertificate)
	mux.HandleFunc("GET /api/v1/pki/audit-logs", s.handleListPKIAuditLogs)

	mux.HandleFunc("GET /api/v1/dns/providers", s.handleListDNSProviders)
	mux.HandleFunc("GET /api/v1/dns/accounts", s.handleListDNSAccounts)
	mux.HandleFunc("POST /api/v1/dns/accounts", s.handleCreateDNSAccount)
	mux.HandleFunc("PUT /api/v1/dns/accounts/{id}", s.handleUpdateDNSAccount)
	mux.HandleFunc("DELETE /api/v1/dns/accounts/{id}", s.handleDeleteDNSAccount)
	mux.HandleFunc("POST /api/v1/dns/accounts/{id}/test", s.handleTestDNSAccount)
	mux.HandleFunc("GET /api/v1/managed-domains", s.handleListManagedDomains)
	mux.HandleFunc("POST /api/v1/managed-domains", s.handleCreateManagedDomain)
	mux.HandleFunc("PUT /api/v1/managed-domains/{id}", s.handleUpdateManagedDomain)
	mux.HandleFunc("DELETE /api/v1/managed-domains/{id}", s.handleDeleteManagedDomain)
	mux.HandleFunc("POST /api/v1/managed-domains/{id}/reconcile", s.handleReconcileManagedDomain)
	mux.HandleFunc("GET /api/v1/acme/accounts", s.handleListACMEAccounts)
	mux.HandleFunc("POST /api/v1/acme/accounts", s.handleCreateACMEAccount)
	mux.HandleFunc("PUT /api/v1/acme/accounts/{id}", s.handleUpdateACMEAccount)
	mux.HandleFunc("DELETE /api/v1/acme/accounts/{id}", s.handleDeleteACMEAccount)
	mux.HandleFunc("POST /api/v1/acme/accounts/{id}/register", s.handleRegisterACMEAccount)
	mux.HandleFunc("GET /api/v1/protocol-certificates", s.handleListProtocolCertificates)
	mux.HandleFunc("POST /api/v1/protocol-certificates", s.handleCreateProtocolCertificate)
	mux.HandleFunc("DELETE /api/v1/protocol-certificates/{id}", s.handleDeleteProtocolCertificate)
	mux.HandleFunc("POST /api/v1/protocol-certificates/{id}/issue", s.handleIssueProtocolCertificate)
	mux.HandleFunc("GET /api/v1/nodes/{id}/inbounds/{inbound_id}/tls", s.handleGetNodeInboundTLS)
	mux.HandleFunc("PUT /api/v1/nodes/{id}/inbounds/{inbound_id}/tls", s.handlePutNodeInboundTLS)
	mux.HandleFunc("GET /api/v1/automation/jobs", s.handleListAutomationJobs)
	mux.HandleFunc("GET /api/v1/automation/audit-logs", s.handleListAutomationAudits)

	mux.HandleFunc("GET /api/v1/subscriptions", s.handleListSubscriptions)
	mux.HandleFunc("POST /api/v1/subscriptions", s.handleCreateSubscription)
	mux.HandleFunc("PUT /api/v1/subscriptions/{id}", s.handleUpdateSubscription)
	mux.HandleFunc("DELETE /api/v1/subscriptions/{id}", s.handleDeleteSubscription)
	mux.HandleFunc("GET /api/v1/subscriptions/{id}/preview", s.handlePreviewSubscription)
	mux.HandleFunc("POST /api/v1/subscriptions/{id}/token/rotate", s.handleRotateSubscriptionToken)
	mux.HandleFunc("POST /api/v1/subscriptions/{id}/disable", s.handleDisableSubscription)
	mux.HandleFunc("POST /api/v1/subscriptions/{id}/enable", s.handleEnableSubscription)

	mux.HandleFunc("GET /api/v1/route-plans", s.handleListRoutePlans)
	mux.HandleFunc("POST /api/v1/route-plans", s.handleCreateRoutePlan)
	mux.HandleFunc("GET /api/v1/route-plans/{id}", s.handleGetRoutePlan)
	mux.HandleFunc("PUT /api/v1/route-plans/{id}", s.handleUpdateRoutePlan)
	mux.HandleFunc("DELETE /api/v1/route-plans/{id}", s.handleDeleteRoutePlan)

	mux.HandleFunc("GET /api/v1/proxy-chains", s.handleListProxyChains)
	mux.HandleFunc("POST /api/v1/proxy-chains", s.handleCreateProxyChain)
	mux.HandleFunc("GET /api/v1/proxy-chains/{id}", s.handleGetProxyChain)
	mux.HandleFunc("PUT /api/v1/proxy-chains/{id}", s.handleUpdateProxyChain)
	mux.HandleFunc("DELETE /api/v1/proxy-chains/{id}", s.handleDeleteProxyChain)
	mux.HandleFunc("POST /api/v1/proxy-chains/{id}/enable", s.handleEnableProxyChain)
	mux.HandleFunc("POST /api/v1/proxy-chains/{id}/disable", s.handleDisableProxyChain)

	mux.HandleFunc("GET /api/v1/external-sources", s.handleListExternalSources)
	mux.HandleFunc("POST /api/v1/external-sources", s.handleCreateExternalSource)
	mux.HandleFunc("PUT /api/v1/external-sources/{id}", s.handleUpdateExternalSource)
	mux.HandleFunc("DELETE /api/v1/external-sources/{id}", s.handleDeleteExternalSource)
	mux.HandleFunc("POST /api/v1/external-sources/{id}/refresh", s.handleRefreshExternalSource)
	mux.HandleFunc("GET /api/v1/external-sources/{id}/preview", s.handlePreviewExternalSource)
}

func hasCapability(caps []string, want string) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
}

const errUplinkNoLiveRPC = "uplink 节点未建立实时通道，无法即时操作，请改用 push 或等待下次配置拉取"

func (s *Server) liveDial(ctx context.Context, n store.Node, token string) (NodeLive, error) {
	if s.Dial != nil {
		return s.Dial(ctx, n, token)
	}
	return s.defaultLiveDial(ctx, n, token)
}

// uplinkClient returns a live WS-backed AgentControl client for a connected
// uplink node, if one is currently registered in the Hub. The returned client
// mirrors *nodeclient.Client so live handlers use it exactly like a gRPC dial.
func (s *Server) uplinkClient(nodeID string) (*uplinkhub.Client, bool) {
	if s.Uplink == nil {
		return nil, false
	}
	return s.Uplink.Client(nodeID)
}

// uplinkWSConnected reports whether an uplink node currently has a live WS
// socket, as an optional bool suitable for the store.Node display field. It
// returns nil for non-uplink nodes (the field is only meaningful for uplink).
func (s *Server) uplinkWSConnected(node store.Node) *bool {
	if node.ControlMode != store.ControlModeUplink {
		return nil
	}
	connected := false
	if s.Uplink != nil {
		connected = s.Uplink.Connected(node.ID)
	}
	return &connected
}

// liveClientFor resolves a live AgentControl client for node so a handler can
// run the exact same RPC path for push and uplink nodes.
//
//   - push node: dials the gRPC control port.
//   - uplink node with a live WS socket: returns the WS-backed client, achieving
//     full parity with the gRPC control plane.
//   - uplink node with no live socket: returns ok=false, err=nil so the caller
//     runs its degraded HTTP fallback (command-queue enqueue / cached report).
//   - dial/connect failure: returns ok=false, err!=nil (suitable for 502).
//
// When ok=true the caller owns the client and must Close it; the WS client's
// Close is a no-op since the Hub owns the socket.
func (s *Server) liveClientFor(ctx context.Context, node *store.Node) (NodeLive, bool, error) {
	if node.ControlMode == store.ControlModeUplink {
		if client, connected := s.uplinkClient(node.ID); connected {
			return client, true, nil
		}
		return nil, false, nil
	}
	client, err := s.liveDial(ctx, *node, s.nodeToken(node))
	if err != nil {
		return nil, false, err
	}
	return client, true, nil
}

func (s *Server) defaultLiveDial(ctx context.Context, n store.Node, token string) (NodeLive, error) {
	if s.PKI == nil {
		return nil, fmt.Errorf("管理 PKI 不可用")
	}
	if n.PKICertSerial == "" || n.PKICABundlePEM == "" {
		return nil, fmt.Errorf("节点 %s 尚未完成 Panel PKI 注册", n.ID)
	}
	timeout := s.Timeout
	if timeout <= 0 && s.Runner != nil {
		timeout = s.Runner.OperationTimeout()
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	clientCert := s.PKI.ClientCertificate()
	cfg := nodeclient.DialConfig{
		Address:           net.JoinHostPort(n.Address, fmt.Sprintf("%d", n.GRPCPort)),
		Token:             token,
		Timeout:           timeout,
		CACertPEM:         []byte(n.PKICABundlePEM),
		ClientCertificate: &clientCert,
		ExpectedPeerURI:   pki.AgentURI(n.ID),
		ExpectedSerial:    n.PKICertSerial,
	}
	return nodeclient.Dial(ctx, cfg)
}

func (s *Server) nodeToken(n *store.Node) string {
	if n.Token != "" {
		return n.Token
	}
	if s.Runner != nil && s.Runner.DefaultToken != nil {
		return s.Runner.DefaultToken()
	}
	return ""
}

func (s *Server) opTimeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	if s.Runner != nil {
		return s.Runner.OperationTimeout()
	}
	return 10 * time.Second
}

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": localizeDatabaseError(msg)})
}

func localizeDatabaseError(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "foreign key constraint failed"):
		return errorPrefix(msg) + "仍有其他数据引用该对象，请先解除关联"
	case strings.Contains(lower, "unique constraint failed"):
		return errorPrefix(msg) + "数据已存在，不能重复"
	case strings.Contains(lower, "not null constraint failed"):
		return errorPrefix(msg) + "缺少必填数据"
	case strings.Contains(lower, "check constraint failed"):
		return errorPrefix(msg) + "数据未通过有效性校验"
	case strings.Contains(lower, "constraint failed"):
		return errorPrefix(msg) + "数据约束校验失败"
	default:
		return msg
	}
}

func errorPrefix(msg string) string {
	lower := strings.ToLower(msg)
	idx := strings.Index(lower, "constraint failed")
	if idx < 0 {
		return ""
	}
	prefix := strings.TrimRight(strings.TrimSpace(msg[:idx]), ":：")
	if prefix == "" {
		return ""
	}
	return prefix + "："
}

// maxJSONBodyBytes caps admin API request bodies at 1 MiB.
const maxJSONBodyBytes = 1 << 20

func decodeJSON(w http.ResponseWriter, r *http.Request, dest any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
	dec.UseNumber()
	return dec.Decode(dest)
}

// writeDecodeError reports a decodeJSON failure: 413 when the body exceeds
// maxJSONBodyBytes, otherwise 400.
func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "请求体超过大小限制")
		return
	}
	writeError(w, http.StatusBadRequest, "JSON 请求体无效")
}

func pathID(r *http.Request) string {
	return r.PathValue("id")
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not found") || strings.Contains(msg, "不存在") || strings.Contains(msg, "未找到")
}
