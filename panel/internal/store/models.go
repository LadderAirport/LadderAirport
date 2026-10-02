package store

// PortMapping rewrites an agent listen port to the client-facing NAT/public port.
// Used only when rendering subscriptions; agent configs still use the inbound listen port.
type PortMapping struct {
	// ListenPort is the port the agent/inbound actually binds (params.port).
	ListenPort int `json:"listen_port"`
	// PublicPort is the external NAT-mapped port clients dial.
	// 0 or equal to ListenPort means no rewrite.
	PublicPort int `json:"public_port"`
}

const (
	ControlModePush   = "push"
	ControlModeUplink = "uplink"

	DesiredRuntimeRunning = "running"
	DesiredRuntimeStopped = "stopped"
)

type Node struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Address  string   `json:"address"`   // control dial host (Panel → Agent)
	GRPCPort int      `json:"grpc_port"` // control dial port (external mapped port if NAT)
	Token    string   `json:"token,omitempty"`
	Labels   []string `json:"labels"`
	// ControlMode selects how the node syncs: push is Panel-dialed gRPC;
	// uplink is Agent-initiated HTTP report + config pull.
	ControlMode string `json:"control_mode"`
	// DesiredRuntime is the operator-intended core state for uplink pull (running|stopped).
	DesiredRuntime string `json:"desired_runtime,omitempty"`
	// UplinkLastSeenUnix is the newest accepted HTTP report timestamp.
	UplinkLastSeenUnix int64  `json:"uplink_last_seen_unix,omitempty"`
	PKICABundlePEM     string `json:"-"`
	// PKICertSerial binds Panel dialing to the currently active Panel-issued
	// Agent server certificate.
	PKICertSerial string `json:"pki_cert_serial,omitempty"`
	PKINotAfter   int64  `json:"pki_not_after_unix,omitempty"`
	// PublicAddress is the client-facing host for subscriptions (Clash/sing-box server).
	// Empty means fall back to Address. Host only — client ports come from inbound params
	// or PortMappings when listen/public ports differ.
	PublicAddress string `json:"public_address"`
	// PortMappings maps agent listen ports → external NAT ports for subscription clients.
	// Empty means subscribe with the inbound listen port as-is.
	PortMappings []PortMapping `json:"port_mappings"`
	// EgressInterface is the host NIC name for sing-box direct bind_interface.
	// Empty means OS default routing.
	EgressInterface string `json:"egress_interface"`
	// DDNSEnabled lets the DNS reconcile worker probe this node's public
	// address (managed domains with address_source=agent_public). New nodes
	// default to true; false pauses probing while keeping the schedule.
	DDNSEnabled  bool   `json:"ddns_enabled"`
	Status       string `json:"status"` // online | unreachable | unauthorized | unknown
	LastSeenUnix int64  `json:"last_seen_unix"`
	ConfigHash   string `json:"config_hash"`
	// Live monitoring cache (updated by fleet refresh / probe).
	RuntimeState   string   `json:"runtime_state"` // running | stopped | error | ""
	AgentVersion   string   `json:"agent_version"`
	SingboxVersion string   `json:"singbox_version"`
	Capabilities   []string `json:"capabilities"`
	Connections    int64    `json:"connections"`
	UplinkBytes    int64    `json:"uplink_bytes"`
	DownlinkBytes  int64    `json:"downlink_bytes"`
	CPUPercent     float64  `json:"cpu_percent"`
	MemoryRSSBytes int64    `json:"memory_rss_bytes"`
	MetricsAtUnix  int64    `json:"metrics_at_unix"`
	LastError      string   `json:"last_error,omitempty"`
	InboundCount   int      `json:"inbound_count,omitempty"` // filled by overview, not persisted
	// UplinkWSConnected reports whether an uplink node currently holds a live
	// WebSocket control channel. It is computed by the API from the uplink Hub
	// at read time (not persisted) and only set for uplink nodes; nil means
	// "not applicable" (push node) or "not computed".
	UplinkWSConnected *bool `json:"uplink_ws_connected,omitempty"`
	CreatedAtUnix     int64 `json:"created_at_unix"`
	UpdatedAtUnix     int64 `json:"updated_at_unix"`
}

// FRPServerPortRange limits the remote proxy ports that FRPS may allocate.
type FRPServerPortRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// FRPServerConfig stores a node's desired FRPS configuration and the latest
// runtime state observed from the Agent. The authentication token is always
// encrypted at rest and never serialized by the HTTP API.
type FRPServerConfig struct {
	NodeID              string               `json:"node_id"`
	Enabled             bool                 `json:"enabled"`
	BindAddr            string               `json:"bind_addr"`
	BindPort            int                  `json:"bind_port"`
	ProxyBindAddr       string               `json:"proxy_bind_addr"`
	AllowPorts          []FRPServerPortRange `json:"allow_ports"`
	AuthTokenCiphertext string               `json:"-"`
	HasAuthToken        bool                 `json:"has_auth_token"`
	TLSForce            bool                 `json:"tls_force"`
	MaxPortsPerClient   int64                `json:"max_ports_per_client"`
	// ManagedDomainID optionally binds a managed domain as the frpc-facing
	// server address. Display only — the agent-side FRPS bind config is unchanged.
	ManagedDomainID string `json:"managed_domain_id,omitempty"`
	DesiredHash     string `json:"desired_hash"`
	AppliedHash     string `json:"applied_hash"`
	RuntimeState    string `json:"runtime_state"`
	FRPSVersion     string `json:"frps_version"`
	LastError       string `json:"last_error,omitempty"`
	StartedAtUnix   int64  `json:"started_at_unix"`
	CreatedAtUnix   int64  `json:"created_at_unix"`
	UpdatedAtUnix   int64  `json:"updated_at_unix"`
}

// NodeOperatorUpdate contains only fields controlled by an operator. Runtime
// status, metrics, versions and config hashes are deliberately excluded.
type NodeOperatorUpdate struct {
	Name            *string        `json:"name"`
	Address         *string        `json:"address"`
	GRPCPort        *int           `json:"grpc_port"`
	Token           *string        `json:"token"`
	Labels          *[]string      `json:"labels"`
	PublicAddress   *string        `json:"public_address"`
	PortMappings    *[]PortMapping `json:"port_mappings"`
	EgressInterface *string        `json:"egress_interface"`
	DDNSEnabled     *bool          `json:"ddns_enabled"`
	ControlMode     *string        `json:"control_mode"`
	DesiredRuntime  *string        `json:"desired_runtime"`
}

// NodeReport is a partial live update from an Agent HTTP report.
type NodeReport struct {
	CollectedAtUnix int64
	Status          string
	RuntimeState    string
	ConfigHash      string
	LastError       string
	AgentVersion    string
	SingboxVersion  string
	Capabilities    []string
	HasCapabilities bool
	HasMetrics      bool
	Connections     int64
	UplinkBytes     int64
	DownlinkBytes   int64
	CPUPercent      float64
	MemoryRSSBytes  int64
}

// NormalizePortMappings drops invalid/identity rows and keeps the last mapping per listen_port.
func NormalizePortMappings(in []PortMapping) []PortMapping {
	if len(in) == 0 {
		return []PortMapping{}
	}
	// Preserve first-seen order; later rows with the same listen_port overwrite.
	order := make([]int, 0, len(in))
	byListen := map[int]PortMapping{}
	for _, m := range in {
		if m.ListenPort < 1 || m.ListenPort > 65535 {
			continue
		}
		if m.PublicPort < 1 || m.PublicPort > 65535 || m.PublicPort == m.ListenPort {
			// Invalid or identity: drop any previous mapping for this listen port.
			if _, exists := byListen[m.ListenPort]; exists {
				delete(byListen, m.ListenPort)
				for i, p := range order {
					if p == m.ListenPort {
						order = append(order[:i], order[i+1:]...)
						break
					}
				}
			}
			continue
		}
		if _, exists := byListen[m.ListenPort]; !exists {
			order = append(order, m.ListenPort)
		}
		byListen[m.ListenPort] = PortMapping{ListenPort: m.ListenPort, PublicPort: m.PublicPort}
	}
	out := make([]PortMapping, 0, len(order))
	for _, p := range order {
		if m, ok := byListen[p]; ok {
			out = append(out, m)
		}
	}
	return out
}

// MapPublicPort returns the client-facing port for an agent listen port.
// Falls back to listenPort when no mapping is configured.
// Deprecated for new UI: prefer per-inbound PublicPort on NodeInboundAttachment.
func MapPublicPort(mappings []PortMapping, listenPort int) int {
	for _, m := range mappings {
		if m.ListenPort == listenPort && m.PublicPort >= 1 && m.PublicPort <= 65535 {
			return m.PublicPort
		}
	}
	return listenPort
}

// NodeInboundAttachment is an inbound linked to a node, with per-node exposure settings.
// PublicAddress/PublicPort only affect subscriptions for directly exposed inbounds.
type NodeInboundAttachment struct {
	InboundConfig
	// PublicAddress overrides node.public_address / address for this inbound in subscriptions.
	// Empty falls back to node-level client host.
	PublicAddress string `json:"public_address"`
	// PublicPort is the external NAT-mapped port clients dial.
	// 0 means use inbound listen port (params.port), then node port_mappings if any.
	PublicPort int    `json:"public_port"`
	FRPEnabled bool   `json:"frp_enabled"`
	FRPCConfig string `json:"frpc_config"`
}

// EffectiveInbound overlays node-specific FRP settings without changing the reusable template.
func (a NodeInboundAttachment) EffectiveInbound() InboundConfig {
	in := a.InboundConfig
	in.Params = make(map[string]any, len(a.Params)+2)
	for key, value := range a.Params {
		in.Params[key] = value
	}
	in.Params["frp_enabled"] = a.FRPEnabled
	in.Params["frpc_config"] = a.FRPCConfig
	return in
}

// NodeInboundBinding is the write payload for attaching an inbound with NAT overrides.
type NodeInboundBinding struct {
	InboundID     string `json:"inbound_id"`
	PublicAddress string `json:"public_address"`
	PublicPort    int    `json:"public_port"`
	FRPEnabled    bool   `json:"frp_enabled"`
	FRPCConfig    string `json:"frpc_config"`
}

type InboundConfig struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Protocol      string         `json:"protocol"` // shadowsocks|trojan|vless|hysteria2|tuic|anytls|vmess|http|socks5|auto
	Params        map[string]any `json:"params"`
	Enabled       bool           `json:"enabled"`
	CreatedAtUnix int64          `json:"created_at_unix"`
	UpdatedAtUnix int64          `json:"updated_at_unix"`
}

type Task struct {
	ID            string           `json:"id"`
	Type          string           `json:"type"`   // apply|start|stop
	Status        string           `json:"status"` // pending|running|success|partial|failed
	NodeIDs       []string         `json:"node_ids"`
	Results       []TaskNodeResult `json:"results"`
	CreatedAtUnix int64            `json:"created_at_unix"`
	UpdatedAtUnix int64            `json:"updated_at_unix"`
}

type TaskNodeResult struct {
	NodeID  string `json:"node_id"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Phase   string `json:"phase,omitempty"`
}

// AgentCommand is a single queued immediate operation for an uplink node.
// Panel enqueues it, the node long-polls, executes locally, and posts a result.
type AgentCommand struct {
	ID               string `json:"id"`
	NodeID           string `json:"node_id"`
	Type             string `json:"type"`    // probe|interfaces|upgrade|sysmetrics|bbr-status|bbr-set|frps-mappings|frps-start|frps-stop
	Payload          string `json:"payload"` // opaque JSON arguments for the command type
	Status           string `json:"status"`  // pending|leased|succeeded|failed|expired
	Attempt          int    `json:"attempt"`
	LeaseExpiresUnix int64  `json:"lease_expires_unix"`
	Result           string `json:"result,omitempty"` // JSON result payload on success
	Error            string `json:"error,omitempty"`
	CreatedAtUnix    int64  `json:"created_at_unix"`
	CompletedAtUnix  int64  `json:"completed_at_unix,omitempty"`
	ExpiresAtUnix    int64  `json:"expires_at_unix,omitempty"`
}

// AgentCommand status values.
const (
	AgentCommandPending   = "pending"
	AgentCommandLeased    = "leased"
	AgentCommandSucceeded = "succeeded"
	AgentCommandFailed    = "failed"
	AgentCommandExpired   = "expired"
)

type Settings struct {
	AdminPasswordHash string `json:"-"`
	DefaultAgentToken string `json:"default_agent_token"`
	GRPCTimeoutSec    int    `json:"grpc_timeout_sec"`
	MaxConcurrency    int    `json:"max_concurrency"`
	ListenAddr        string `json:"listen_addr"`
	// PublicBaseURL is used to render full subscription links (e.g. https://panel.example.com).
	PublicBaseURL             string `json:"public_base_url"`
	ChainProbeURL             string `json:"chain_probe_url"`
	ChainProbeIntervalSec     int    `json:"chain_probe_interval_sec"`
	ChainProbeTimeoutSec      int    `json:"chain_probe_timeout_sec"`
	ChainSubscriptionMigrated bool   `json:"-"`
	// SessionVersion revokes issued session tokens when bumped (logout /
	// password change). Never serialized to API clients.
	SessionVersion int64 `json:"-"`
	// TrustedProxyCIDRs is a comma-separated list of proxy CIDRs whose
	// X-Forwarded-Proto header is honored. Empty trusts no proxy.
	TrustedProxyCIDRs string `json:"trusted_proxy_cidrs"`
}

// Subscription is a client-facing share link.
type Subscription struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Format             string   `json:"format,omitempty"` // legacy format (optional)
	Token              string   `json:"token"`            // URL secret for public /sub/{token}
	InboundIDs         []string `json:"inbound_ids"`      // selected IDs when IncludeAllInbounds is false
	IncludeAllInbounds bool     `json:"include_all_inbounds"`
	IncludeStandalone  bool     `json:"include_standalone"`
	ChainIDs           []string `json:"chain_ids"`
	IncludeAllChains   bool     `json:"include_all_chains"`
	// RoutePlanID binds a subscription-scope route plan whose rules are
	// injected into rendered client configs. Empty means no plan.
	RoutePlanID string `json:"route_plan_id"`
	Enabled     bool   `json:"enabled"`
	// Disabled is the kill switch for the public link: /sub/{token} answers
	// 404 while disabled, as if the subscription did not exist.
	Disabled      bool  `json:"disabled"`
	CreatedAtUnix int64 `json:"created_at_unix"`
	UpdatedAtUnix int64 `json:"updated_at_unix"`
}

// RoutePlan scopes an ordered rule set: "global" plans are pushed into every
// node's sing-box route rules; "subscription" plans are injected into the
// rendered client config of the bound subscription.
type RoutePlan struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Scope          string          `json:"scope"` // global | subscription
	SubscriptionID string          `json:"subscription_id"`
	Enabled        bool            `json:"enabled"`
	SortOrder      int             `json:"sort_order"`
	Rules          []RoutePlanRule `json:"rules"`
	CreatedAtUnix  int64           `json:"created_at_unix"`
	UpdatedAtUnix  int64           `json:"updated_at_unix"`
}

// RoutePlanRule is one ordered match rule of a route plan. Position is
// rewritten sequentially (0..n-1) on every replace.
type RoutePlanRule struct {
	PlanID     string `json:"-"`
	Position   int    `json:"position"`
	MatchType  string `json:"match_type"` // domain|domain_suffix|domain_keyword|ip_cidr|process_name
	MatchValue string `json:"match_value"`
	Action     string `json:"action"` // proxy|direct|block
	// TargetChainID is the proxy chain for action=proxy; empty otherwise.
	TargetChainID string `json:"target_chain_id"`
	Enabled       bool   `json:"enabled"`
}

// ProxyChain is an ordered server-side proxy path. Hops are chain-owned and
// therefore do not appear in node_inbounds unless separately attached.
type ProxyChain struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Enabled          bool            `json:"enabled"`
	State            string          `json:"state"` // disabled|deploying|healthy|degraded
	Hops             []ProxyChainHop `json:"hops"`
	LastDeployUnix   int64           `json:"last_deploy_unix"`
	LastDeployError  string          `json:"last_deploy_error,omitempty"`
	LastProbeUnix    int64           `json:"last_probe_unix"`
	LastProbeDelayMS int             `json:"last_probe_delay_ms"`
	LastProbeError   string          `json:"last_probe_error,omitempty"`
	FailedHopIndex   int             `json:"failed_hop_index"` // -1 when unknown/healthy
	CreatedAtUnix    int64           `json:"created_at_unix"`
	UpdatedAtUnix    int64           `json:"updated_at_unix"`
}

// ProxyChainHop describes how a consumer (the client for hop zero, otherwise
// the preceding Agent) reaches this hop.
type ProxyChainHop struct {
	ChainID       string `json:"chain_id,omitempty"`
	Position      int    `json:"position"`
	NodeID        string `json:"node_id"`
	InboundID     string `json:"inbound_id"`
	DialAddress   string `json:"dial_address"`
	DialPort      int    `json:"dial_port"`
	TLSSkipVerify bool   `json:"tls_skip_verify"`
}

// ExternalSource is a remote subscription URL that can be attached to panel subscriptions.
// CachedBody holds the last successful raw fetch (not exported in list JSON).
type ExternalSource struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	Headers            map[string]string `json:"headers,omitempty"`
	Enabled            bool              `json:"enabled"`
	RefreshIntervalSec int               `json:"refresh_interval_sec"` // 0 = default (24h)
	LastFetchUnix      int64             `json:"last_fetch_unix"`
	LastSuccessUnix    int64             `json:"last_success_unix"`
	LastError          string            `json:"last_error,omitempty"`
	ContentType        string            `json:"content_type,omitempty"` // clash_yaml | share_links | singbox_json
	CachedProxyCount   int               `json:"cached_proxy_count"`
	CachedBody         string            `json:"-"`
	CreatedAtUnix      int64             `json:"created_at_unix"`
	UpdatedAtUnix      int64             `json:"updated_at_unix"`
}

type ConfigSnapshot struct {
	ID            string `json:"id"`
	NodeID        string `json:"node_id"`
	ConfigJSON    string `json:"config_json"`
	ConfigHash    string `json:"config_hash"`
	TaskID        string `json:"task_id,omitempty"`
	CreatedAtUnix int64  `json:"created_at_unix"`
}

// PKICertificate is an issuance record for a management-plane certificate.
// Private keys are never stored by Panel for Agent certificates.
type PKICertificate struct {
	Serial        string `json:"serial"`
	NodeID        string `json:"node_id,omitempty"`
	Profile       string `json:"profile"` // agent-server | panel-client
	Subject       string `json:"subject"`
	URISAN        string `json:"uri_san"`
	DNSSANs       string `json:"dns_sans,omitempty"`
	IPSANs        string `json:"ip_sans,omitempty"`
	NotBeforeUnix int64  `json:"not_before_unix"`
	NotAfterUnix  int64  `json:"not_after_unix"`
	Status        string `json:"status"` // active | replaced | revoked | expired
	RevokedAtUnix int64  `json:"revoked_at_unix,omitempty"`
	RevokeReason  string `json:"revoke_reason,omitempty"`
	CertPEM       string `json:"cert_pem,omitempty"`
	CreatedAtUnix int64  `json:"created_at_unix"`
}

type PKIAuditLog struct {
	ID            string `json:"id"`
	Action        string `json:"action"`
	NodeID        string `json:"node_id,omitempty"`
	Serial        string `json:"serial,omitempty"`
	Actor         string `json:"actor"`
	Detail        string `json:"detail,omitempty"`
	CreatedAtUnix int64  `json:"created_at_unix"`
}

// DNSAccount is a provider credential container. CredentialsCiphertext is an
// encrypted envelope and is never serialized to API clients.
type DNSAccount struct {
	ID                    string         `json:"id"`
	Name                  string         `json:"name"`
	Provider              string         `json:"provider"`
	Zone                  string         `json:"zone"`
	CredentialsCiphertext string         `json:"-"`
	HasCredentials        bool           `json:"has_credentials"`
	Settings              map[string]any `json:"settings"`
	Enabled               bool           `json:"enabled"`
	LastTestUnix          int64          `json:"last_test_unix"`
	LastTestError         string         `json:"last_test_error,omitempty"`
	CreatedAtUnix         int64          `json:"created_at_unix"`
	UpdatedAtUnix         int64          `json:"updated_at_unix"`
}

// ManagedDomain describes the desired DNS endpoint for one node.
type ManagedDomain struct {
	ID                    string   `json:"id"`
	NodeID                string   `json:"node_id"`
	DNSAccountID          string   `json:"dns_account_id"`
	Zone                  string   `json:"zone"`
	FQDN                  string   `json:"fqdn"`
	RecordMode            string   `json:"record_mode"`    // a | aaaa | dual | cname
	AddressSource         string   `json:"address_source"` // manual | node_address | agent_public
	ManualIPv4            string   `json:"manual_ipv4,omitempty"`
	ManualIPv6            string   `json:"manual_ipv6,omitempty"`
	ManualCNAME           string   `json:"manual_cname,omitempty"`
	TTL                   int      `json:"ttl"`
	Enabled               bool     `json:"enabled"`
	State                 string   `json:"state"`
	DesiredIPv4           string   `json:"desired_ipv4,omitempty"`
	DesiredIPv6           string   `json:"desired_ipv6,omitempty"`
	DesiredCNAME          string   `json:"desired_cname,omitempty"`
	ObservedIPv4          []string `json:"observed_ipv4"`
	ObservedIPv6          []string `json:"observed_ipv6"`
	ObservedCNAME         []string `json:"observed_cname"`
	ProviderRecordAID     string   `json:"provider_record_a_id,omitempty"`
	ProviderRecordAAAAID  string   `json:"provider_record_aaaa_id,omitempty"`
	ProviderRecordCNAMEID string   `json:"provider_record_cname_id,omitempty"`
	CreatedAByPanel       bool     `json:"created_a_by_panel"`
	CreatedAAAAByPanel    bool     `json:"created_aaaa_by_panel"`
	CreatedCNAMEByPanel   bool     `json:"created_cname_by_panel"`
	LastReconcileUnix     int64    `json:"last_reconcile_unix"`
	NextReconcileUnix     int64    `json:"next_reconcile_unix"`
	RetryCount            int      `json:"retry_count"`
	LastError             string   `json:"last_error,omitempty"`
	CreatedAtUnix         int64    `json:"created_at_unix"`
	UpdatedAtUnix         int64    `json:"updated_at_unix"`
}

// ACMEAccount stores public registration metadata and encrypted account/EAB keys.
type ACMEAccount struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	DirectoryURL         string `json:"directory_url"`
	Email                string `json:"email,omitempty"`
	AccountKeyCiphertext string `json:"-"`
	HasAccountKey        bool   `json:"has_account_key"`
	RegistrationURI      string `json:"registration_uri,omitempty"`
	EABKeyID             string `json:"eab_key_id,omitempty"`
	EABHMACCiphertext    string `json:"-"`
	HasEABHMAC           bool   `json:"has_eab_hmac"`
	TermsAcceptedUnix    int64  `json:"terms_accepted_unix"`
	Status               string `json:"status"`
	LastError            string `json:"last_error,omitempty"`
	CreatedAtUnix        int64  `json:"created_at_unix"`
	UpdatedAtUnix        int64  `json:"updated_at_unix"`
}

// ProtocolCertificate tracks public certificate state. Its private key is
// represented only by an opaque Agent key ID and Agent-local paths.
type ProtocolCertificate struct {
	ID                   string   `json:"id"`
	NodeID               string   `json:"node_id"`
	ManagedDomainID      string   `json:"managed_domain_id"`
	ACMEAccountID        string   `json:"acme_account_id"`
	Domains              []string `json:"domains"`
	Status               string   `json:"status"`
	AgentKeyID           string   `json:"agent_key_id,omitempty"`
	PublicKeyFingerprint string   `json:"public_key_fingerprint,omitempty"`
	CandidateCertPath    string   `json:"candidate_cert_path,omitempty"`
	CandidateKeyPath     string   `json:"candidate_key_path,omitempty"`
	ActiveCertPath       string   `json:"active_cert_path,omitempty"`
	ActiveKeyPath        string   `json:"active_key_path,omitempty"`
	CertPEM              string   `json:"-"`
	Serial               string   `json:"serial,omitempty"`
	Fingerprint          string   `json:"fingerprint,omitempty"`
	NotBeforeUnix        int64    `json:"not_before_unix"`
	NotAfterUnix         int64    `json:"not_after_unix"`
	RenewAfterUnix       int64    `json:"renew_after_unix"`
	Revision             int64    `json:"revision"`
	RetryCount           int      `json:"retry_count"`
	NextRetryUnix        int64    `json:"next_retry_unix"`
	LastError            string   `json:"last_error,omitempty"`
	CreatedAtUnix        int64    `json:"created_at_unix"`
	UpdatedAtUnix        int64    `json:"updated_at_unix"`
}

// NodeInboundTLSBinding selects legacy or Panel-managed TLS for one concrete
// node/inbound deployment.
type NodeInboundTLSBinding struct {
	NodeID          string `json:"node_id"`
	InboundID       string `json:"inbound_id"`
	Mode            string `json:"mode"` // legacy | managed
	ManagedDomainID string `json:"managed_domain_id,omitempty"`
	CertificateID   string `json:"certificate_id,omitempty"`
	CreatedAtUnix   int64  `json:"created_at_unix"`
	UpdatedAtUnix   int64  `json:"updated_at_unix"`
}

// AutomationJob is a persistent, leased DNS/certificate operation.
type AutomationJob struct {
	ID               string         `json:"id"`
	Type             string         `json:"type"`
	TargetType       string         `json:"target_type"`
	TargetID         string         `json:"target_id"`
	State            string         `json:"state"`
	Payload          map[string]any `json:"payload"`
	Attempt          int            `json:"attempt"`
	NextRunUnix      int64          `json:"next_run_unix"`
	LeaseOwner       string         `json:"lease_owner,omitempty"`
	LeaseExpiresUnix int64          `json:"lease_expires_unix,omitempty"`
	LastError        string         `json:"last_error,omitempty"`
	CreatedAtUnix    int64          `json:"created_at_unix"`
	UpdatedAtUnix    int64          `json:"updated_at_unix"`
}

type AutomationAuditLog struct {
	ID            string `json:"id"`
	Action        string `json:"action"`
	TargetType    string `json:"target_type,omitempty"`
	TargetID      string `json:"target_id,omitempty"`
	Actor         string `json:"actor,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	Detail        string `json:"detail,omitempty"`
	CreatedAtUnix int64  `json:"created_at_unix"`
}
