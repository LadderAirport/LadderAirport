/** Panel API client — all calls use credentials: 'include' for session cookie. */

const API = '/api/v1'

/** Default timeout for non-streaming API requests. */
const REQUEST_TIMEOUT_MS = 30_000

export const AUTH_EXPIRED_EVENT = 'ladder-airport:auth-expired'

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
    this.name = 'ApiError'
  }
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const opts: RequestInit = {
    method,
    credentials: 'include',
    signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    headers: { Accept: 'application/json' },
  }
  if (body !== undefined) {
    ;(opts.headers as Record<string, string>)['Content-Type'] = 'application/json'
    opts.body = JSON.stringify(body)
  }
  const res = await fetch(`${API}${path}`, opts)
  notifyAuthExpired(res.status, path)
  if (res.status === 204) {
    return undefined as T
  }
  const text = await res.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = { error: text }
    }
  }
  if (!res.ok) {
    const msg =
      data && typeof data === 'object' && data !== null && 'error' in data
        ? String((data as { error: unknown }).error)
        : res.statusText || `HTTP ${res.status}`
    throw new ApiError(res.status, msg)
  }
  // Uplink nodes cannot be dialed live: the immediate operation was enqueued
  // (HTTP 202 + command_id). Transparently poll the command to completion so
  // callers get the same resolved result as a push node's synchronous reply.
  if (res.status === 202 && isQueuedCommand(data)) {
    return awaitCommand<T>((data as QueuedCommand).command_id)
  }
  return data as T
}

interface QueuedCommand {
  queued: true
  command_id: string
  type?: string
  message?: string
}

function isQueuedCommand(data: unknown): data is QueuedCommand {
  return (
    typeof data === 'object' &&
    data !== null &&
    (data as { queued?: unknown }).queued === true &&
    typeof (data as { command_id?: unknown }).command_id === 'string'
  )
}

/** Terminal + in-flight states of an enqueued uplink command. */
type CommandStatus = 'pending' | 'leased' | 'succeeded' | 'failed' | 'expired'

interface AgentCommand {
  id: string
  status: CommandStatus
  result?: string
  error?: string
}

const COMMAND_POLL_INTERVAL_MS = 700
const COMMAND_POLL_TIMEOUT_MS = 90_000

/**
 * awaitCommand polls a queued uplink command until it reaches a terminal state.
 * On success it parses the stored JSON result into T (matching the push-mode
 * shape); on failure/expiry/timeout it throws an ApiError.
 */
async function awaitCommand<T>(commandId: string): Promise<T> {
  const deadline = Date.now() + COMMAND_POLL_TIMEOUT_MS
  for (;;) {
    const cmd = await request<AgentCommand>('GET', `/commands/${commandId}`)
    if (cmd.status === 'succeeded') {
      if (!cmd.result) return undefined as T
      try {
        return JSON.parse(cmd.result) as T
      } catch {
        return cmd.result as unknown as T
      }
    }
    if (cmd.status === 'failed') {
      throw new ApiError(502, cmd.error || '节点执行命令失败')
    }
    if (cmd.status === 'expired') {
      throw new ApiError(504, '节点未在有效期内执行命令（可能离线）')
    }
    if (Date.now() >= deadline) {
      throw new ApiError(504, '等待节点执行命令超时')
    }
    await sleep(COMMAND_POLL_INTERVAL_MS)
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function notifyAuthExpired(status: number, path: string) {
  if (status !== 401 || path === '/auth/login' || typeof window === 'undefined') return
  window.dispatchEvent(new CustomEvent(AUTH_EXPIRED_EVENT))
}

async function requestText(path: string): Promise<string> {
  const res = await fetch(`${API}${path}`, {
    credentials: 'include',
    signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    headers: { Accept: 'text/plain, application/json' },
  })
  notifyAuthExpired(res.status, path)
  const text = await res.text()
  if (!res.ok) {
    let message = text || res.statusText || `HTTP ${res.status}`
    try {
      const data = JSON.parse(text) as { error?: unknown }
      if (data.error != null) message = String(data.error)
    } catch {
      // Keep the plain-text response.
    }
    throw new ApiError(res.status, message)
  }
  return text
}

// --- Types ---

/** Node-level NAT rewrite: agent listen port → external public port for subscriptions. */
export interface PortMapping {
  listen_port: number
  public_port: number
}

export interface Node {
  id: string
  name: string
  address: string
  grpc_port: number
  /** Client-facing host for subscriptions; empty falls back to address. */
  public_address?: string
  /**
   * NAT port map for subscription clients.
   * Agent still listens on inbound params.port; only subscription server_port is rewritten.
   */
  port_mappings?: PortMapping[]
  token?: string
  labels: string[]
  pki_cert_serial?: string
  pki_not_after_unix?: number
  /** Host NIC for sing-box direct bind_interface; empty = OS default route. */
  egress_interface?: string
  /** Per-node sing-box 1.12 DNS. The API always returns the prefilled defaults. */
  dns?: NodeDNS
  /** When false, managed domains with address_source=agent_public pause probing. */
  ddns_enabled?: boolean
  /** push = Panel dials Agent gRPC; uplink = Agent reports/pulls over Panel HTTP. */
  control_mode?: 'push' | 'uplink'
  desired_runtime?: 'running' | 'stopped'
  uplink_last_seen_unix?: number
  /**
   * For uplink nodes: whether a live WebSocket control channel is currently
   * connected. When true the node supports the full real-time op set at parity
   * with push/gRPC; when false it only has the HTTP report/config-sync fallback.
   * Undefined for push nodes (not applicable).
   */
  uplink_ws_connected?: boolean
  status: string
  last_seen_unix: number
  config_hash: string
  runtime_state?: string
  agent_version?: string
  singbox_version?: string
  /** Upstream ladder-agent version. Empty on old clients; fall back to agent_version. */
  core_version?: string
  core_commit?: string
  /** linux | android | openwrt. Empty means the client did not report a wrapper. */
  client_type?: string
  client_version?: string
  luci_version?: string
  capabilities?: string[]
  connections?: number
  uplink_bytes?: number
  downlink_bytes?: number
  cpu_percent?: number
  memory_rss_bytes?: number
  metrics_at_unix?: number
  last_error?: string
  inbound_count?: number
  created_at_unix: number
  updated_at_unix: number
}

export interface FRPServerPortRange {
  start: number
  end: number
}

export interface FRPServerConfig {
  node_id: string
  enabled: boolean
  bind_addr: string
  bind_port: number
  proxy_bind_addr: string
  allow_ports: FRPServerPortRange[]
  has_auth_token: boolean
  /** Returned immediately after generation/rotation; it can also be revealed later. */
  generated_auth_token?: string
  tls_force: boolean
  max_ports_per_client: number
  /** Bound managed domain used as the frpc-facing server address; empty = node address. */
  managed_domain_id?: string
  /** frpc dial host: bound domain FQDN, otherwise node public/control address. */
  server_addr?: string
  desired_hash: string
  applied_hash: string
  runtime_state: string
  frps_version: string
  last_error?: string
  started_at_unix: number
  created_at_unix: number
  updated_at_unix: number
}

export interface PutFRPServerConfigInput {
  enabled: boolean
  bind_addr: string
  bind_port: number
  proxy_bind_addr: string
  allow_ports: FRPServerPortRange[]
  /** Empty preserves the encrypted token already stored by Panel. */
  auth_token?: string
  /** Generate and apply a new random token. Existing clients must be updated. */
  rotate_auth_token?: boolean
  tls_force: boolean
  max_ports_per_client: number
  /** Managed domain id to bind as frpc server address; empty string clears the binding. */
  managed_domain_id?: string
}

export interface FRPServerClient {
  key: string
  user?: string
  client_id?: string
  run_id?: string
  version?: string
  wire_protocol?: string
  hostname?: string
  client_ip?: string
  connected_at_unix?: number
  online: boolean
}

export interface FRPServerMapping {
  name: string
  type: string
  status: string
  user?: string
  client_id?: string
  local_ip?: string
  local_port?: number
  remote_port?: number
  custom_domains?: string[]
  subdomain?: string
  current_connections?: number
  traffic_in_bytes?: number
  traffic_out_bytes?: number
  last_start_time?: string
  plugin?: string
}

export interface FRPServerMappings {
  clients?: FRPServerClient[]
  mappings?: FRPServerMapping[]
  collected_at_unix: number
}

export interface NetworkInterface {
  name: string
  addresses: string[]
  up: boolean
  loopback: boolean
  mtu?: number
  hardware_addr?: string
}

export interface FleetOverview {
  total_nodes: number
  online_nodes: number
  offline_nodes: number
  running_nodes: number
  nodes: Node[]
  refreshed_at: number
}

export interface InboundConfig {
  id: string
  name: string
  protocol: string
  params: Record<string, unknown>
  enabled: boolean
  created_at_unix: number
  updated_at_unix: number
}

/** Inbound attached to a node, with optional subscription NAT overrides. */
export interface NodeInboundAttachment extends InboundConfig {
  /** Client-facing host for this inbound; empty falls back to node public_address/address. */
  public_address?: string
  /** Client-facing NAT port; 0/empty uses inbound listen port (and legacy node port_mappings). */
  public_port?: number
  frp_enabled: boolean
  frpc_config: string
}

export interface NodeInboundBinding {
  inbound_id: string
  public_address?: string
  public_port?: number
  frp_enabled?: boolean
  frpc_config?: string
}

export interface Field {
  name: string
  label: string
  type: string // string|int|bool|select|password
  required: boolean
  default?: unknown
  options?: string[]
  description?: string
}

export interface Template {
  id: string
  protocol: string
  name: string
  fields: Field[]
}

export interface PKIStatus {
  enabled: boolean
  root_subject?: string
  root_not_after_unix?: number
  intermediate_subject?: string
  intermediate_not_after_unix?: number
  agent_lifetime_seconds?: number
  directory?: string
  root_key_online?: boolean
  active_certificates?: number
  expiring_certificates?: number
}

export interface PKICertificate {
  serial: string
  node_id?: string
  profile: string
  subject: string
  uri_san: string
  dns_sans?: string
  ip_sans?: string
  not_before_unix: number
  not_after_unix: number
  status: 'active' | 'replaced' | 'revoked' | 'expired'
  revoked_at_unix?: number
  revoke_reason?: string
  created_at_unix: number
}

export interface DNSCredentialField {
  name: string
  label: string
  secret: boolean
  required: boolean
  description?: string
}

export interface DNSProviderMetadata {
  name: string
  label: string
  credential_fields: DNSCredentialField[]
  capabilities: string[]
}

export interface DNSAccount {
  id: string
  name: string
  provider: string
  zone: string
  has_credentials: boolean
  settings: Record<string, unknown>
  enabled: boolean
  last_test_unix: number
  last_test_error?: string
  created_at_unix: number
  updated_at_unix: number
}

export interface ManagedDomain {
  id: string
  node_id: string
  dns_account_id: string
  zone: string
  fqdn: string
  record_mode: 'a' | 'aaaa' | 'dual' | 'cname'
  address_source: 'manual' | 'node_address' | 'agent_public'
  manual_ipv4?: string
  manual_ipv6?: string
  manual_cname?: string
  ttl: number
  enabled: boolean
  state: string
  desired_ipv4?: string
  desired_ipv6?: string
  desired_cname?: string
  observed_ipv4: string[]
  observed_ipv6: string[]
  observed_cname: string[]
  last_reconcile_unix: number
  next_reconcile_unix: number
  last_error?: string
  created_a_by_panel?: boolean
  created_aaaa_by_panel?: boolean
}

export interface ACMEAccount {
  id: string
  name: string
  directory_url: string
  email?: string
  has_account_key: boolean
  registration_uri?: string
  eab_key_id?: string
  has_eab_hmac: boolean
  terms_accepted_unix: number
  status: string
  last_error?: string
}

export interface ProtocolCertificate {
  id: string
  node_id: string
  managed_domain_id: string
  acme_account_id: string
  domains: string[]
  status: string
  active_cert_path?: string
  active_key_path?: string
  serial?: string
  fingerprint?: string
  not_before_unix: number
  not_after_unix: number
  renew_after_unix: number
  revision: number
  last_error?: string
}

export interface AutomationJob {
  id: string
  type: string
  target_type: string
  target_id: string
  state: string
  attempt: number
  next_run_unix: number
  last_error?: string
  created_at_unix: number
  updated_at_unix: number
}

export interface NodeInboundTLSBinding {
  node_id: string
  inbound_id: string
  mode: 'legacy' | 'managed'
  managed_domain_id?: string
  certificate_id?: string
}

export interface TaskNodeResult {
  node_id: string
  ok: boolean
  message: string
}

export interface Task {
  id: string
  type: string
  status: string
  node_ids: string[]
  results: TaskNodeResult[]
  created_at_unix: number
  updated_at_unix: number
}

export interface Settings {
  default_agent_token: string
  grpc_timeout_sec: number
  max_concurrency: number
  listen_addr: string
  public_base_url?: string
  chain_probe_url?: string
  chain_probe_interval_sec?: number
  chain_probe_timeout_sec?: number
}

export interface Subscription {
  id: string
  name: string
  format?: string
  token: string
  inbound_ids: string[]
  /** True includes every enabled local inbound; false allows an external-only subscription. */
  include_all_inbounds: boolean
  include_standalone: boolean
  chain_ids: string[]
  include_all_chains: boolean
  external_source_ids?: string[]
  enabled: boolean
  /** Bound route plan (scope=subscription); empty means none. */
  route_plan_id?: string
  /** Admin kill-switch: disabled subscriptions return 404 on the public link. */
  disabled: boolean
  url?: string
  created_at_unix: number
  updated_at_unix: number
}

export interface ProxyChainHop {
  chain_id?: string
  position: number
  node_id: string
  inbound_id: string
  dial_address: string
  dial_port: number
  tls_skip_verify: boolean
}

export interface ProxyChain {
  id: string
  name: string
  enabled: boolean
  state: 'disabled' | 'deploying' | 'healthy' | 'degraded' | string
  hops: ProxyChainHop[]
  last_deploy_unix: number
  last_deploy_error?: string
  last_probe_unix: number
  last_probe_delay_ms: number
  last_probe_error?: string
  failed_hop_index: number
  created_at_unix: number
  updated_at_unix: number
}

export interface ProxyChainInput {
  name: string
  hops: Array<Omit<ProxyChainHop, 'chain_id' | 'position'>>
}

export interface ChainProbeResult {
  ok: boolean
  delay_ms: number
  message?: string
  failed_hop_index: number
}

export type RoutePlanMatchType =
  | 'domain'
  | 'domain_suffix'
  | 'domain_keyword'
  | 'ip_cidr'
  | 'process_name'

export type RoutePlanAction = 'proxy' | 'direct' | 'block'

export interface RoutePlanRule {
  position: number
  match_type: RoutePlanMatchType
  match_value: string
  action: RoutePlanAction
  /** Required when action === 'proxy'; must reference an existing chain. */
  target_chain_id?: string
  enabled: boolean
}

export interface RoutePlan {
  id: string
  name: string
  scope: 'global' | 'subscription' | string
  subscription_id?: string
  enabled: boolean
  sort_order: number
  rules: RoutePlanRule[]
  created_at_unix: number
  updated_at_unix: number
}

export interface ExternalSource {
  id: string
  name: string
  url: string
  headers?: Record<string, string>
  enabled: boolean
  refresh_interval_sec: number
  last_fetch_unix: number
  last_success_unix: number
  last_error?: string
  content_type?: string
  cached_proxy_count: number
  created_at_unix: number
  updated_at_unix: number
}

export interface ProbeResult {
  node: Node
  agent_version: string
  singbox_version: string
}

export interface Metrics {
  connections: number
  uplink_bytes: number
  downlink_bytes: number
  cpu_percent: number
  memory_rss_bytes: number
}

/** Host-level system metrics collected by the agent (CPU/memory/disk/NIC rates). */
export interface NodeSysmetrics {
  cpu_percent: number
  memory_total_bytes: number
  memory_used_bytes: number
  disk_total_bytes: number
  disk_used_bytes: number
  /** Bytes per second. */
  uplink_bps: number
  /** Bytes per second. */
  downlink_bps: number
  collected_at_unix: number
}

export interface NodeBBRStatus {
  bbr_available: boolean
  bbr_enabled: boolean
  current_congestion_control: string
  current_qdisc: string
  supported_controls: string[]
}

export interface CreateNodeInput {
  name: string
  address?: string
  grpc_port?: number
  public_address?: string
  token?: string
  labels?: string[]
  control_mode?: 'push' | 'uplink'
}

/** One DNS row. The panel parses url into a sing-box 1.12 server when it builds the node config. */
export interface NodeDNSServer {
  name: string
  url: string
}

/** Per-node DNS. Server order is the list order; the first row is final. */
export interface NodeDNS {
  strategy: string
  sniff: boolean
  sniff_override_destination: boolean
  sniff_timeout: string
  independent_cache: boolean
  cache_capacity: number
  reverse_mapping: boolean
  disable_expire: boolean
  disable_cache: boolean
  client_subnet?: string
  servers: NodeDNSServer[]
}

/** Matches pkg/nodedns.Default. The first row is final. */
export const DEFAULT_NODE_DNS: NodeDNS = {
  strategy: 'prefer_ipv4',
  sniff: true,
  sniff_override_destination: true,
  sniff_timeout: '300ms',
  independent_cache: true,
  cache_capacity: 2048,
  reverse_mapping: true,
  disable_expire: false,
  disable_cache: false,
  client_subnet: '',
  servers: [
    { name: 'doh-ali', url: 'https://223.5.5.5/dns-query' },
    { name: 'doh-dnspod', url: 'https://1.12.12.12/dns-query' },
    { name: 'doh-cf', url: 'https://1.1.1.1/dns-query' },
    { name: 'doh-google', url: 'https://8.8.8.8/dns-query' },
  ],
}

export interface UpdateNodeInput {
  name?: string
  address?: string
  grpc_port?: number
  public_address?: string
  token?: string
  labels?: string[]
  egress_interface?: string
  dns?: NodeDNS
  port_mappings?: PortMapping[]
  ddns_enabled?: boolean
  control_mode?: 'push' | 'uplink'
}

export interface BootstrapNodeInput {
  name: string
  address?: string
  grpc_port?: number
  public_address?: string
  token?: string
  labels?: string[]
  agent_version?: string
  control_mode?: 'push' | 'uplink'
}

export interface NodeInstallInfo {
  node: Node
  token: string
  install_command: string
  upgrade_command?: string
  uninstall_command?: string
  steps: string[]
  panel_base_url?: string
  recommended_agent_version?: string
  outdated?: boolean
}

export interface MetaInfo {
  panel_version?: string
  panel_commit?: string
  recommended_agent_version?: string
  agent_upgrade_command: string
  agent_uninstall_command: string
  source: string
  checked_at_unix?: number
}

export interface CreateInboundInput {
  name: string
  protocol: string
  params: Record<string, unknown>
  enabled?: boolean
}

export interface PutSettingsInput {
  default_agent_token?: string
  grpc_timeout_sec?: number
  max_concurrency?: number
  listen_addr?: string
  public_base_url?: string
  chain_probe_url?: string
  chain_probe_interval_sec?: number
  chain_probe_timeout_sec?: number
  new_password?: string
}

export interface BatchRequest {
  node_ids?: string[]
  labels?: string[]
}

// --- Auth ---

export function login(password: string): Promise<{ ok: boolean }> {
  return request('POST', '/auth/login', { password })
}

export function logout(): Promise<void> {
  return request('POST', '/auth/logout')
}

// --- Fleet (multi-node overview / refresh) ---

export function fleetOverview(): Promise<FleetOverview> {
  return request('GET', '/fleet/overview')
}

export function fleetRefresh(): Promise<FleetOverview> {
  return request('POST', '/fleet/refresh')
}

/** Recommended agent version + upgrade/uninstall one-liners. */
export function getMeta(): Promise<MetaInfo> {
  return request('GET', '/meta')
}

// --- Nodes ---

export function listNodes(): Promise<Node[]> {
  return request('GET', '/nodes')
}

export function createNode(body: CreateNodeInput): Promise<Node> {
  return request('POST', '/nodes', body)
}

/** Create node + return one-click agent install command. */
export function bootstrapNode(body: BootstrapNodeInput): Promise<NodeInstallInfo> {
  return request('POST', '/nodes/bootstrap', body)
}

/** Regenerate install command for an existing node. */
export function getNodeInstallCommand(
  id: string,
  opts?: { tls?: boolean; version?: string },
): Promise<NodeInstallInfo> {
  const q = new URLSearchParams()
  if (opts?.tls === false) q.set('tls', '0')
  if (opts?.tls === true) q.set('tls', '1')
  if (opts?.version) q.set('version', opts.version)
  const qs = q.toString()
  return request('POST', `/nodes/${id}/install-command${qs ? `?${qs}` : ''}`)
}

export function updateNode(id: string, body: UpdateNodeInput): Promise<Node> {
  return request('PUT', `/nodes/${id}`, body)
}

export function deleteNode(id: string): Promise<void> {
  return request('DELETE', `/nodes/${id}`)
}

export function probeNode(id: string): Promise<ProbeResult> {
  return request('POST', `/nodes/${id}/probe`)
}

export interface UpgradeAgentResult {
  ok: boolean
  message: string
  version?: string
  staged_path?: string
  previous_version?: string
  node_id?: string
  hint?: string
}

/** Stage remote agent upgrade (binary download + helper restart). */
export function upgradeNode(
  id: string,
  body?: { version?: string; repo?: string; download_url?: string; sha256?: string },
): Promise<UpgradeAgentResult> {
  return request('POST', `/nodes/${id}/upgrade`, body ?? {})
}

export function listNodeInbounds(id: string): Promise<NodeInboundAttachment[]> {
  return request('GET', `/nodes/${id}/inbounds`)
}

export interface SetNodeInboundsResult {
  inbounds: InboundConfig[]
  attachments?: NodeInboundAttachment[]
  deployed: boolean
  deploy_message?: string
  apply_task?: Task
  start_task?: Task
}

/** Attach inbounds (legacy IDs only) and auto-apply+start on the agent. */
export function setNodeInbounds(
  id: string,
  inbound_ids: string[],
  opts?: { skip_deploy?: boolean },
): Promise<SetNodeInboundsResult> {
  return request('PUT', `/nodes/${id}/inbounds`, {
    inbound_ids,
    skip_deploy: opts?.skip_deploy ?? false,
  })
}

/** Attach inbounds with per-inbound public host/port overrides. */
export function setNodeInboundBindings(
  id: string,
  bindings: NodeInboundBinding[],
  opts?: { skip_deploy?: boolean },
): Promise<SetNodeInboundsResult> {
  return request('PUT', `/nodes/${id}/inbounds`, {
    bindings,
    skip_deploy: opts?.skip_deploy ?? false,
  })
}

export function previewNodeConfig(id: string): Promise<unknown> {
  return request('POST', `/nodes/${id}/config/preview`)
}

export function applyNode(id: string): Promise<Task> {
  return request('POST', `/nodes/${id}/apply`)
}

export function startNode(id: string): Promise<Task> {
  return request('POST', `/nodes/${id}/start`)
}

export function stopNode(id: string): Promise<Task> {
  return request('POST', `/nodes/${id}/stop`)
}

export function getNodeMetrics(id: string): Promise<Metrics> {
  return request('GET', `/nodes/${id}/metrics`)
}

/** Host system metrics (CPU/memory/disk/NIC rates); 400 when the agent is too old. */
export function getNodeSysmetrics(id: string): Promise<NodeSysmetrics> {
  return request('GET', `/nodes/${id}/sysmetrics`)
}

/** BBR congestion-control status; 400 when the agent is too old. */
export function getNodeBBR(id: string): Promise<NodeBBRStatus> {
  return request('GET', `/nodes/${id}/bbr`)
}

export function setNodeBBR(id: string, enabled: boolean): Promise<{ ok: boolean; message: string }> {
  return request('POST', `/nodes/${id}/bbr`, { enabled })
}

export function listNodeInterfaces(id: string): Promise<{ interfaces: NetworkInterface[] }> {
  return request('GET', `/nodes/${id}/interfaces`)
}

export function getNodeFRPS(id: string): Promise<FRPServerConfig> {
  return request('GET', `/nodes/${id}/frps`)
}

export function putNodeFRPS(id: string, body: PutFRPServerConfigInput): Promise<FRPServerConfig> {
  return request('PUT', `/nodes/${id}/frps`, body)
}

export function getNodeFRPSStatus(id: string): Promise<FRPServerConfig> {
  return request('GET', `/nodes/${id}/frps/status`)
}

export function getNodeFRPSMappings(id: string): Promise<FRPServerMappings> {
  return request('GET', `/nodes/${id}/frps/mappings`)
}

export function startNodeFRPS(id: string): Promise<FRPServerConfig> {
  return request('POST', `/nodes/${id}/frps/start`)
}

export function stopNodeFRPS(id: string): Promise<FRPServerConfig> {
  return request('POST', `/nodes/${id}/frps/stop`)
}

export function revealNodeFRPSToken(id: string): Promise<{ auth_token: string }> {
  return request('POST', `/nodes/${id}/frps/token/reveal`)
}

/**
 * Stream node logs via fetch + ReadableStream so the session cookie is sent.
 * EventSource does not reliably include credentials in all browsers.
 * Yields parsed SSE `data:` payloads (JSON objects with level/message/ts).
 */
export async function streamNodeLogs(
  id: string,
  opts: {
    level?: string
    tail?: number
    signal?: AbortSignal
    onLine: (line: { level: string; message: string; ts: number }) => void
  },
): Promise<void> {
  const qs = new URLSearchParams()
  if (opts.level) qs.set('level', opts.level)
  if (opts.tail != null) qs.set('tail', String(opts.tail))
  const q = qs.toString()
  const url = `${API}/nodes/${id}/logs${q ? `?${q}` : ''}`
  const res = await fetch(url, {
    method: 'GET',
    credentials: 'include',
    signal: opts.signal,
    headers: { Accept: 'text/event-stream' },
  })
  notifyAuthExpired(res.status, `/nodes/${id}/logs`)
  if (!res.ok) {
    const text = await res.text()
    let msg = res.statusText
    try {
      const j = JSON.parse(text)
      if (j.error) msg = j.error
    } catch {
      if (text) msg = text
    }
    throw new ApiError(res.status, msg)
  }
  if (!res.body) {
    throw new ApiError(500, 'no response body for log stream')
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const parts = buffer.split('\n')
    buffer = parts.pop() ?? ''
    for (const raw of parts) {
      const line = raw.replace(/\r$/, '')
      if (line.startsWith('data:')) {
        const payload = line.slice(5).trim()
        if (!payload) continue
        try {
          const obj = JSON.parse(payload) as {
            level: string
            message: string
            ts: number
          }
          opts.onLine(obj)
        } catch {
          // ignore non-JSON data lines
        }
      }
    }
  }
}

// --- Inbounds ---

export function listInbounds(): Promise<InboundConfig[]> {
  return request('GET', '/inbounds')
}

export function createInbound(body: CreateInboundInput): Promise<InboundConfig> {
  return request('POST', '/inbounds', body)
}

export function updateInbound(
  id: string,
  body: Partial<InboundConfig>,
): Promise<InboundConfig> {
  return request('PUT', `/inbounds/${id}`, body)
}

export function deleteInbound(id: string): Promise<void> {
  return request('DELETE', `/inbounds/${id}`)
}

// --- Templates ---

export function listTemplates(): Promise<Template[]> {
  return request('GET', '/templates')
}

// --- Batch ---

export function batchApply(body: BatchRequest): Promise<Task> {
  return request('POST', '/batch/apply', body)
}

export function batchStart(body: BatchRequest): Promise<Task> {
  return request('POST', '/batch/start', body)
}

export function batchStop(body: BatchRequest): Promise<Task> {
  return request('POST', '/batch/stop', body)
}

// --- Tasks ---

export function listTasks(): Promise<Task[]> {
  return request('GET', '/tasks')
}

export function getTask(id: string): Promise<Task> {
  return request('GET', `/tasks/${id}`)
}

// --- Settings ---

export function getSettings(): Promise<Settings> {
  return request('GET', '/settings')
}

export function putSettings(body: PutSettingsInput): Promise<Settings> {
  return request('PUT', '/settings', body)
}

// --- Management PKI ---

export function getPKIStatus(): Promise<PKIStatus> {
  return request('GET', '/pki/status')
}

export function listPKICertificates(): Promise<PKICertificate[]> {
  return request('GET', '/pki/certificates')
}

export function revokePKICertificate(serial: string, reason = ''): Promise<{ ok: boolean }> {
  return request('POST', `/pki/certificates/${encodeURIComponent(serial)}/revoke`, { reason })
}

// --- Managed DNS / ACME protocol certificates ---

export function listDNSProviders(): Promise<DNSProviderMetadata[]> {
  return request('GET', '/dns/providers')
}

export function listDNSAccounts(): Promise<DNSAccount[]> {
  return request('GET', '/dns/accounts')
}

export function createDNSAccount(body: {
  name: string
  provider: string
  zone: string
  credentials: Record<string, string>
  settings?: Record<string, unknown>
  enabled?: boolean
}): Promise<DNSAccount> {
  return request('POST', '/dns/accounts', body)
}

export function deleteDNSAccount(id: string): Promise<void> {
  return request('DELETE', `/dns/accounts/${id}`)
}

export function testDNSAccount(id: string): Promise<{ ok: boolean }> {
  return request('POST', `/dns/accounts/${id}/test`)
}

export function listManagedDomains(nodeId?: string): Promise<ManagedDomain[]> {
  const qs = nodeId ? `?node_id=${encodeURIComponent(nodeId)}` : ''
  return request('GET', `/managed-domains${qs}`)
}

export function createManagedDomain(body: {
  node_id: string
  dns_account_id: string
  fqdn: string
  record_mode: 'a' | 'aaaa' | 'dual' | 'cname'
  address_source: 'manual' | 'node_address' | 'agent_public'
  manual_ipv4?: string
  manual_ipv6?: string
  manual_cname?: string
  ttl?: number
}): Promise<ManagedDomain> {
  return request('POST', '/managed-domains', body)
}

export function reconcileManagedDomain(id: string): Promise<AutomationJob> {
  return request('POST', `/managed-domains/${id}/reconcile`)
}

export function deleteManagedDomain(id: string, deleteRecords: boolean): Promise<AutomationJob | void> {
  const qs = deleteRecords ? '?delete_records=true' : ''
  return request('DELETE', `/managed-domains/${encodeURIComponent(id)}${qs}`)
}

export function listACMEAccounts(): Promise<ACMEAccount[]> {
  return request('GET', '/acme/accounts')
}

export function createACMEAccount(body: {
  name: string
  directory_url: string
  email?: string
  eab_key_id?: string
  eab_hmac?: string
  accept_terms: boolean
}): Promise<ACMEAccount> {
  return request('POST', '/acme/accounts', body)
}

export function registerACMEAccount(id: string): Promise<ACMEAccount> {
  return request('POST', `/acme/accounts/${id}/register`)
}

export function deleteACMEAccount(id: string): Promise<void> {
  return request('DELETE', `/acme/accounts/${encodeURIComponent(id)}`)
}

export function listProtocolCertificates(): Promise<ProtocolCertificate[]> {
  return request('GET', '/protocol-certificates')
}

export function createProtocolCertificate(body: {
  node_id: string
  managed_domain_id: string
  acme_account_id: string
}): Promise<{ certificate: ProtocolCertificate; job: AutomationJob }> {
  return request('POST', '/protocol-certificates', body)
}

export function issueProtocolCertificate(id: string): Promise<AutomationJob> {
  return request('POST', `/protocol-certificates/${id}/issue`)
}

export function deleteProtocolCertificate(id: string): Promise<void> {
  return request('DELETE', `/protocol-certificates/${encodeURIComponent(id)}`)
}

export function listAutomationJobs(): Promise<AutomationJob[]> {
  return request('GET', '/automation/jobs')
}

export function getNodeInboundTLS(nodeID: string, inboundID: string): Promise<NodeInboundTLSBinding> {
  return request('GET', `/nodes/${nodeID}/inbounds/${inboundID}/tls`)
}

export function putNodeInboundTLS(
  nodeID: string,
  inboundID: string,
  body: Pick<NodeInboundTLSBinding, 'mode' | 'managed_domain_id' | 'certificate_id'>,
): Promise<{ binding: NodeInboundTLSBinding; apply_task?: Task; message?: string }> {
  return request('PUT', `/nodes/${nodeID}/inbounds/${inboundID}/tls`, body)
}

// --- Subscriptions ---

export function listSubscriptions(): Promise<Subscription[]> {
  return request('GET', '/subscriptions')
}

export function createSubscription(body: {
  name: string
  format?: string
  inbound_ids?: string[]
  include_all_inbounds?: boolean
  include_standalone?: boolean
  chain_ids?: string[]
  include_all_chains?: boolean
  external_source_ids?: string[]
  route_plan_id?: string
  enabled?: boolean
}): Promise<Subscription> {
  return request('POST', '/subscriptions', body)
}

export function updateSubscription(
  id: string,
  body: {
    name?: string
    format?: string
    inbound_ids?: string[]
    include_all_inbounds?: boolean
    include_standalone?: boolean
    chain_ids?: string[]
    include_all_chains?: boolean
    external_source_ids?: string[]
    route_plan_id?: string
    enabled?: boolean
    rotate_token?: boolean
  },
): Promise<Subscription> {
  return request('PUT', `/subscriptions/${id}`, body)
}

export function deleteSubscription(id: string): Promise<void> {
  return request('DELETE', `/subscriptions/${id}`)
}

/** Rotate the public subscription token; the old link is invalidated immediately. */
export function rotateSubscriptionToken(id: string): Promise<{ token: string }> {
  return request('POST', `/subscriptions/${id}/token/rotate`)
}

/** Disable the public subscription link (returns 404) without deleting config. */
export function disableSubscription(id: string): Promise<{ ok: boolean }> {
  return request('POST', `/subscriptions/${id}/disable`)
}

export function enableSubscription(id: string): Promise<{ ok: boolean }> {
  return request('POST', `/subscriptions/${id}/enable`)
}

export async function previewSubscription(id: string, format?: string): Promise<string> {
  const query = format ? `?format=${encodeURIComponent(format)}` : ''
  return requestText(`/subscriptions/${id}/preview${query}`)
}

// --- Server-side proxy chains ---

export function listProxyChains(): Promise<ProxyChain[]> {
  return request('GET', '/proxy-chains')
}

export function createProxyChain(body: ProxyChainInput): Promise<ProxyChain> {
  return request('POST', '/proxy-chains', body)
}

export function updateProxyChain(id: string, body: ProxyChainInput): Promise<ProxyChain> {
  return request('PUT', `/proxy-chains/${id}`, body)
}

export function deleteProxyChain(id: string): Promise<void> {
  return request('DELETE', `/proxy-chains/${id}`)
}

export function enableProxyChain(id: string): Promise<ProxyChain> {
  return request('POST', `/proxy-chains/${id}/enable`)
}

export function disableProxyChain(id: string): Promise<ProxyChain> {
  return request('POST', `/proxy-chains/${id}/disable`)
}

export function probeProxyChain(id: string): Promise<ChainProbeResult> {
  return request('POST', `/proxy-chains/${id}/probe`)
}

export function previewProxyChain(body: ProxyChainInput): Promise<{ configs: Record<string, unknown> }> {
  return request('POST', '/proxy-chains/preview', body)
}

// --- Route plans (subscription routing rules) ---

export function listRoutePlans(): Promise<RoutePlan[]> {
  return request('GET', '/route-plans')
}

export function createRoutePlan(body: {
  name: string
  scope: 'global' | 'subscription'
  subscription_id?: string
  enabled?: boolean
  rules?: RoutePlanRule[]
}): Promise<RoutePlan> {
  return request('POST', '/route-plans', body)
}

export function getRoutePlan(id: string): Promise<RoutePlan> {
  return request('GET', `/route-plans/${id}`)
}

/** When rules are provided they fully replace the existing set in array order. */
export function updateRoutePlan(
  id: string,
  body: {
    name?: string
    enabled?: boolean
    sort_order?: number
    rules?: RoutePlanRule[]
  },
): Promise<RoutePlan> {
  return request('PUT', `/route-plans/${id}`, body)
}

export function deleteRoutePlan(id: string): Promise<void> {
  return request('DELETE', `/route-plans/${id}`)
}

// --- External subscription sources ---

export function listExternalSources(): Promise<ExternalSource[]> {
  return request('GET', '/external-sources')
}

export function createExternalSource(body: {
  name: string
  url: string
  headers?: Record<string, string>
  enabled?: boolean
  refresh_interval_sec?: number
}): Promise<ExternalSource> {
  return request('POST', '/external-sources', body)
}

export function updateExternalSource(
  id: string,
  body: {
    name?: string
    url?: string
    headers?: Record<string, string>
    enabled?: boolean
    refresh_interval_sec?: number
  },
): Promise<ExternalSource> {
  return request('PUT', `/external-sources/${id}`, body)
}

export function deleteExternalSource(id: string): Promise<void> {
  return request('DELETE', `/external-sources/${id}`)
}

export function refreshExternalSource(id: string): Promise<ExternalSource> {
  return request('POST', `/external-sources/${id}/refresh`)
}

export function previewExternalSource(id: string): Promise<{
  count: number
  names: string[]
  warnings?: string[]
  source: Partial<ExternalSource>
}> {
  return request('GET', `/external-sources/${id}/preview`)
}
