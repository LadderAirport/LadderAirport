import { useState } from 'react'
import { type ConnectionErrors } from '@/components/NodeDetailDrawer'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Eye, EyeOff, X, Copy, Check, QrCode } from 'lucide-react'
import type { NetworkInterface, NodeDNS, NodeInstallInfo } from '@/api/client'
import { NodePairingQRModal } from '@/components/NodePairingQRModal'
import { linuxReleaseUpgradeAllowed } from '@/lib/nodeDisplay'

/** Radix Select rejects empty-string item values; use a sentinel for "系统默认". */
const EGRESS_DEFAULT_VALUE = '__default__'

const DNS_SERVERS: { tag: string; title: string; role: string }[] = [
  { tag: 'doh-ali', title: 'AliDNS', role: '国内：geosite-cn 中除 .cn 以外的域名，例如 baidu.com' },
  { tag: 'doh-dnspod', title: 'DNSPod', role: '国内：.cn。先于 geosite-cn 命中，避免 .cn 再打到 AliDNS' },
  { tag: 'doh-cf', title: 'Cloudflare', role: '海外 final：其余查询（主要是 A）。下一跳主机名也用它解析' },
  { tag: 'doh-google', title: 'Google', role: '海外：非中国域名的 AAAA 与 HTTPS。不是 Cloudflare 的备份' },
]

const DNS_STRATEGIES = [
  { value: 'prefer_ipv4', label: '优先 IPv4' },
  { value: 'prefer_ipv6', label: '优先 IPv6' },
  { value: 'ipv4_only', label: '仅 IPv4' },
  { value: 'ipv6_only', label: '仅 IPv6' },
]

function singboxDNSGate(version?: string): 'ready' | 'old' | 'unknown' {
  const match = (version || '').match(/(\d+)\.(\d+)/)
  if (!match) return 'unknown'
  const major = Number(match[1])
  const minor = Number(match[2])
  if (major > 1 || (major === 1 && minor >= 12)) return 'ready'
  return 'old'
}

export interface NodeOverviewTabProps {
  busy: boolean
  editName: string
  setEditName: (v: string) => void
  editLabels: string[]
  setEditLabels: (v: string[]) => void
  editLabelDraft: string
  setEditLabelDraft: (v: string) => void
  editToken: string
  setEditToken: (v: string) => void
  setEditTokenChanged: (v: boolean) => void
  tokenVisible: boolean
  setTokenVisible: (v: boolean) => void
  editAddress: string
  setEditAddress: (v: string) => void
  editPort: number | string
  setEditPort: (v: number | string) => void
  editPublic: string
  setEditPublic: (v: string) => void
  editEgress: string
  setEditEgress: (v: string) => void
  editDNS: NodeDNS
  setEditDNS: (v: NodeDNS) => void
  singboxVersion?: string
  clientType?: string
  editDDNS: boolean
  setEditDDNS: (v: boolean) => void
  editControlMode: 'push' | 'uplink'
  setEditControlMode: (v: 'push' | 'uplink') => void
  canUplink: boolean
  connectionErrors: ConnectionErrors
  clearConnectionError: (f: keyof ConnectionErrors) => void
  onSaveConnection: () => void
  ifaces: NetworkInterface[]
  installInfo: NodeInstallInfo | null
  copied: boolean
  copiedUpgrade: boolean
  copyInstallCommand: () => void
  copyUpgradeCommand: () => void
}

export function NodeOverviewTab({
  busy,
  editName,
  setEditName,
  editLabels,
  setEditLabels,
  editLabelDraft,
  setEditLabelDraft,
  editToken,
  setEditToken,
  setEditTokenChanged,
  tokenVisible,
  setTokenVisible,
  editAddress,
  setEditAddress,
  editPort,
  setEditPort,
  editPublic,
  setEditPublic,
  editEgress,
  setEditEgress,
  editDNS,
  setEditDNS,
  singboxVersion,
  clientType,
  editDDNS,
  setEditDDNS,
  editControlMode,
  setEditControlMode,
  canUplink,
  connectionErrors,
  clearConnectionError,
  onSaveConnection,
  ifaces,
  installInfo,
  copied,
  copiedUpgrade,
  copyInstallCommand,
  copyUpgradeCommand,
}: NodeOverviewTabProps) {
  const [showPairingQR, setShowPairingQR] = useState(false)
  const reported = installInfo?.node
  const installed = Boolean(
    reported && (reported.agent_version || reported.last_seen_unix || reported.uplink_last_seen_unix || reported.pki_cert_serial),
  )
  const showInstall = Boolean(installInfo?.install_command) && !installed
  const showUpgrade =
    installed &&
    linuxReleaseUpgradeAllowed(reported?.client_type) &&
    Boolean(installInfo?.upgrade_command && installInfo.outdated)

  const dnsGate = singboxDNSGate(singboxVersion)
  const androidNode = (clientType || '').toLowerCase() === 'android'

  function patchDNS(patch: Partial<NodeDNS>) {
    setEditDNS({ ...editDNS, ...patch })
  }

  function patchServer(tag: string, patch: Partial<NodeDNS['servers'][number]>) {
    setEditDNS({
      ...editDNS,
      servers: editDNS.servers.map((server) => (server.tag === tag ? { ...server, ...patch } : server)),
    })
  }

  function egressOptions() {
    const opts = [{ value: EGRESS_DEFAULT_VALUE, label: '系统默认' }]
    for (const iface of ifaces) {
      const ips = iface.addresses && iface.addresses.length > 0 ? ` (${iface.addresses.join(', ')})` : ''
      opts.push({ value: iface.name, label: `${iface.name}${ips}` })
    }
    return opts
  }

  return (
    <div className="space-y-6">
      {/* Node Info Section */}
      <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5">
        <h3 className="text-sm font-semibold text-foreground">节点信息</h3>
        <div className="space-y-4">
          <div className="flex flex-col space-y-1.5">
            <Label htmlFor="node-edit-name" className="text-muted-foreground">
              节点名称 *
            </Label>
            <Input
              id="node-edit-name"
              value={editName}
              disabled={busy}
              onChange={(e) => {
                setEditName(e.target.value)
                clearConnectionError('name')
              }}
            />
            {connectionErrors.name && (
              <p className="text-xs text-destructive font-medium">{connectionErrors.name}</p>
            )}
          </div>

          <div className="flex flex-col space-y-1.5">
            <Label htmlFor="node-edit-labels" className="text-muted-foreground font-medium">
              标签
            </Label>
            <div className="flex flex-wrap gap-1.5 p-2 border border-border rounded-md bg-background min-h-[42px]">
              {editLabels.map((lbl) => (
                <Badge key={lbl} variant="secondary" className="gap-1 pr-1 bg-secondary text-foreground">
                  {lbl}
                  <button
                    type="button"
                    onClick={() => setEditLabels(editLabels.filter((l) => l !== lbl))}
                    className="text-muted-foreground hover:text-foreground cursor-pointer"
                  >
                    <X className="h-3 w-3" />
                  </button>
                </Badge>
              ))}
              <input
                id="node-edit-labels"
                value={editLabelDraft}
                disabled={busy}
                onChange={(e) => setEditLabelDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ',') {
                    e.preventDefault()
                    const tag = editLabelDraft.trim()
                    if (tag && !editLabels.includes(tag)) {
                      setEditLabels([...editLabels, tag])
                    }
                    setEditLabelDraft('')
                  }
                }}
                placeholder={editLabels.length === 0 ? '输入标签后按 Enter' : '新标签...'}
                className="flex-1 bg-transparent border-0 outline-none text-sm p-0 focus:ring-0 text-foreground placeholder:text-muted-foreground"
              />
            </div>
          </div>

          <div className="flex flex-col space-y-1.5">
            <Label htmlFor="node-edit-token" className="text-muted-foreground">
              Agent Token
            </Label>
            <div className="relative">
              <Input
                id="node-edit-token"
                type={tokenVisible ? 'text' : 'password'}
                value={editToken}
                disabled={busy}
                autoComplete="new-password"
                onChange={(e) => {
                  setEditToken(e.target.value)
                  setEditTokenChanged(true)
                }}
                className="pr-10"
                placeholder="留空时安装命令会回退使用系统默认 Token"
              />
              <button
                type="button"
                onClick={() => setTokenVisible(!tokenVisible)}
                className="absolute right-3 top-2.5 text-muted-foreground hover:text-foreground"
              >
                {tokenVisible ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            </div>
          </div>
        </div>
      </div>

      {/* Panel Controller Section */}
      <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5">
        <div className="space-y-1">
          <h3 className="text-sm font-semibold text-foreground">
            {editControlMode === 'uplink' ? '控制模式' : 'Panel 控制面'}
          </h3>
          <p className="text-xs text-muted-foreground">
            {editControlMode === 'uplink'
              ? '节点主动建立 WebSocket 长连接，断线时回退到 HTTP 上报和拉配置。'
              : 'Panel 拨号用。NAT 时填映射后的公网/VPN 地址与外部 gRPC 端口。'}
          </p>
        </div>
        <div className="flex flex-col space-y-1.5">
          <Label htmlFor="node-edit-control-mode" className="text-muted-foreground">
            控制模式
          </Label>
          <Select
            value={editControlMode}
            onValueChange={(value) => setEditControlMode(value as 'push' | 'uplink')}
            disabled={busy}
          >
            <SelectTrigger id="node-edit-control-mode">
              <SelectValue />
            </SelectTrigger>
            <SelectContent className="bg-popover border-border">
              <SelectItem value="push">push（Panel 拨号 gRPC）</SelectItem>
              <SelectItem value="uplink" disabled={!canUplink}>
                uplink（Agent WebSocket 长连接）
              </SelectItem>
            </SelectContent>
          </Select>
          {!canUplink ? (
            <p className="text-xs text-muted-foreground">当前 Agent 未上报 uplink 能力，升级后再切换。</p>
          ) : null}
        </div>
        {editControlMode === 'uplink' ? null : (
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div className="flex flex-col space-y-1.5">
            <Label htmlFor="node-edit-address" className="text-muted-foreground">
              控制面地址
            </Label>
            <Input
              id="node-edit-address"
              value={editAddress}
              disabled={busy}
              onChange={(e) => {
                setEditAddress(e.target.value)
                clearConnectionError('address')
              }}
              placeholder="公网 IP / DDNS / VPN 地址"
            />
            {connectionErrors.address && (
              <p className="text-xs text-destructive font-medium">{connectionErrors.address}</p>
            )}
          </div>

          <div className="flex flex-col space-y-1.5">
            <Label htmlFor="node-edit-grpc-port" className="text-muted-foreground">
              gRPC 端口 (外部映射)
            </Label>
            <Input
              id="node-edit-grpc-port"
              type="number"
              value={editPort}
              min={1}
              max={65535}
              disabled={busy}
              onChange={(e) => {
                setEditPort(e.target.value)
                clearConnectionError('grpcPort')
              }}
            />
            {connectionErrors.grpcPort && (
              <p className="text-xs text-destructive font-medium">{connectionErrors.grpcPort}</p>
            )}
          </div>
        </div>
        )}
      </div>

      {/* DDNS Auto-resolve */}
      <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5">
        <div className="flex items-center gap-3 rounded-md border border-border bg-background px-3 py-3">
          <Checkbox
            id="node-ddns-enabled"
            checked={editDDNS}
            disabled={busy}
            onCheckedChange={(checked) => setEditDDNS(Boolean(checked))}
          />
          <div>
            <Label htmlFor="node-ddns-enabled" className="cursor-pointer text-foreground">DDNS 自动解析</Label>
            <p className="text-xs text-muted-foreground">
              开启时系统定期探测节点公网 IP 并更新关联的托管域名 A 记录；关闭后不再探测，重新开启自动恢复。
            </p>
          </div>
        </div>
      </div>

      {/* Public Sub Entry */}
      <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5">
        <div className="space-y-1">
          <h3 className="text-sm font-semibold text-foreground">订阅默认入口</h3>
          <p className="text-xs text-muted-foreground">
            节点级默认 server host。各入站可在「入站」单独覆盖公网 IP/端口。
          </p>
        </div>
        <div className="flex flex-col space-y-1.5">
          <Label htmlFor="node-edit-public" className="text-muted-foreground">
            默认公网地址 / NAT IP
          </Label>
          <Input
            id="node-edit-public"
            value={editPublic}
            disabled={busy}
            onChange={(e) => {
              setEditPublic(e.target.value)
              clearConnectionError('publicAddress')
            }}
            placeholder={editControlMode === 'uplink' ? '客户端入口' : '与控制面相同或特定接入 IP/域名'}
          />
          {connectionErrors.publicAddress && (
            <p className="text-xs text-destructive font-medium">{connectionErrors.publicAddress}</p>
          )}
        </div>
      </div>

      {/* Outbound Egress */}
      <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5">
        <div className="space-y-1">
          <h3 className="text-sm font-semibold text-foreground">出口网卡选择</h3>
          <p className="text-xs text-muted-foreground">
            指定此节点代理流量出站时使用的具体物理/虚拟网络接口。
          </p>
        </div>
        <div className="flex flex-col space-y-1.5">
          <Label htmlFor="node-edit-egress" className="text-muted-foreground">
            出口接口
          </Label>
          <Select
            value={editEgress || EGRESS_DEFAULT_VALUE}
            onValueChange={(v) => setEditEgress(v === EGRESS_DEFAULT_VALUE ? '' : v)}
            disabled={busy}
          >
            <SelectTrigger id="node-edit-egress">
              <SelectValue placeholder="系统默认" />
            </SelectTrigger>
            <SelectContent className="bg-popover border-border">
              {egressOptions().map((opt) => (
                <SelectItem key={opt.value} value={opt.value}>
                  {opt.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      {/* Per-node DNS */}
      <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5">
        <div className="space-y-1">
          <h3 className="text-sm font-semibold text-foreground">节点 DNS</h3>
          <p className="text-xs text-muted-foreground">
            只影响这台节点解析下一跳主机名，以及路由到直连时解析用户目标。代理链中间跳把域名原样交给下一跳。
            保存后于下次下发生效。sing-box 1.12 没有 DNS 故障转移：同组两台不是互为备份，各自只接自己那类查询；一台不可达时，打到它的查询失败。
          </p>
        </div>
        {dnsGate === 'old' && (
          <p className="text-xs text-muted-foreground">
            当前上报的 sing-box 低于 1.12。这里的修改会保存，但要等核心升到 1.12 之后的下次下发才会写入节点配置。
          </p>
        )}
        {dnsGate === 'unknown' && (
          <p className="text-xs text-muted-foreground">
            节点还没上报 sing-box 版本。Panel 在确认不低于 1.12 之前不会下发这段 DNS。已升级的 Agent 会在配置里没有 dns 时自行补上同一组默认 DoH。
          </p>
        )}
        {androidNode && (
          <p className="text-xs text-muted-foreground">
            Android 节点不会把 DNS 或出站绑定到出口网卡。
          </p>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="flex flex-col space-y-1.5">
            <Label htmlFor="node-dns-strategy" className="text-muted-foreground">地址策略</Label>
            <Select
              value={editDNS.strategy || 'prefer_ipv4'}
              onValueChange={(value) => patchDNS({ strategy: value })}
              disabled={busy}
            >
              <SelectTrigger id="node-dns-strategy">
                <SelectValue />
              </SelectTrigger>
              <SelectContent className="bg-popover border-border">
                {DNS_STRATEGIES.map((item) => (
                  <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col space-y-1.5">
            <Label htmlFor="node-dns-subnet" className="text-muted-foreground">ECS client subnet</Label>
            <Input
              id="node-dns-subnet"
              value={editDNS.client_subnet || ''}
              disabled={busy}
              placeholder="留空不发送，例如 203.0.113.0/24"
              onChange={(e) => patchDNS({ client_subnet: e.target.value })}
            />
          </div>
        </div>
        <div className="flex flex-col gap-3">
          <label className="flex items-start gap-3 text-sm">
            <Checkbox
              checked={editDNS.sniff}
              disabled={busy}
              onCheckedChange={(checked) => patchDNS({ sniff: Boolean(checked) })}
            />
            <span>
              <span className="text-foreground">嗅探内层 TLS / HTTP / QUIC</span>
              <span className="block text-xs text-muted-foreground">协议已经带了域名时不必依赖嗅探。开启后域名规则才能命中「目标是 IP」的连接。</span>
            </span>
          </label>
          <label className="flex items-start gap-3 text-sm">
            <Checkbox
              checked={editDNS.sniff_override_destination}
              disabled={busy || !editDNS.sniff}
              onCheckedChange={(checked) => patchDNS({ sniff_override_destination: Boolean(checked) })}
            />
            <span>
              <span className="text-foreground">用嗅探到的域名改写目标</span>
              <span className="block text-xs text-muted-foreground">把 IP 目标改成 SNI / Host，域名路由计划才会命中。</span>
            </span>
          </label>
        </div>
        <div className="space-y-3">
          {DNS_SERVERS.map((meta) => {
            const server = editDNS.servers.find((item) => item.tag === meta.tag)
            if (!server) return null
            return (
              <div key={meta.tag} className="space-y-2 rounded-md border border-border bg-background p-3">
                <div>
                  <p className="text-sm font-medium text-foreground">{meta.title}</p>
                  <p className="text-xs text-muted-foreground">{meta.role}。标签 {meta.tag} 固定。</p>
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="flex flex-col space-y-1.5">
                    <Label htmlFor={`dns-${meta.tag}-server`} className="text-muted-foreground">服务器</Label>
                    <Input
                      id={`dns-${meta.tag}-server`}
                      value={server.server}
                      disabled={busy}
                      onChange={(e) => patchServer(meta.tag, { server: e.target.value })}
                    />
                  </div>
                  <div className="flex flex-col space-y-1.5">
                    <Label htmlFor={`dns-${meta.tag}-port`} className="text-muted-foreground">端口</Label>
                    <Input
                      id={`dns-${meta.tag}-port`}
                      type="number"
                      min={1}
                      max={65535}
                      value={server.server_port}
                      disabled={busy}
                      onChange={(e) => patchServer(meta.tag, { server_port: Number(e.target.value) })}
                    />
                  </div>
                  <div className="flex flex-col space-y-1.5">
                    <Label htmlFor={`dns-${meta.tag}-path`} className="text-muted-foreground">路径</Label>
                    <Input
                      id={`dns-${meta.tag}-path`}
                      value={server.path}
                      disabled={busy}
                      onChange={(e) => patchServer(meta.tag, { path: e.target.value })}
                    />
                  </div>
                  <div className="flex flex-col space-y-1.5">
                    <Label htmlFor={`dns-${meta.tag}-sni`} className="text-muted-foreground">TLS server name</Label>
                    <Input
                      id={`dns-${meta.tag}-sni`}
                      value={server.server_name}
                      disabled={busy}
                      onChange={(e) => patchServer(meta.tag, { server_name: e.target.value })}
                    />
                  </div>
                </div>
              </div>
            )
          })}
        </div>
        <p className="text-xs text-muted-foreground">
          默认地址是 IP，证书名用 server name，这样不需要先解析 DoH 域名，也可以绑到出口网卡。改成域名后，会用仍是 IP 的那一台做引导。geosite-cn 在节点启动时从 GitHub 下载，下载失败则 sing-box 起不来。
        </p>
      </div>

      {/* Save Button */}
      <div className="flex justify-end">
        <Button onClick={onSaveConnection} loading={busy}>
          保存连接设置
        </Button>
      </div>

      {(showInstall || showUpgrade) && installInfo && (
        <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5 pt-4">
          <div className="flex items-center justify-between gap-3">
            <h3 className="text-sm font-semibold text-foreground">
              {showInstall ? '一键安装' : '一键升级'}
            </h3>
            <div className="flex items-center gap-2">
              {showInstall ? (
                <Button size="sm" variant="outline" onClick={() => setShowPairingQR(true)} className="gap-1.5 text-xs">
                  <QrCode className="h-3.5 w-3.5" />
                  扫码配对
                </Button>
              ) : null}
              <Button
                size="sm"
                variant="outline"
                onClick={showInstall ? copyInstallCommand : copyUpgradeCommand}
                className="gap-1.5 text-xs"
              >
                {(showInstall ? copied : copiedUpgrade)
                  ? <Check className="h-3.5 w-3.5 text-success" />
                  : <Copy className="h-3.5 w-3.5" />}
                {(showInstall ? copied : copiedUpgrade) ? '已复制' : '复制命令'}
              </Button>
            </div>
          </div>
          <pre className="p-3 bg-muted border border-border rounded-md text-[11px] font-mono text-foreground leading-relaxed whitespace-pre-wrap break-all max-h-[160px] overflow-y-auto">
            {showInstall ? installInfo.install_command : installInfo.upgrade_command}
          </pre>
        </div>
      )}

      {showInstall && installInfo && (
        <NodePairingQRModal
          open={showPairingQR}
          onClose={() => setShowPairingQR(false)}
          nodeName={editName || installInfo.node.name}
          panelUrl={installInfo.panel_base_url || window.location.origin}
          nodeId={installInfo.node.id}
          token={installInfo.token}
        />
      )}
    </div>
  )
}
