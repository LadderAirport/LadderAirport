import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Badge } from '@/components/ui/badge'
import { DEFAULT_NODE_DNS, updateNode, type Node, type NodeDNS, type NodeDNSServer } from '@/api/client'
import { toast } from '@/lib/toast'
import { ChevronDown, ChevronUp, Plus, Trash2 } from 'lucide-react'

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

export function cloneNodeDNS(dns?: NodeDNS): NodeDNS {
  const source = dns && dns.servers?.length ? dns : DEFAULT_NODE_DNS
  return {
    ...DEFAULT_NODE_DNS,
    ...source,
    client_subnet: source.client_subnet || '',
    servers: source.servers.map((server) => ({
      name: server.name,
      url: server.url,
    })),
  }
}

function blankServer(existing: NodeDNSServer[]): NodeDNSServer {
  const names = new Set(existing.map((server) => server.name))
  let n = existing.length + 1
  let name = `dns-${n}`
  while (names.has(name) || name === 'dns-system') {
    n += 1
    name = `dns-${n}`
  }
  return { name, url: '' }
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

  async function onSave() {
    if (busy) return
    if (editDNS.servers.length === 0) {
      toast.warning('至少保留一台 DNS 服务器')
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
            这台 Agent 的 sing-box DNS。每行只有名称和 URL。第一行是 final：节点上的查询和代理链下一跳主机名都只走它。
            sing-box 1.12 没有故障转移，也没有健康检查，所以不会自动改用后面的行。后面的行会写进配置，但不会接到查询；要换上游，把它移到第一行。
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
          这台 Agent 还没上报 sing-box 版本。Panel 在确认不低于 1.12 之前不会下发这段 DNS。已升级的 Agent 会在配置里没有 dns 时自行补上同一组默认 DoH，并把第一行当作 final。
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
        {editDNS.servers.map((server, index) => (
          <div key={`${index}-${server.name}`} className="space-y-3 rounded-md border border-border bg-background p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex items-center gap-2">
                <span className="text-xs text-muted-foreground">#{index + 1}</span>
                {index === 0 ? <Badge variant="secondary">final</Badge> : null}
              </div>
              <div className="flex items-center gap-1">
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
            <div className="grid gap-3 sm:grid-cols-[minmax(8rem,12rem)_minmax(0,1fr)]">
              <div className="flex flex-col space-y-1.5">
                <Label htmlFor={`dns-${index}-name`} className="text-muted-foreground">名称</Label>
                <Input id={`dns-${index}-name`} value={server.name} disabled={busy} onChange={(e) => patchServer(index, { name: e.target.value })} />
              </div>
              <div className="flex flex-col space-y-1.5">
                <Label htmlFor={`dns-${index}-url`} className="text-muted-foreground">URL</Label>
                <Input
                  id={`dns-${index}-url`}
                  value={server.url}
                  disabled={busy}
                  placeholder="https://1.1.1.1/dns-query"
                  spellCheck={false}
                  onChange={(e) => patchServer(index, { url: e.target.value })}
                />
              </div>
            </div>
          </div>
        ))}
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
        URL 写成 udp://IP[:端口]、tcp://IP[:端口]、tls://主机[:端口]、https://主机[:端口]/路径、quic://主机[:端口] 或 h3://主机[:端口]/路径。只写 IP 时按 UDP。保存时按行校验，错误会指出第几行。
        主机名会用列表里第一台 IP 做引导解析；一台 IP 都没有时，生成配置会追加本机系统解析器 dns-system。默认四台是带 IP 的 DoH，证书包含该 IP，不需要引导，也可以绑定出口网卡。没有健康探测。
      </p>
    </div>
  )
}
