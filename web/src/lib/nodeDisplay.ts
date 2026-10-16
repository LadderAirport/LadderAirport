/** Shared display helpers for node status, metrics, and tasks. */

export function formatBytes(n?: number): string {
  if (n == null || !Number.isFinite(n) || n < 0) return n == null ? '—' : '0 B'
  if (n === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 && i > 0 ? v.toFixed(1) : i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

export function formatTime(unix?: number): string {
  if (!unix) return '—'
  return new Date(unix * 1000).toLocaleString()
}

export function statusLabel(s?: string): string {
  switch (s) {
    case 'online':
    case 'running': // legacy: apply once wrote runtime into status
      return '在线'
    case 'unreachable':
      return '离线'
    case 'unauthorized':
      return '鉴权失败'
    case 'pending':
      return '待安装'
    case 'unknown':
      return '未知'
    default:
      return s || '未知'
  }
}

/** Map connectivity status to Badge theme. */
export function statusTheme(s?: string): 'success' | 'destructive' | 'warning' | 'default' {
  if (s === 'online' || s === 'running') return 'success'
  if (s === 'unreachable' || s === 'unauthorized') return 'destructive'
  if (s === 'pending') return 'warning'
  return 'default'
}

export function isOnlineStatus(s?: string): boolean {
  return s === 'online' || s === 'running'
}

/** Host shown in the fleet. Uplink nodes have no Panel-dialed gRPC port. */
export function nodeEndpointLabel(node: {
  control_mode?: string
  address?: string
  grpc_port?: number
  public_address?: string
}): string {
  if (node.control_mode === 'uplink') {
    return node.public_address || node.address || '—'
  }
  return `${node.address || '（待填）'}:${node.grpc_port || ''}`
}

export function runtimeLabel(s?: string): string {
  switch (s) {
    case 'running':
      return '运行中'
    case 'stopped':
      return '已停止'
    case 'error':
      return '错误'
    default:
      return s || '未知'
  }
}

export function runtimeTheme(s?: string): 'success' | 'destructive' | 'default' {
  if (s === 'running') return 'success'
  if (s === 'error') return 'destructive'
  return 'default'
}

export function taskKindLabel(kind: string): string {
  switch (kind) {
    case 'apply':
      return '应用配置'
    case 'start':
      return '启动服务'
    case 'stop':
      return '停止服务'
    default:
      return kind
  }
}

export function taskStatusLabel(status: string): string {
  switch (status) {
    case 'online':
      return '在线'
    case 'unreachable':
      return '无法连接'
    case 'success':
      return '成功'
    case 'failed':
      return '失败'
    case 'partial':
      return '部分成功'
    case 'pending':
      return '等待中'
    case 'running':
      return '进行中'
    default:
      return status || '未知'
  }
}

/** Parse a semver-ish string: "v0.3.1", "0.1.0-dev", "1.2". */
export type SemVer = {
  major: number
  minor: number
  patch: number
  pre: string
  valid: boolean
  raw: string
}

function looksLikeVersion(s: string): boolean {
  const t = s.replace(/^[vV]/, '')
  if (!t || t[0] < '0' || t[0] > '9') return false
  return /[.+-]/.test(t) || /^\d+$/.test(t)
}

export function parseSemVer(input?: string | null): SemVer {
  const raw = String(input || '').trim()
  if (!raw) return { major: 0, minor: 0, patch: 0, pre: '', valid: false, raw }
  let candidate = raw
  for (const part of raw.split(/\s+/)) {
    const p = part.replace(/^[,;()[\]{}]+|[,;()[\]{}]+$/g, '')
    if (looksLikeVersion(p)) {
      candidate = p
      break
    }
  }
  candidate = candidate.replace(/^[vV]/, '')
  const plus = candidate.indexOf('+')
  if (plus >= 0) candidate = candidate.slice(0, plus)
  let pre = ''
  let core = candidate
  const dash = candidate.indexOf('-')
  if (dash >= 0) {
    core = candidate.slice(0, dash)
    pre = candidate.slice(dash + 1)
  }
  const segs = core.split('.')
  const nums: number[] = []
  for (const seg of segs) {
    if (!/^\d+$/.test(seg)) return { major: 0, minor: 0, patch: 0, pre: '', valid: false, raw }
    nums.push(parseInt(seg, 10))
    if (nums.length === 3) break
  }
  if (nums.length === 0) return { major: 0, minor: 0, patch: 0, pre: '', valid: false, raw }
  while (nums.length < 3) nums.push(0)
  return { major: nums[0], minor: nums[1], patch: nums[2], pre, valid: true, raw }
}

function cmpInt(a: number, b: number): number {
  return a < b ? -1 : a > b ? 1 : 0
}

function comparePre(a: string, b: string): number {
  const as = a.split('.')
  const bs = b.split('.')
  const n = Math.min(as.length, bs.length)
  for (let i = 0; i < n; i++) {
    const aNum = /^\d+$/.test(as[i])
    const bNum = /^\d+$/.test(bs[i])
    if (aNum && bNum) {
      const c = cmpInt(parseInt(as[i], 10), parseInt(bs[i], 10))
      if (c !== 0) return c
    } else if (aNum && !bNum) return -1
    else if (!aNum && bNum) return 1
    else if (as[i] < bs[i]) return -1
    else if (as[i] > bs[i]) return 1
  }
  return cmpInt(as.length, bs.length)
}

/** -1 if a<b, 0 if equal, 1 if a>b. Invalid < valid. */
export function compareSemVer(a: SemVer, b: SemVer): number {
  if (!a.valid && !b.valid) return 0
  if (!a.valid) return -1
  if (!b.valid) return 1
  if (a.major !== b.major) return cmpInt(a.major, b.major)
  if (a.minor !== b.minor) return cmpInt(a.minor, b.minor)
  if (a.patch !== b.patch) return cmpInt(a.patch, b.patch)
  if (!a.pre && !b.pre) return 0
  if (!a.pre) return 1
  if (!b.pre) return -1
  return comparePre(a.pre, b.pre)
}

/** Normalize version for display/fallback equality (strip leading v, lower-case). */
export function normalizeVersion(v?: string | null): string {
  return String(v || '')
    .trim()
    .replace(/^[vV]/, '')
    .toLowerCase()
}

/**
 * True when current agent version should be upgraded to recommended.
 * Uses semver ordering: 0.2.0 < 0.3.1; 0.3.1-rc.1 < 0.3.1; 0.4.0 is NOT outdated vs 0.3.1.
 */
export function isAgentOutdated(
  current?: string | null,
  recommended?: string | null,
): boolean {
  const recRaw = String(recommended || '').trim()
  if (!recRaw) return false
  const curRaw = String(current || '').trim()
  if (!curRaw) return true
  const low = curRaw.toLowerCase()
  if (low === 'unknown' || low === 'dev') return true

  const cur = parseSemVer(curRaw)
  const rec = parseSemVer(recRaw)
  if (cur.valid && rec.valid) return compareSemVer(cur, rec) < 0
  if (rec.valid && !cur.valid) return true
  return normalizeVersion(curRaw) !== normalizeVersion(recRaw)
}

/** Core version used for staleness. Old clients only send agent_version. */
export function coreVersion(node?: { core_version?: string | null; agent_version?: string | null } | null): string {
  return String(node?.core_version || node?.agent_version || '').trim()
}

/**
 * Second fleet line for a wrapper. Linux and unknown old clients stay on one line.
 * Does not infer a platform from the version string.
 */
export function clientVersionLine(node?: {
  client_type?: string | null
  client_version?: string | null
} | null): string {
  const kind = String(node?.client_type || '').trim().toLowerCase()
  const version = String(node?.client_version || '').trim()
  if (kind === 'android') return version ? `Android ${version}` : 'Android'
  if (kind === 'openwrt') return version ? `OpenWrt ${version}` : 'OpenWrt'
  return ''
}

/** Linux release binaries must not be pushed to Android or OpenWrt wrappers. */
export function linuxReleaseUpgradeAllowed(clientType?: string | null): boolean {
  const kind = String(clientType || '').trim().toLowerCase()
  return kind !== 'android' && kind !== 'openwrt'
}
