import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import { DEFAULT_NODE_DNS, updateNode, type Node, type NodeDNS, type NodeDNSServer } from '@/api/client'
import { toast } from '@/lib/toast'
import { ChevronDown, ChevronUp, Plus, Trash2 } from 'lucide-react'

const DNS_TYPES = [
  { value: 'udp', label: 'UDP' },
  { value: 'tcp', label: 'TCP' },
  { value: 'tls', label: 'TLS' },
  { value: 'https', label: 'HTTPS' },
  { value: 'quic', label: 'QUIC' },
  { value: 'h3', label: 'HTTP/3' },
] as const

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

function usesTLS(type?: string) {
  return type === 'tls' || type === 'https' || type === 'quic' || type === 'h3'
}

function usesPath(type?: string) {
  return type === 'https' || type === 'h3'
}

function defaultPort(type?: string) {
  if (type === 'udp' || type === 'tcp') return 53
  if (type === 'tls' || type === 'quic') return 853
  return 443
}

export function cloneNodeDNS(dns?: NodeDNS): NodeDNS {
  const source = dns && dns.servers?.length ? dns : DEFAULT_NODE_DNS
  return {
    ...DEFAULT_NODE_DNS,
    ...source,
    client_subnet: source.client_subnet || '',
    servers: source.servers.map((server) => ({
      tag: server.tag,
      type: server.type || 'https',
      server: server.server,
      server_port: server.server_port,
      path: server.path || '',
      server_name: server.server_name || '',
      enabled: server.enabled !== false,
    })),
  }
}

function blankServer(existing: NodeDNSServer[]): NodeDNSServer {
  const tags = new Set(existing.map((server) => server.tag))
  let n = existing.length + 1
  let tag = `dns-${n}`
  while (tags.has(tag)) {
    n += 1
    tag = `dns-${n}`
  }
  return {
    tag,
    type: 'https',
    server: '',
    server_port: 443,
    path: '/dns-query',
    server_name: '',
    enabled: true,
  }
}

export interface NodeDNSTabProps {
  node: Node
  onSaved: (node: Node) => void
  onDirtyChange: (dirty: boolean) => void
}

/** DNS for this agent. One registered agent is one node row, so this config is that agent's sing-box DNS. */
export function NodeDNSTab({ node, onSaved, onDirtyChange }: NodeDNSTabProps) {
  const [editDNS, setEditDNS] = useState<NodeDNS>(() => cloneNodeDNS(node.dns))
  const [busy, setBusy] = useState(false)
  const dnsGate = singboxDNSGate(node.singbox_version)
  const androidNode = (node.client_type || '').toLowerCase() === 'android'
  const dirty = JSON.stringify(editDNS) !== JSON.stringify(cloneNodeDNS(node.dns))
  const finalIndex = editDNS.servers.findIndex((server) => server.enabled !== false)

  useEffect(() => {
    setEditDNS(cloneNodeDNS(node.dns))
  }, [node.id])

  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])

  useEffect(() => () => onDirtyChange(false), [onDirtyChange])

  function patchDNS(patch: Partial<NodeDNS>) {
    setEditDNS((current) => ({ ...current, ...patch }))
  }

  function patchServers(servers: NodeDNSServer[]) {
    setEditDNS((current) => ({ ...current, servers }))
  }

  function patchServer(index: number, patch: Partial<NodeDNSServer>) {
    patchServers(editDNS.servers.map((server, i) => (i === index ? { ...server, ...patch } : server)))
  }

  function moveServer(index: number, direction: -1 | 1) {
    const next = index + direction
    if (next < 0 || next >= editDNS.servers.length) return
    const servers = editDNS.servers.slice()
    const [row] = servers.splice(index, 1)
    servers.splice(next, 0, row)
    patchServers(servers)
  }

  function changeType(index: number, type: string) {
    const current = editDNS.servers[index]
    patchServer(index, {
      type,
      server_port: defaultPort(type),
      path: usesPath(type) ? current.path || '/dns-query' : '',
      server_name: usesTLS(type) ? current.server_name : '',
    })
  }

  async function onSave() {
    if (busy) return
    if (editDNS.servers.length === 0) {
      toast.warning('至少保留一台 DNS 服务器')
      return
    }
    if (finalIndex < 0) {
      toast.warning('至少启用一台 DNS 服务器作为 final')
      return
    }
    setBusy(true)
    try {
      const updated = await updateNode(node.id, { dns: editDNS })
      setEditDNS(cloneNodeDNS(updated.dns))
      onSaved(updated)
      toast.success('DNS 设置保存成功')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '保存失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="space-y-1">
          <h3 className="text-sm font-semibold text-foreground">DNS</h3>
          <p className="text-xs text-muted-foreground">
            这台 Agent 的 sing-box DNS。服务器按列表顺序保存。第一台启用的是 final：节点上的查询和代理链下一跳主机名都只走它。
            sing-box 1.12 没有故障转移，也没有健康检查，所以不会自动改用后面的服务器。后面已启用的行会写进配置，但不会接到查询；要换上游，把它启用并移到第一行。停用的行只留在这里，不会下发。
            保存后于下次下发生效。
          </p>
        </div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={() => patchServers(DEFAULT_NODE_DNS.servers.map((server) => ({ ...server })))}
        >
          恢复默认
        </Button>
      </div>
      {dnsGate === 'old' && (
        <p className="text-xs text-muted-foreground">
          当前上报的 sing-box 低于 1.12。这里的修改会保存，但要等核心升到 1.12 之后的下次下发才会写入节点配置。
        </p>
      )}
      {dnsGate === 'unknown' && (
        <p className="text-xs text-muted-foreground">
          这台 Agent 还没上报 sing-box 版本。Panel 在确认不低于 1.12 之前不会下发这段 DNS。已升级的 Agent 会在配置里没有 dns 时自行补上同一组默认 DoH，并把第一台当作 final。
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
        {editDNS.servers.map((server, index) => {
          const type = server.type || 'https'
          const isFinal = index === finalIndex
          return (
            <div key={`${index}-${server.tag}`} className="space-y-3 rounded-md border border-border bg-background p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <span className="text-xs text-muted-foreground">#{index + 1}</span>
                  {isFinal ? <Badge variant="secondary">final</Badge> : null}
                  {server.enabled === false ? <Badge variant="outline">已停用</Badge> : null}
                </div>
                <div className="flex items-center gap-1">
                  <div className="mr-2 flex items-center gap-2">
                    <Label htmlFor={`dns-${index}-enabled`} className="text-xs text-muted-foreground">启用</Label>
                    <Switch
                      id={`dns-${index}-enabled`}
                      checked={server.enabled !== false}
                      disabled={busy}
                      onCheckedChange={(checked) => patchServer(index, { enabled: checked })}
                    />
                  </div>
                  <Button type="button" size="icon" variant="ghost" className="h-8 w-8" disabled={busy || index === 0} onClick={() => moveServer(index, -1)} aria-label="上移">
                    <ChevronUp className="h-4 w-4" />
                  </Button>
                  <Button type="button" size="icon" variant="ghost" className="h-8 w-8" disabled={busy || index === editDNS.servers.length - 1} onClick={() => moveServer(index, 1)} aria-label="下移">
                    <ChevronDown className="h-4 w-4" />
                  </Button>
                  <Button type="button" size="icon" variant="ghost" className="h-8 w-8" disabled={busy} onClick={() => patchServers(editDNS.servers.filter((_, i) => i !== index))} aria-label="删除">
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="flex flex-col space-y-1.5">
                  <Label htmlFor={`dns-${index}-tag`} className="text-muted-foreground">名称</Label>
                  <Input id={`dns-${index}-tag`} value={server.tag} disabled={busy} onChange={(e) => patchServer(index, { tag: e.target.value })} />
                </div>
                <div className="flex flex-col space-y-1.5">
                  <Label htmlFor={`dns-${index}-type`} className="text-muted-foreground">类型</Label>
                  <Select value={type} onValueChange={(value) => changeType(index, value)} disabled={busy}>
                    <SelectTrigger id={`dns-${index}-type`}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent className="bg-popover border-border">
                      {DNS_TYPES.map((item) => (
                        <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="flex flex-col space-y-1.5">
                  <Label htmlFor={`dns-${index}-server`} className="text-muted-foreground">地址</Label>
                  <Input id={`dns-${index}-server`} value={server.server} disabled={busy} placeholder="IP 或主机名" onChange={(e) => patchServer(index, { server: e.target.value })} />
                </div>
                <div className="flex flex-col space-y-1.5">
                  <Label htmlFor={`dns-${index}-port`} className="text-muted-foreground">端口</Label>
                  <Input
                    id={`dns-${index}-port`}
                    type="number"
                    min={1}
                    max={65535}
                    value={server.server_port}
                    disabled={busy}
                    onChange={(e) => patchServer(index, { server_port: Number(e.target.value) })}
                  />
                </div>
                {usesPath(type) ? (
                  <div className="flex flex-col space-y-1.5">
                    <Label htmlFor={`dns-${index}-path`} className="text-muted-foreground">路径</Label>
                    <Input id={`dns-${index}-path`} value={server.path || ''} disabled={busy} onChange={(e) => patchServer(index, { path: e.target.value })} />
                  </div>
                ) : null}
                {usesTLS(type) ? (
                  <div className="flex flex-col space-y-1.5">
                    <Label htmlFor={`dns-${index}-sni`} className="text-muted-foreground">SNI / server name</Label>
                    <Input id={`dns-${index}-sni`} value={server.server_name || ''} disabled={busy} onChange={(e) => patchServer(index, { server_name: e.target.value })} />
                  </div>
                ) : null}
              </div>
            </div>
          )
        })}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Button type="button" size="sm" variant="outline" disabled={busy} onClick={() => patchServers([...editDNS.servers, blankServer(editDNS.servers)])} className="gap-1.5">
          <Plus className="h-3.5 w-3.5" /> 添加服务器
        </Button>
        <Button onClick={() => void onSave()} loading={busy}>
          保存 DNS
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        默认四台都是 DoH，地址是 IP。恢复默认后第一台是 AliDNS，所以 final 是它。地址改成主机名时，列表里还要有一台启用的 IP 服务器做引导。没有做健康探测。
      </p>
    </div>
  )
}
