import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { DEFAULT_NODE_DNS, updateNode, type Node, type NodeDNS } from '@/api/client'
import { toast } from '@/lib/toast'

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

export function cloneNodeDNS(dns?: NodeDNS): NodeDNS {
  const source = dns && dns.servers?.length ? dns : DEFAULT_NODE_DNS
  return {
    ...DEFAULT_NODE_DNS,
    ...source,
    client_subnet: source.client_subnet || '',
    servers: DEFAULT_NODE_DNS.servers.map((item) => {
      const found = source.servers.find((server) => server.tag === item.tag)
      return { ...item, ...found }
    }),
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

  function patchServer(tag: string, patch: Partial<NodeDNS['servers'][number]>) {
    setEditDNS((current) => ({
      ...current,
      servers: current.servers.map((server) => (server.tag === tag ? { ...server, ...patch } : server)),
    }))
  }

  async function onSave() {
    if (busy) return
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
      <div className="space-y-1">
        <h3 className="text-sm font-semibold text-foreground">DNS</h3>
        <p className="text-xs text-muted-foreground">
          这台 Agent 的 sing-box DNS。一台已注册的 Agent 对应一条节点，这里的服务器列表只作用于它，并用于解析下一跳主机名，以及路由到直连时解析用户目标。代理链中间跳把域名原样交给下一跳。
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
          这台 Agent 还没上报 sing-box 版本。Panel 在确认不低于 1.12 之前不会下发这段 DNS。已升级的 Agent 会在配置里没有 dns 时自行补上同一组默认 DoH。
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
      <div className="flex justify-end">
        <Button onClick={() => void onSave()} loading={busy}>
          保存 DNS
        </Button>
      </div>
    </div>
  )
}
