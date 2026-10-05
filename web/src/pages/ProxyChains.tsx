import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { StatusBadge } from '@/components/ui/status-badge'
import { ConfirmModal } from '@/components/ui/confirm-modal'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import {
  ArrowDown,
  ArrowUp,
  CirclePlay,
  Edit,
  Gauge,
  Link2,
  Loader2,
  Plus,
  Power,
  RefreshCw,
  ShieldCheck,
  Trash2,
} from 'lucide-react'
import {
  createProxyChain,
  deleteProxyChain,
  disableProxyChain,
  enableProxyChain,
  listInbounds,
  listNodes,
  listProxyChains,
  probeProxyChain,
  updateProxyChain,
  type Node,
  type InboundConfig,
  type ProxyChain,
  type ProxyChainHop,
  type ProxyChainInput,
} from '../api/client'
import { formatTime } from '../lib/nodeDisplay'
import { toast } from '../lib/toast'

type HopDraft = Omit<ProxyChainHop, 'chain_id' | 'position'>

const EMPTY_HOP: HopDraft = {
  node_id: '',
  inbound_id: '',
  dial_address: '',
  dial_port: 0,
  tls_skip_verify: false,
}

export default function ProxyChains() {
  const [chains, setChains] = useState<ProxyChain[]>([])
  const [nodes, setNodes] = useState<Node[]>([])
  const [inbounds, setInbounds] = useState<InboundConfig[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [pending, setPending] = useState<Set<string>>(new Set())
  const [editor, setEditor] = useState<{
    open: boolean
    id: string | null
    name: string
    hops: HopDraft[]
  }>({ open: false, id: null, name: '', hops: [{ ...EMPTY_HOP }, { ...EMPTY_HOP }] })
  const [editorError, setEditorError] = useState('')
  const [saving, setSaving] = useState(false)

  const nodeByID = useMemo(
    () => new Map(nodes.map((node) => [node.id, node])),
    [nodes],
  )

  const inboundUsedByOtherChain = useMemo(() => {
    const used = new Set<string>()
    for (const chain of chains) {
      if (editor.id && chain.id === editor.id) continue
      for (const hop of chain.hops ?? []) {
        if (hop.inbound_id) used.add(hop.inbound_id)
      }
    }
    return used
  }, [chains, editor.id])

  function inboundOptionsForHop(currentInboundId: string) {
    const selectedHere = new Set(
      editor.hops.map((hop) => hop.inbound_id).filter((id) => id && id !== currentInboundId),
    )
    return inbounds.filter((inbound) => {
      if (inbound.id === currentInboundId) return true
      if (!inbound.enabled) return false
      if (inboundUsedByOtherChain.has(inbound.id) || selectedHere.has(inbound.id)) return false
      return true
    })
  }

  const load = useCallback(async () => {
    setLoading(true)
    setLoadError('')
    try {
      const [nextChains, nextNodes, nextInbounds] = await Promise.all([
        listProxyChains(),
        listNodes(),
        listInbounds(),
      ])
      setChains(nextChains ?? [])
      setNodes(nextNodes ?? [])
      setInbounds(nextInbounds ?? [])
    } catch (err) {
      setLoadError(errorText(err, '代理链加载失败'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  function openCreate() {
    setEditorError('')
    setEditor({
      open: true,
      id: null,
      name: '',
      hops: [{ ...EMPTY_HOP }, { ...EMPTY_HOP }],
    })
  }

  function openEdit(chain: ProxyChain) {
    setEditorError('')
    setEditor({
      open: true,
      id: chain.id,
      name: chain.name,
      hops: chain.hops.map((hop) => ({
        node_id: hop.node_id,
        inbound_id: hop.inbound_id,
        dial_address: hop.dial_address ?? '',
        dial_port: hop.dial_port ?? 0,
        tls_skip_verify: hop.tls_skip_verify ?? false,
      })),
    })
  }

  function updateHop(index: number, patch: Partial<HopDraft>) {
    setEditor((current) => ({
      ...current,
      hops: current.hops.map((hop, hopIndex) =>
        hopIndex === index ? { ...hop, ...patch } : hop,
      ),
    }))
  }

  function moveHop(index: number, direction: -1 | 1) {
    setEditor((current) => {
      const target = index + direction
      if (target < 0 || target >= current.hops.length) return current
      const hops = [...current.hops]
      ;[hops[index], hops[target]] = [hops[target], hops[index]]
      return { ...current, hops }
    })
  }

  function removeHop(index: number) {
    if (editor.hops.length <= 2) {
      setEditorError('代理链至少需要 2 跳')
      return
    }
    setEditor((current) => ({
      ...current,
      hops: current.hops.filter((_, hopIndex) => hopIndex !== index),
    }))
  }

  async function save() {
    const name = editor.name.trim()
    if (!name) {
      setEditorError('请填写代理链名称')
      return
    }
    if (editor.hops.length < 2 || editor.hops.length > 8) {
      setEditorError('代理链必须包含 2–8 跳')
      return
    }
    const incomplete = editor.hops.findIndex((hop) => !hop.node_id || !hop.inbound_id)
    if (incomplete >= 0) {
      setEditorError(`第 ${incomplete + 1} 跳需要选择节点和入站`)
      return
    }
    if (new Set(editor.hops.map((hop) => hop.node_id)).size !== editor.hops.length) {
      setEditorError('同一节点不能在一条代理链中重复出现')
      return
    }
    const invalidPort = editor.hops.findIndex(
      (hop) => hop.dial_port !== 0 && (hop.dial_port < 1 || hop.dial_port > 65535),
    )
    if (invalidPort >= 0) {
      setEditorError(`第 ${invalidPort + 1} 跳的覆盖端口必须在 1–65535 之间`)
      return
    }

    const body: ProxyChainInput = {
      name,
      hops: editor.hops.map((hop) => ({
        ...hop,
        dial_address: hop.dial_address.trim(),
        dial_port: Number(hop.dial_port) || 0,
      })),
    }
    setSaving(true)
    setEditorError('')
    try {
      const saved = editor.id
        ? await updateProxyChain(editor.id, body)
        : await createProxyChain(body)
      setChains((current) =>
        editor.id
          ? current.map((chain) => (chain.id === saved.id ? saved : chain))
          : [...current, saved],
      )
      toast.success(editor.id ? '代理链已更新并重新部署' : '代理链已创建')
      setEditor((current) => ({ ...current, open: false }))
    } catch (err) {
      setEditorError(errorText(err, '保存代理链失败'))
    } finally {
      setSaving(false)
    }
  }

  const [deleteTarget, setDeleteTarget] = useState<ProxyChain | null>(null)

  async function operate(chain: ProxyChain, action: 'enable' | 'disable' | 'probe' | 'delete') {
    if (pending.has(chain.id)) return
    if (action === 'delete') {
      setDeleteTarget(chain)
      return
    }
    setPending((current) => new Set(current).add(chain.id))
    try {
      if (action === 'probe') {
        const result = await probeProxyChain(chain.id)
        if (result.ok) {
          toast.success(`完整链路可用，延迟 ${result.delay_ms} ms`)
        } else {
          const hop = result.failed_hop_index >= 0 ? `，疑似第 ${result.failed_hop_index + 1} 跳` : ''
          toast.error(`完整链路探测失败${hop}：${result.message || '未知错误'}`)
        }
        await refreshChain(chain.id)
        return
      }
      const updated = action === 'enable'
        ? await enableProxyChain(chain.id)
        : await disableProxyChain(chain.id)
      setChains((current) => current.map((item) => (item.id === updated.id ? updated : item)))
      toast.success(action === 'enable' ? '代理链已部署并启用' : '代理链已停用')
    } catch (err) {
      toast.error(errorText(err, `${actionText(action)}失败`))
      await refreshChain(chain.id).catch(() => undefined)
    } finally {
      setPending((current) => {
        const next = new Set(current)
        next.delete(chain.id)
        return next
      })
    }
  }

  async function confirmDeleteChain() {
    if (!deleteTarget) return
    const chain = deleteTarget
    setDeleteTarget(null)
    setPending((current) => new Set(current).add(chain.id))
    try {
      await deleteProxyChain(chain.id)
      setChains((current) => current.filter((item) => item.id !== chain.id))
      toast.success('代理链已删除')
    } catch (err) {
      toast.error(errorText(err, '删除失败'))
    } finally {
      setPending((current) => {
        const next = new Set(current)
        next.delete(chain.id)
        return next
      })
    }
  }

  async function refreshChain(id: string) {
    const latest = await listProxyChains()
    setChains(latest ?? [])
    return latest.find((chain) => chain.id === id)
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between border-b border-border pb-5">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">服务端代理链</h1>
          <p className="text-sm text-muted-foreground mt-1">
            将 2–8 个 Agent 按顺序串联；客户端只连接入口节点，任一跳失败都会阻断流量
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => void load()} loading={loading} className="gap-1.5">
            <RefreshCw className="h-4 w-4" /> 刷新
          </Button>
          <Button onClick={openCreate} className="gap-1.5">
            <Plus className="h-4 w-4" /> 新建代理链
          </Button>
        </div>
      </div>

      {loadError && (
        <Alert variant="destructive">
          <AlertDescription>{loadError}</AlertDescription>
        </Alert>
      )}

      <Alert variant="warning">
        <ShieldCheck className="h-4 w-4" />
        <AlertDescription className="text-xs">
          启用前会检查所有 Agent 是否支持链式代理。部署按出口到入口逆序进行；失败时自动回滚已变更节点。
          部署成功但数据探测失败时保留配置并标记为“降级”。
        </AlertDescription>
      </Alert>

      {loading && chains.length === 0 ? (
        <div className="flex items-center justify-center py-20 gap-3 text-sm text-muted-foreground">
          <Loader2 className="h-5 w-5 animate-spin" /> 正在加载代理链…
        </div>
      ) : chains.length === 0 ? (
        <Card className="border-border bg-card">
          <CardContent className="py-16 text-center space-y-3">
            <Link2 className="h-10 w-10 text-muted-foreground mx-auto" />
            <h2 className="font-semibold text-foreground">暂无代理链</h2>
            <p className="text-xs text-muted-foreground">先为至少两个节点绑定入站，再创建第一条链。</p>
            <Button onClick={openCreate} className="gap-1.5"><Plus className="h-4 w-4" /> 新建代理链</Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
          {chains.map((chain) => {
            const busy = pending.has(chain.id)
            return (
              <Card key={chain.id} className="border-border bg-card">
                <CardContent className="p-5 space-y-4">
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <div className="flex items-center gap-2">
                        <h2 className="font-semibold text-foreground">{chain.name}</h2>
                        <StatusBadge value={chain.enabled ? chain.state : 'disabled'} />
                      </div>
                      <p className="text-[11px] text-muted-foreground mt-1 font-mono">{chain.hops.length} hops · {chain.id}</p>
                    </div>
                    <div className="flex gap-1">
                      <Button size="sm" variant="ghost" disabled={busy} onClick={() => openEdit(chain)} className="h-8 px-2">
                        <Edit className="h-3.5 w-3.5" />
                      </Button>
                      <Button size="sm" variant="ghost" disabled={busy || chain.enabled} onClick={() => void operate(chain, 'delete')} className="h-8 px-2 text-destructive hover:text-destructive/80">
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  </div>

                  <div className="flex flex-wrap items-center gap-1.5">
                    {chain.hops.map((hop, index) => (
                      <div key={`${hop.node_id}-${index}`} className="contents">
                        <div className="rounded-md border border-border bg-background px-2.5 py-2 min-w-[105px]">
                          <span className="text-[10px] text-muted-foreground block">第 {index + 1} 跳{index === 0 ? ' · 入口' : index === chain.hops.length - 1 ? ' · 出口' : ''}</span>
                          <span className="text-xs text-foreground font-medium block truncate max-w-[145px]">
                            {nodeByID.get(hop.node_id)?.name ?? hop.node_id}
                          </span>
                          <span className="text-[10px] text-muted-foreground font-mono block truncate max-w-[145px]">
                            {inboundName(inbounds, hop.inbound_id)}
                          </span>
                        </div>
                        {index < chain.hops.length - 1 && <ArrowDown className="h-3.5 w-3.5 text-muted-foreground -rotate-90" />}
                      </div>
                    ))}
                  </div>

                  {(chain.last_probe_unix > 0 || chain.last_probe_error) && (
                    <div className="rounded-md bg-background border border-border px-3 py-2 text-xs flex flex-wrap gap-x-4 gap-y-1">
                      <span className="text-muted-foreground flex items-center gap-1">
                        <Gauge className="h-3.5 w-3.5" />
                        {chain.last_probe_delay_ms > 0 ? `${chain.last_probe_delay_ms} ms` : '无延迟数据'}
                      </span>
                      <span className="text-muted-foreground">{formatTime(chain.last_probe_unix)}</span>
                      {chain.last_probe_error && (
                        <span className="basis-full text-destructive">
                          {chain.failed_hop_index >= 0 ? `疑似第 ${chain.failed_hop_index + 1} 跳：` : ''}
                          {chain.last_probe_error}
                        </span>
                      )}
                    </div>
                  )}

                  <div className="flex flex-wrap gap-2 pt-1 border-t border-border">
                    {chain.enabled ? (
                      <Button size="sm" variant="outline" loading={busy} disabled={busy} onClick={() => void operate(chain, 'disable')} className="h-8 gap-1.5">
                        <Power className="h-3.5 w-3.5" /> 停用
                      </Button>
                    ) : (
                      <Button size="sm" loading={busy} disabled={busy} onClick={() => void operate(chain, 'enable')} className="h-8 gap-1.5">
                        <CirclePlay className="h-3.5 w-3.5" /> 部署并启用
                      </Button>
                    )}
                    <Button size="sm" variant="outline" loading={busy} disabled={busy || !chain.enabled} onClick={() => void operate(chain, 'probe')} className="h-8 gap-1.5">
                      <Gauge className="h-3.5 w-3.5" /> 完整链路探测
                    </Button>
                  </div>
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}

      <Dialog open={editor.open} onOpenChange={(open) => setEditor((current) => ({ ...current, open }))}>
        <DialogContent className="sm:max-w-3xl max-h-[90vh] overflow-y-auto bg-background border-border text-foreground p-6 space-y-5">
          <DialogHeader>
            <DialogTitle>{editor.id ? '编辑代理链' : '新建代理链'}</DialogTitle>
          </DialogHeader>
          {editorError && <Alert variant="destructive"><AlertDescription>{editorError}</AlertDescription></Alert>}
          <div className="space-y-2">
            <Label htmlFor="chain-name">代理链名称</Label>
            <Input id="chain-name" value={editor.name} onChange={(event) => setEditor({ ...editor, name: event.target.value })} placeholder="例：上海入口 → 香港中继 → 日本出口" />
          </div>

          <div className="space-y-3">
            <Label>链路跳序 (Hops)</Label>
            <div className="space-y-3">
              {editor.hops.map((hop, index) => (
                <div key={index} className="rounded-lg border border-border bg-card p-4 space-y-3">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <Badge variant="outline" className="border-border">第 {index + 1} 跳</Badge>
                      <span className="text-xs text-muted-foreground">{index === 0 ? '客户端入口' : index === editor.hops.length - 1 ? '公网出口' : '中继节点'}</span>
                    </div>
                    <div className="flex items-center gap-1">
                      <Button type="button" size="sm" variant="ghost" disabled={index === 0} onClick={() => moveHop(index, -1)} className="h-7 w-7 p-0"><ArrowUp className="h-3.5 w-3.5" /></Button>
                      <Button type="button" size="sm" variant="ghost" disabled={index === editor.hops.length - 1} onClick={() => moveHop(index, 1)} className="h-7 w-7 p-0"><ArrowDown className="h-3.5 w-3.5" /></Button>
                      <Button type="button" size="sm" variant="ghost" disabled={editor.hops.length <= 2} onClick={() => removeHop(index)} className="h-7 w-7 p-0 text-destructive"><Trash2 className="h-3.5 w-3.5" /></Button>
                    </div>
                  </div>
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                    <div className="space-y-1">
                      <Label className="text-xs text-muted-foreground">节点</Label>
                      <select
                        value={hop.node_id}
                        onChange={(event) => updateHop(index, { node_id: event.target.value, inbound_id: '' })}
                        className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground"
                      >
                        <option value="">请选择节点…</option>
                        {nodes.map((node) => <option key={node.id} value={node.id}>{node.name}</option>)}
                      </select>
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs text-muted-foreground">链专用入站</Label>
                      <select
                        value={hop.inbound_id}
                        disabled={!hop.node_id}
                        onChange={(event) => updateHop(index, { inbound_id: event.target.value })}
                        className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground disabled:opacity-50"
                      >
                        <option value="">请选择链专用入站…</option>
                        {inboundOptionsForHop(hop.inbound_id).map((inbound) => (
                          <option key={inbound.id} value={inbound.id}>
                            {inbound.name} ({inbound.protocol.toUpperCase()}
                            {inbound.enabled ? '' : ' · 已禁用'})
                          </option>
                        ))}
                      </select>
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs text-muted-foreground">本跳接入地址（可选覆盖）</Label>
                      <Input value={hop.dial_address} onChange={(event) => updateHop(index, { dial_address: event.target.value })} placeholder="留空使用本节点公网地址" />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs text-muted-foreground">本跳接入端口（可选覆盖）</Label>
                      <Input type="number" min={0} max={65535} value={hop.dial_port || ''} onChange={(event) => updateHop(index, { dial_port: Number(event.target.value) || 0 })} placeholder="留空使用入站公网端口" />
                    </div>
                  </div>
                  <div className="flex items-center justify-between rounded-md border border-border bg-background px-3 py-2">
                    <div>
                      <Label htmlFor={`skip-tls-${index}`} className="text-xs text-foreground">连接本跳时跳过 TLS 证书校验</Label>
                      <p className="text-[10px] text-muted-foreground">默认关闭；仅用于自签名或测试证书</p>
                    </div>
                    <Switch id={`skip-tls-${index}`} checked={hop.tls_skip_verify} onCheckedChange={(value) => updateHop(index, { tls_skip_verify: value })} />
                  </div>
                </div>
              ))}
            </div>
            <Button type="button" variant="outline" disabled={editor.hops.length >= 8} onClick={() => setEditor((current) => ({ ...current, hops: [...current.hops, { ...EMPTY_HOP }] }))} className="w-full border-dashed border-border gap-1.5">
              <Plus className="h-4 w-4" /> 添加下一跳
            </Button>
          </div>

          <DialogFooter className="gap-2 sm:gap-0">
            <Button variant="outline" disabled={saving} onClick={() => setEditor((current) => ({ ...current, open: false }))} className="border-border">取消</Button>
            <Button loading={saving} onClick={() => void save()}>{editor.id ? '保存并按需重新部署' : '创建代理链'}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Proxy Chain Confirm Modal */}
      <ConfirmModal
        open={!!deleteTarget}
        title="删除代理链"
        description={`确定要删除代理链「${deleteTarget?.name || ''}」吗？此操作无法撤销。`}
        confirmText="确认删除"
        confirmVariant="destructive"
        onConfirm={() => void confirmDeleteChain()}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

function inboundName(inbounds: InboundConfig[] | undefined, id: string): string {
  return inbounds?.find((inbound) => inbound.id === id)?.name ?? id
}

function actionText(action: 'enable' | 'disable' | 'probe' | 'delete'): string {
  return ({ enable: '启用代理链', disable: '停用代理链', probe: '探测代理链', delete: '删除代理链' })[action]
}

function errorText(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback
}
