import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Plus,
  X,
  UploadCloud,
  Filter,
  Play,
  RefreshCw,
  Square,
  List,
  Grid,
  Server,
  ArrowUp,
  ArrowDown,
} from 'lucide-react'
import {
  ApiError,
  applyNode,
  batchApply,
  batchStart,
  batchStop,
  deleteNode,
  fleetOverview,
  fleetRefresh,
  getMeta,
  getNodeInstallCommand,
  upgradeNode,
  probeNode,
  startNode,
  stopNode,
  type FleetOverview,
  type MetaInfo,
  type Node,
  type NodeInstallInfo,
  type Task,
} from '../api/client'
import AddNodeModal from '../components/AddNodeModal'
import NodeDetailDrawer from '../components/NodeDetailDrawer'
import StatsBar from '../components/StatsBar'
import { ConfirmModal } from '@/components/ui/confirm-modal'
import { StatusBadge } from '@/components/ui/status-badge'
import {
  formatBytes,
  formatTime,
  clientVersionLine,
  coreVersion,
  isAgentOutdated,
  linuxReleaseUpgradeAllowed,
  isOnlineStatus,
  nodeEndpointLabel,
  runtimeLabel,
  runtimeTheme,
  statusLabel,
  statusTheme,
  taskKindLabel,
  taskStatusLabel,
} from '../lib/nodeDisplay'
import { copyText } from '../lib/clipboard'
import { toast } from '../lib/toast'

type ViewMode = 'cards' | 'table'
type NodeAction = 'apply' | 'start' | 'stop' | 'probe' | 'install' | 'upgrade' | 'delete'
const EMPTY_NODES: Node[] = []
const METRICS_MAX_AGE_SECONDS = 120

function hasFreshMetrics(node: Node): boolean {
  const sampledAt = Number(node.metrics_at_unix) || 0
  if (!isOnlineStatus(node.status) || sampledAt <= 0) return false
  return Math.floor(Date.now() / 1000) - sampledAt <= METRICS_MAX_AGE_SECONDS
}

export default function Fleet() {
  const [searchParams, setSearchParams] = useSearchParams()
  const detailId = searchParams.get('node')

  const [ov, setOv] = useState<FleetOverview | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [refreshing, setRefreshing] = useState(false)
  const [batchBusy, setBatchBusy] = useState(false)
  const [auto, setAuto] = useState(true)
  const [view, setView] = useState<ViewMode>(() => {
    const v = localStorage.getItem('fleet-view')
    return v === 'table' ? 'table' : 'cards'
  })
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [labelFilter, setLabelFilter] = useState('')
  const [lastTask, setLastTask] = useState<Task | null>(null)
  const [activeActions, setActiveActions] = useState<Record<string, NodeAction>>({})
  const [addOpen, setAddOpen] = useState(false)
  const [installBanner, setInstallBanner] = useState<NodeInstallInfo | null>(null)
  const [upgradeBanner, setUpgradeBanner] = useState<{
    node: Node
    command: string
    recommended?: string
  } | null>(null)
  const [meta, setMeta] = useState<MetaInfo | null>(null)
  const [installCopied, setInstallCopied] = useState(false)
  const [upgradeCopied, setUpgradeCopied] = useState(false)
  const cachedRequestRef = useRef(0)
  const liveGenerationRef = useRef(0)
  const refreshRunningRef = useRef<Promise<void> | null>(null)
  const probeTimersRef = useRef<number[]>([])
  const pendingTimersRef = useRef<number[]>([])
  const activeActionsRef = useRef<Record<string, NodeAction>>({})
  const installBannerRequestRef = useRef(0)
  const upgradeBannerRequestRef = useRef(0)
  const installCopyVersionRef = useRef(0)
  const upgradeCopyVersionRef = useRef(0)

  function beginNodeAction(id: string, kind: NodeAction): boolean {
    if (activeActionsRef.current[id]) return false
    const next = { ...activeActionsRef.current, [id]: kind }
    activeActionsRef.current = next
    setActiveActions(next)
    return true
  }

  function finishNodeAction(id: string, kind: NodeAction) {
    if (activeActionsRef.current[id] !== kind) return
    const next = { ...activeActionsRef.current }
    delete next[id]
    activeActionsRef.current = next
    setActiveActions(next)
  }

  const loadCached = useCallback(async () => {
    while (refreshRunningRef.current) await refreshRunningRef.current
    const requestVersion = ++cachedRequestRef.current
    const liveGeneration = liveGenerationRef.current
    setLoading(true)
    setLoadError('')
    try {
      const data = await fleetOverview()
      if (
        requestVersion === cachedRequestRef.current &&
        liveGeneration === liveGenerationRef.current
      ) {
        setOv(data)
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : '加载失败'
      if (
        requestVersion === cachedRequestRef.current &&
        liveGeneration === liveGenerationRef.current
      ) {
        setLoadError(message)
      }
    } finally {
      if (
        requestVersion === cachedRequestRef.current &&
        liveGeneration === liveGenerationRef.current
      ) {
        setLoading(false)
      }
    }
  }, [])

  const refreshLive = useCallback(async (opts?: { silent?: boolean; skipIfRunning?: boolean }) => {
    const silent = opts?.silent === true
    if (!silent) setRefreshing(true)

    while (refreshRunningRef.current) {
      if (opts?.skipIfRunning) {
        if (!silent) setRefreshing(false)
        return
      }
      await refreshRunningRef.current
    }

    const liveGeneration = ++liveGenerationRef.current
    const run = (async () => {
      try {
        const data = await fleetRefresh()
        if (liveGeneration === liveGenerationRef.current) {
          setOv(data)
          setLoadError('')
        }
        if (!silent) {
          toast.success(`已刷新 ${data.total_nodes} 个节点 · ${formatTime(data.refreshed_at)}`)
        }
      } catch (err) {
        const message = err instanceof Error ? err.message : '刷新失败'
        if (liveGeneration === liveGenerationRef.current) setLoadError(message)
        if (!silent) {
          toast.error(message)
        }
      } finally {
        if (liveGeneration === liveGenerationRef.current) setLoading(false)
        if (!silent) setRefreshing(false)
      }
    })()
    refreshRunningRef.current = run
    try {
      await run
    } finally {
      if (refreshRunningRef.current === run) refreshRunningRef.current = null
    }
  }, [])

  useEffect(() => {
    void loadCached()
  }, [loadCached])

  useEffect(() => {
    void getMeta()
      .then(setMeta)
      .catch(() => {
        /* offline / no release info is fine */
      })
  }, [])

  useEffect(() => {
    if (!auto) return
    const t = window.setInterval(() => {
      void refreshLive({ silent: true, skipIfRunning: true })
    }, 15000)
    return () => window.clearInterval(t)
  }, [auto, refreshLive])

  useEffect(() => {
    return () => {
      for (const timer of probeTimersRef.current) window.clearTimeout(timer)
      for (const timer of pendingTimersRef.current) window.clearTimeout(timer)
    }
  }, [])

  function setViewMode(mode: ViewMode) {
    setView(mode)
    localStorage.setItem('fleet-view', mode)
  }

  const allNodes = ov?.nodes ?? EMPTY_NODES

  useEffect(() => {
    const existing = new Set(allNodes.map((node) => node.id))
    setSelected((previous) => {
      const next = new Set(Array.from(previous).filter((id) => existing.has(id)))
      return next.size === previous.size ? previous : next
    })
  }, [allNodes])

  const nodes = useMemo(() => {
    const q = labelFilter.trim().toLowerCase()
    if (!q) return allNodes
    return allNodes.filter((n) => (n.labels ?? []).some((l) => l.toLowerCase().includes(q)))
  }, [allNodes, labelFilter])

  const visibleAllSelected = nodes.length > 0 && nodes.every((n) => selected.has(n.id))

  function openDetail(id: string) {
    const next = new URLSearchParams(searchParams)
    next.set('node', id)
    setSearchParams(next, { replace: false })
  }

  function closeDetail() {
    const next = new URLSearchParams(searchParams)
    next.delete('node')
    setSearchParams(next, { replace: true })
  }

  function toggle(id: string) {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function toggleAllVisible() {
    if (nodes.length === 0) return
    const allSelected = nodes.every((n) => selected.has(n.id))
    setSelected((prev) => {
      const next = new Set(prev)
      if (allSelected) {
        for (const n of nodes) next.delete(n.id)
      } else {
        for (const n of nodes) next.add(n.id)
      }
      return next
    })
  }

  async function runBatch(kind: 'apply' | 'start' | 'stop') {
    if (batchBusy || Object.keys(activeActionsRef.current).length > 0) return
    const ids = Array.from(selected)
    if (ids.length === 0) {
      toast.warning('请先勾选节点')
      return
    }
    setBatchBusy(true)
    try {
      const fn = kind === 'apply' ? batchApply : kind === 'start' ? batchStart : batchStop
      const task = await fn({ node_ids: ids })
      setLastTask(task)
      const ok = task.results?.filter((r) => r.ok).length ?? 0
      const total = task.results?.length ?? 0
      const message = `批量${kind === 'apply' ? '下发配置（核心已自动拉起）' : kind === 'start' ? '启动' : '停止'}完成：${ok}/${total} 成功（${taskStatusLabel(task.status)}）`
      if (total > 0 && ok === total) toast.success(message)
      else if (ok > 0) toast.warning(message)
      else toast.error(message)
      await refreshLive({ silent: true })
      if (kind === 'apply' || kind === 'start') {
        pendingTimersRef.current.push(window.setTimeout(() => void refreshLive({ silent: true }), 1500))
        pendingTimersRef.current.push(window.setTimeout(() => void refreshLive({ silent: true }), 3500))
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '批量操作失败')
    } finally {
      setBatchBusy(false)
    }
  }

  async function runOne(id: string, kind: 'apply' | 'start' | 'stop') {
    if (!beginNodeAction(id, kind)) return
    try {
      const task =
        kind === 'apply'
          ? await applyNode(id)
          : kind === 'start'
            ? await startNode(id)
            : await stopNode(id)
      setLastTask(task)
      const ok = task.results?.[0]?.ok
      const text = `${kind === 'apply' ? '下发' : kind === 'start' ? '启动' : '停止'} ${ok ? '成功' : '失败'}: ${task.results?.[0]?.message || taskStatusLabel(task.status)}`
      if (ok) toast.success(text)
      else toast.error(text)
      await refreshLive({ silent: true })
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '操作失败')
    } finally {
      finishNodeAction(id, kind)
    }
  }

  async function onProbe(id: string, silent = false) {
    if (!beginNodeAction(id, 'probe')) return
    try {
      const r = await probeNode(id)
      if (!silent) {
        toast.success(`探测成功: Agent = ${r.agent_version}, sing-box = ${r.singbox_version}, 状态 = ${statusLabel(r.node.status)}`)
      }
      await loadCached()
    } catch (err) {
      if (!silent) {
        if (err instanceof ApiError) toast.error(err.message)
        else toast.error(err instanceof Error ? err.message : '探测失败')
      }
      await loadCached()
    } finally {
      finishNodeAction(id, 'probe')
    }
  }

  const [deleteTarget, setDeleteTarget] = useState<{ id: string; name: string } | null>(null)

  function onRequestDelete(id: string, name: string) {
    setDeleteTarget({ id, name })
  }

  function confirmDeleteNode() {
    if (!deleteTarget) return
    const { id, name } = deleteTarget
    setDeleteTarget(null)

    if (!beginNodeAction(id, 'delete')) return
    void (async () => {
      try {
        await deleteNode(id)
        if (detailId === id) closeDetail()
        if (installBanner?.node.id === id) setInstallBanner(null)
        setSelected((prev) => {
          const next = new Set(prev)
          next.delete(id)
          return next
        })
        await loadCached()
        toast.success(`已删除节点「${name}」`)
      } catch (err) {
        toast.error(err instanceof Error ? err.message : '删除失败')
      } finally {
        finishNodeAction(id, 'delete')
      }
    })()
  }

  async function showInstall(id: string) {
    if (!beginNodeAction(id, 'install')) return
    const request = ++installBannerRequestRef.current
    upgradeBannerRequestRef.current += 1
    installCopyVersionRef.current += 1
    setInstallCopied(false)
    setUpgradeBanner(null)
    setUpgradeCopied(false)
    try {
      const info = await getNodeInstallCommand(id)
      if (request !== installBannerRequestRef.current) return
      setInstallBanner(info)
      toast.success(`已生成节点「${info.node.name}」的安装命令`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '获取安装命令失败')
    } finally {
      finishNodeAction(id, 'install')
    }
  }

  async function showUpgrade(id: string) {
    const target = (allNodes ?? []).find((x) => x.id === id)
    if (target && !linuxReleaseUpgradeAllowed(target.client_type)) {
      toast.error('该节点是 Android 或 OpenWrt 客户端，不能使用 Linux 发布包远程升级')
      return
    }
    if (!beginNodeAction(id, 'upgrade')) return
    const request = ++upgradeBannerRequestRef.current
    installBannerRequestRef.current += 1
    upgradeCopyVersionRef.current += 1
    setUpgradeCopied(false)
    setInstallBanner(null)
    setInstallCopied(false)
    const version = meta?.recommended_agent_version || 'latest'
    try {
      const res = await upgradeNode(id, { version })
      if (!res.ok) {
        toast.error(res.message || '远程升级失败')
        try {
          const info = await getNodeInstallCommand(id, { version })
          const cmd = info.upgrade_command || meta?.agent_upgrade_command || ''
          if (cmd && request === upgradeBannerRequestRef.current) {
            setUpgradeBanner({
              node: info.node,
              command: cmd,
              recommended: info.recommended_agent_version || version,
            })
            toast.warning('已回退为手动升级命令（节点可能尚未安装升级助手）')
          }
        } catch {
          /* ignore */
        }
        return
      }
      toast.success(
        res.message
          ? `已触发远程升级 → ${res.version || version}：${res.message}`
          : `已触发远程升级 → ${res.version || version}`
      )
      probeTimersRef.current.push(window.setTimeout(() => {
        void onProbe(id, true)
      }, 4000))
      probeTimersRef.current.push(window.setTimeout(() => {
        void onProbe(id, true)
      }, 10000))
    } catch (err) {
      const msg = err instanceof Error ? err.message : '远程升级失败'
      toast.error(msg)
      try {
        const info = await getNodeInstallCommand(id, { version })
        const cmd = info.upgrade_command || meta?.agent_upgrade_command || ''
        if (cmd && request === upgradeBannerRequestRef.current) {
          const n = (allNodes ?? []).find((x) => x.id === id)
          setUpgradeBanner({
            node: info.node || n!,
            command: cmd,
            recommended: info.recommended_agent_version || version,
          })
          toast.warning('远程升级不可用，已生成手动升级命令')
        }
      } catch {
        /* ignore */
      }
    } finally {
      finishNodeAction(id, 'upgrade')
    }
  }

  async function copyInstallBanner() {
    if (!installBanner?.install_command) return
    const version = ++installCopyVersionRef.current
    try {
      await copyText(installBanner.install_command)
      if (version !== installCopyVersionRef.current) return
      setInstallCopied(true)
      toast.success('已复制安装命令')
      pendingTimersRef.current.push(window.setTimeout(() => {
        if (version === installCopyVersionRef.current) setInstallCopied(false)
      }, 2000))
    } catch {
      toast.error('复制失败')
    }
  }

  async function copyUpgradeBanner() {
    if (!upgradeBanner?.command) return
    const version = ++upgradeCopyVersionRef.current
    try {
      await copyText(upgradeBanner.command)
      if (version !== upgradeCopyVersionRef.current) return
      setUpgradeCopied(true)
      toast.success('已复制升级命令')
      pendingTimersRef.current.push(window.setTimeout(() => {
        if (version === upgradeCopyVersionRef.current) setUpgradeCopied(false)
      }, 2000))
    } catch {
      toast.error('复制失败')
    }
  }

  const recommended = meta?.recommended_agent_version || ''
  const outdatedNodes = useMemo(() => {
    if (!recommended) return [] as Node[]
    return allNodes.filter(
      (n) => n.status !== 'pending' && isAgentOutdated(coreVersion(n), recommended),
    )
  }, [allNodes, recommended])

  const busy = refreshing || batchBusy || Object.keys(activeActions).length > 0

  return (
    <div className="space-y-6">
      {/* Page Header */}
      <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">节点列表</h1>
          <p className="text-sm text-muted-foreground mt-1 leading-normal flex flex-wrap items-center gap-x-2 gap-y-1">
            <span>状态监控 · 批量运维 · 安装与配置</span>
            {meta?.panel_version && (
              <>
                <span className="text-muted-foreground">•</span>
                <span>Panel <code className="font-mono text-muted-foreground">{meta.panel_version}</code></span>
              </>
            )}
            {recommended && (
              <>
                <span className="text-muted-foreground">•</span>
                <span>推荐 Agent <code className="font-mono text-muted-foreground">{recommended}</code></span>
                {outdatedNodes.length > 0 ? (
                  <StatusBadge value="outdated" label={`${outdatedNodes.length} 个节点核心可升级`} />
                ) : (
                  <StatusBadge value="success" label="版本均已对齐" />
                )}
              </>
            )}
          </p>
        </div>

        {/* Global Toolbar */}
        <div className="flex flex-wrap items-center gap-3 shrink-0">
          <div className="flex border border-border rounded-md bg-secondary p-0.5">
            <button
              onClick={() => setViewMode('cards')}
              className={`flex items-center gap-1.5 px-3 py-1 rounded text-xs font-medium transition-colors cursor-pointer ${
                view === 'cards' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'
              }`}
            >
              <Grid className="h-3.5 w-3.5" /> 卡片
            </button>
            <button
              onClick={() => setViewMode('table')}
              className={`flex items-center gap-1.5 px-3 py-1 rounded text-xs font-medium transition-colors cursor-pointer ${
                view === 'table' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'
              }`}
            >
              <List className="h-3.5 w-3.5" /> 表格
            </button>
          </div>

          <div className="flex items-center gap-2 border border-border bg-card rounded-md px-3 py-1.5 text-xs text-muted-foreground">
            <Switch
              id="auto-refresh"
              checked={auto}
              onCheckedChange={(v) => setAuto(Boolean(v))}
            />
            <label htmlFor="auto-refresh" className="cursor-pointer font-medium select-none">
              15s 自动刷新
            </label>
          </div>

          <Button
            variant="outline"
            onClick={() => void refreshLive()}
            loading={refreshing}
            disabled={batchBusy}
            className="gap-1.5 h-9"
          >
            <RefreshCw className="h-4 w-4" /> 探测全部
          </Button>
          <Button onClick={() => setAddOpen(true)} className="gap-1.5 h-9">
            <Plus className="h-4 w-4" /> 添加节点
          </Button>
        </div>
      </div>

      {loadError ? (
        <Alert variant={ov ? 'warning' : 'destructive'}>
          <AlertTitle>{ov ? '使用本地缓存' : '数据加载失败'}</AlertTitle>
          <AlertDescription>
            {ov ? '当前显示最近缓存；状态实时刷新失败：' : '节点数据加载失败，详细错误：'}
            {loadError}
          </AlertDescription>
        </Alert>
      ) : null}

      <StatsBar ov={ov} loading={loading} unavailable={!ov && Boolean(loadError)} />

      {/* Manual upgrade command banner */}
      {upgradeBanner && (
        <Card className="border-warning/30 bg-warning/10 relative">
          <CardHeader className="p-5 pb-2 flex flex-row items-center justify-between">
            <CardTitle className="text-sm font-semibold text-warning-foreground">
              手动升级命令 · {upgradeBanner.node.name}
              {upgradeBanner.recommended ? ` (→ ${upgradeBanner.recommended})` : ''}
            </CardTitle>
            <div className="flex items-center gap-2 shrink-0">
              <Button
                size="sm"
                variant="outline"
                onClick={() => void copyUpgradeBanner()}
                className="h-8 border-warning/30 text-warning-foreground hover:bg-warning/20"
              >
                {upgradeCopied ? '已复制' : '复制命令'}
              </Button>
              <Button
                size="icon"
                variant="ghost"
                onClick={() => {
                  upgradeCopyVersionRef.current += 1
                  setUpgradeBanner(null)
                  setUpgradeCopied(false)
                }}
                className="h-8 w-8 text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </Button>
            </div>
          </CardHeader>
          <CardContent className="p-5 pt-1 space-y-3">
            <p className="text-xs text-muted-foreground leading-normal">
              当前核心版本为 <code className="font-mono text-foreground">{coreVersion(upgradeBanner.node) || '—'}</code>。在目标服务器以 root 用户执行以下升级命令（会自动保留现有的 Token、TLS 和业务配置）：
            </p>
            <pre className="p-3 bg-muted border border-border rounded-md overflow-x-auto text-[10px] font-mono text-foreground">
              {upgradeBanner.command}
            </pre>
          </CardContent>
        </Card>
      )}

      {/* Install command banner */}
      {installBanner && (
        <Card className="border-border bg-card relative">
          <CardHeader className="p-5 pb-2 flex flex-row items-center justify-between">
            <CardTitle className="text-sm font-semibold">安装命令 · {installBanner.node.name}</CardTitle>
            <div className="flex items-center gap-2 shrink-0">
              <Button
                size="sm"
                variant="outline"
                onClick={() => void copyInstallBanner()}
                className="h-8 border-border text-foreground hover:bg-muted"
              >
                {installCopied ? '已复制' : '复制命令'}
              </Button>
              <Button
                size="icon"
                variant="ghost"
                onClick={() => {
                  installCopyVersionRef.current += 1
                  setInstallBanner(null)
                  setInstallCopied(false)
                }}
                className="h-8 w-8 text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </Button>
            </div>
          </CardHeader>
          <CardContent className="p-5 pt-1 space-y-3">
            <pre className="p-3 bg-muted border border-border rounded-md text-[10px] font-mono text-foreground whitespace-pre-wrap break-all max-h-[160px] overflow-y-auto">
              {installBanner.install_command}
            </pre>
            {installBanner.upgrade_command && (
              <div className="space-y-1 mt-2">
                <span className="text-xs text-muted-foreground block">已装机环境的快速升级命令：</span>
                <pre className="p-3 bg-muted border border-border rounded-md text-[10px] font-mono text-foreground whitespace-pre-wrap break-all max-h-[160px] overflow-y-auto">
                  {installBanner.upgrade_command}
                </pre>
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {/* Main List Container */}
      <Card className="border-border bg-card">
        <CardContent className="p-5">
          {/* Sub Toolbar */}
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between mb-4 pb-4 border-b border-border">
            <div className="flex flex-1 items-center gap-2 max-w-sm">
              <div className="relative flex-1">
                <Filter className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
                <Input
                  value={labelFilter}
                  onChange={(e) => setLabelFilter(e.target.value)}
                  placeholder="按标签筛选..."
                  className="pl-9 bg-background border-border"
                />
              </div>
              <Button
                size="sm"
                variant="outline"
                onClick={toggleAllVisible}
                disabled={nodes.length === 0}
                className="border-border text-muted-foreground hover:bg-muted shrink-0"
              >
                {visibleAllSelected ? '取消全选' : '全选可见'}
              </Button>
            </div>

            {/* Batch actions container */}
            <div className="flex items-center gap-3 bg-background border border-border px-3 py-1.5 rounded-md text-xs shrink-0">
              <span className="text-muted-foreground font-medium">已选择 <strong className="text-foreground">{selected.size}</strong> 个节点</span>
              <div className="h-3 w-px bg-border" />
              <div className="flex items-center gap-1.5">
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={busy || selected.size === 0}
                  onClick={() => void runBatch('apply')}
                  className="h-7 text-muted-foreground hover:text-foreground hover:bg-muted gap-1"
                >
                  <UploadCloud className="h-3.5 w-3.5" /> 下发配置
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={busy || selected.size === 0}
                  onClick={() => void runBatch('start')}
                  className="h-7 text-muted-foreground hover:text-foreground hover:bg-muted gap-1"
                >
                  <Play className="h-3.5 w-3.5 text-success" /> 启动
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={busy || selected.size === 0}
                  onClick={() => void runBatch('stop')}
                  className="h-7 text-muted-foreground hover:text-destructive hover:bg-muted gap-1"
                >
                  <Square className="h-3.5 w-3.5 text-destructive" /> 停止
                </Button>
              </div>
            </div>
          </div>

          {/* Table / Grid loader states */}
          {loading && !ov ? (
            <div className="flex flex-col items-center justify-center py-12 space-y-2">
              <RefreshCw className="h-6 w-6 animate-spin text-zinc-500" />
              <span className="text-sm text-zinc-400">正在获取节点列表数据…</span>
            </div>
          ) : !ov && loadError ? (
            <div className="flex flex-col items-center justify-center py-12 space-y-4">
              <span className="text-sm font-semibold text-muted-foreground">节点列表获取失败</span>
              <Button variant="outline" onClick={() => void loadCached()}>
                <RefreshCw className="h-4 w-4" /> 重试加载
              </Button>
            </div>
          ) : nodes.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-12 space-y-4 text-center">
              {allNodes.length === 0 ? (
                <>
                  <Server className="h-8 w-8 text-muted-foreground" />
                  <div className="space-y-1">
                    <span className="text-sm font-medium text-foreground">暂无节点数据</span>
                    <p className="text-xs text-muted-foreground">点击下方按钮，开始添加您的第一个管控节点。</p>
                  </div>
                  <Button onClick={() => setAddOpen(true)} className="gap-1.5 mt-2">
                    <Plus className="h-4 w-4" /> 添加节点
                  </Button>
                </>
              ) : (
                <>
                  <Filter className="h-8 w-8 text-muted-foreground" />
                  <span className="text-sm text-muted-foreground">没有匹配标签的节点</span>
                </>
              )}
            </div>
          ) : view === 'cards' ? (
            /* Redesigned Premium Cards Grid */
            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
              {nodes.map((n) => (
                <NodeCard
                  key={n.id}
                  node={n}
                  recommended={recommended}
                  checked={selected.has(n.id)}
                  activeAction={activeActions[n.id] ?? null}
                  globallyDisabled={batchBusy}
                  onToggle={() => toggle(n.id)}
                  onDetail={() => openDetail(n.id)}
                  onApply={() => void runOne(n.id, 'apply')}
                  onStart={() => void runOne(n.id, 'start')}
                  onStop={() => void runOne(n.id, 'stop')}
                  onProbe={() => void onProbe(n.id)}
                  onInstall={() => void showInstall(n.id)}
                  onUpgrade={() => void showUpgrade(n.id)}
                  onDelete={() => onRequestDelete(n.id, n.name)}
                />
              ))}
            </div>
          ) : (
            /* Redesigned Premium Table view */
            <div className="overflow-x-auto">
              <Table>
                <TableHeader className="bg-muted/40 border-border">
                  <TableRow className="hover:bg-transparent">
                    <TableHead className="w-12 text-center">
                      <Checkbox
                        checked={visibleAllSelected}
                        onCheckedChange={toggleAllVisible}
                        aria-label="选择全部"
                      />
                    </TableHead>
                    <TableHead className="min-w-[120px]">名称</TableHead>
                    <TableHead className="min-w-[180px]">控制面 / 订阅入口</TableHead>
                    <TableHead className="w-24">连通</TableHead>
                    <TableHead className="w-24">核心</TableHead>
                    <TableHead className="w-16 text-center">入站</TableHead>
                    <TableHead className="w-16 text-center">连接</TableHead>
                    <TableHead className="w-24">吞吐</TableHead>
                    <TableHead className="w-24">内存</TableHead>
                    <TableHead className="min-w-[140px]">标签</TableHead>
                    <TableHead className="w-28">最后活跃</TableHead>
                    <TableHead className="min-w-[140px]">版本</TableHead>
                    <TableHead className="w-[180px] text-right">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody className="divide-y divide-zinc-900 border-zinc-900">
                  {nodes.map((n) => {
                    const fresh = hasFreshMetrics(n)
                    const outdated = n.status !== 'pending' && isAgentOutdated(coreVersion(n), recommended)
                    const wrapper = clientVersionLine(n)
                    const canRemoteUpgrade = linuxReleaseUpgradeAllowed(n.client_type)
                    const actionBusy = batchBusy || activeActions[n.id] !== undefined
                    return (
                      <TableRow key={n.id} className="hover:bg-muted/50 border-border">
                        <TableCell className="text-center">
                          <Checkbox
                            checked={selected.has(n.id)}
                            onCheckedChange={() => toggle(n.id)}
                            aria-label={`选择节点 ${n.name}`}
                          />
                        </TableCell>
                        <TableCell>
                          <button
                            type="button"
                            onClick={() => openDetail(n.id)}
                            className="font-semibold text-foreground hover:underline text-left cursor-pointer"
                          >
                            {n.name}
                          </button>
                          {n.control_mode === 'uplink' ? (
                            <div className="mt-1">
                              {n.uplink_ws_connected ? (
                                <Badge variant="outline" className="text-[10px] border-success/40 text-success">
                                  WS 实时通道
                                </Badge>
                              ) : (
                                <Badge variant="outline" className="text-[10px] text-muted-foreground">
                                  上行离线 · HTTP 回退
                                </Badge>
                              )}
                            </div>
                          ) : null}
                        </TableCell>
                        <TableCell className="space-y-1">
                          <code className="text-xs font-mono text-foreground block">
                            {nodeEndpointLabel(n)}
                          </code>
                          {n.control_mode !== 'uplink' && n.public_address && n.public_address !== n.address && (
                            <span className="text-[10px] text-muted-foreground font-mono flex items-center gap-1">
                              订阅: <code className="text-[10px] text-muted-foreground">{n.public_address}</code>
                            </span>
                          )}
                        </TableCell>
                        <TableCell>
                          <Badge variant={statusTheme(n.status)}>{statusLabel(n.status)}</Badge>
                        </TableCell>
                        <TableCell>
                          <Badge variant={runtimeTheme(n.runtime_state)}>{runtimeLabel(n.runtime_state)}</Badge>
                        </TableCell>
                        <TableCell className="text-center text-foreground font-medium">
                          {n.inbound_count ?? 0}
                        </TableCell>
                        <TableCell className="text-center text-foreground font-mono">
                          {fresh ? (n.connections ?? 0) : '—'}
                        </TableCell>
                        <TableCell className="text-muted-foreground font-mono text-xs space-y-0.5">
                          {fresh ? (
                            <>
                              <div className="flex items-center text-[10px] text-emerald-500">
                                <ArrowUp className="h-3 w-3 shrink-0" /> {formatBytes(n.uplink_bytes)}
                              </div>
                              <div className="flex items-center text-[10px] text-blue-500">
                                <ArrowDown className="h-3 w-3 shrink-0" /> {formatBytes(n.downlink_bytes)}
                              </div>
                            </>
                          ) : '—'}
                        </TableCell>
                        <TableCell className="text-foreground font-mono text-xs">
                          {fresh ? formatBytes(n.memory_rss_bytes) : '—'}
                        </TableCell>
                        <TableCell>
                          <div className="flex flex-wrap gap-1">
                            {(n.labels ?? []).map((l) => (
                              <Badge key={l} variant="outline" className="text-[10px] border-border text-muted-foreground px-1.5 py-0">
                                {l}
                              </Badge>
                            ))}
                          </div>
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground font-mono">
                          {formatTime(n.last_seen_unix || n.metrics_at_unix)}
                        </TableCell>
                        <TableCell className="space-y-0.5">
                          <code className="text-xs font-mono text-foreground block">{coreVersion(n) || '—'}</code>
                          {wrapper ? (
                            <span className="text-[10px] font-mono text-muted-foreground block">{wrapper}</span>
                          ) : null}
                          {outdated ? (
                            <StatusBadge value="outdated" />
                          ) : coreVersion(n) ? (
                            <StatusBadge value="success" label="已最新" />
                          ) : null}
                        </TableCell>
                        <TableCell className="text-right">
                          <div className="flex items-center justify-end gap-1">
                            <Button size="sm" variant="ghost" className="h-8 px-2 text-xs text-muted-foreground hover:text-foreground cursor-pointer" onClick={() => openDetail(n.id)}>
                              详情
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              className="h-8 px-2 text-xs text-muted-foreground hover:text-foreground cursor-pointer"
                              loading={activeActions[n.id] === 'probe'}
                              disabled={actionBusy}
                              onClick={() => void onProbe(n.id)}
                            >
                              探测
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              className="h-8 px-2 text-xs text-muted-foreground hover:text-foreground cursor-pointer"
                              loading={activeActions[n.id] === 'install'}
                              disabled={actionBusy}
                              onClick={() => void showInstall(n.id)}
                            >
                              安装
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              className={`h-8 px-2 text-xs cursor-pointer ${
                                outdated
                                  ? 'text-amber-500 hover:text-amber-400 hover:bg-amber-950/20'
                                  : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-900'
                              }`}
                              loading={activeActions[n.id] === 'upgrade'}
                              title={
                                !canRemoteUpgrade
                                  ? '该客户端不能使用 Linux 发布包远程升级'
                                  : outdated
                                    ? '远程升级核心到最新推荐版本'
                                    : `当前核心已是最新版本 (${coreVersion(n) || recommended})，点击可重新推送升级指令`
                              }
                              disabled={actionBusy || !canRemoteUpgrade}
                              onClick={() => void showUpgrade(n.id)}
                            >
                              {outdated ? '升级' : '重升'}
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              className="h-8 px-2 text-xs text-red-500 hover:text-red-400 hover:bg-red-950/20 cursor-pointer"
                              loading={activeActions[n.id] === 'delete'}
                              disabled={actionBusy}
                              onClick={() => onRequestDelete(n.id, n.name)}
                            >
                              删除
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      {/* Task Logger */}
      {lastTask && (
        <Card className="border-zinc-900 bg-zinc-900/30">
          <CardHeader className="p-5 pb-2">
            <CardTitle className="text-sm font-semibold">
              最近任务：{taskKindLabel(lastTask.type)} ({taskStatusLabel(lastTask.status)})
            </CardTitle>
            <p className="text-[10px] text-zinc-500 font-mono">{lastTask.id}</p>
          </CardHeader>
          <CardContent className="p-5 pt-0">
            <div className="overflow-x-auto">
              <Table>
                <TableHeader className="bg-zinc-900/30 border-zinc-800">
                  <TableRow className="hover:bg-transparent">
                    <TableHead>节点 ID</TableHead>
                    <TableHead className="w-24">结果</TableHead>
                    <TableHead>返回信息</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody className="divide-y divide-zinc-950">
                  {(lastTask.results ?? []).map((row, idx) => (
                    <TableRow key={idx} className="hover:bg-zinc-900/20 border-zinc-900/40">
                      <TableCell>
                        <button
                          type="button"
                          onClick={() => openDetail(row.node_id)}
                          className="font-mono text-xs text-primary hover:underline cursor-pointer"
                        >
                          {row.node_id.slice(0, 8)}…
                        </button>
                      </TableCell>
                      <TableCell>
                        <Badge variant={row.ok ? 'success' : 'destructive'}>
                          {row.ok ? '成功' : '失败'}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-zinc-400 text-xs font-mono">
                        {row.message}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          </CardContent>
        </Card>
      )}

      {/* Add node Dialog */}
      <AddNodeModal
        open={addOpen}
        onClose={() => setAddOpen(false)}
        onCreated={() => void loadCached()}
        onOpenDetail={openDetail}
      />

      {/* Node Detail Modal */}
      <NodeDetailDrawer
        nodeId={detailId}
        onClose={closeDetail}
        onChanged={() => void loadCached()}
      />

      {/* Delete Node Confirmation Modal */}
      <ConfirmModal
        open={!!deleteTarget}
        title="删除节点"
        description={`确定要删除节点「${deleteTarget?.name || ''}」吗？此操作无法撤销。`}
        confirmText="确认删除"
        confirmVariant="destructive"
        onConfirm={confirmDeleteNode}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

function NodeCard({
  node: n,
  recommended,
  checked,
  activeAction,
  globallyDisabled,
  onToggle,
  onDetail,
  onApply,
  onStart,
  onStop,
  onProbe,
  onInstall,
  onUpgrade,
  onDelete,
}: {
  node: Node
  recommended?: string
  checked: boolean
  activeAction: NodeAction | null
  globallyDisabled: boolean
  onToggle: () => void
  onDetail: () => void
  onApply: () => void
  onStart: () => void
  onStop: () => void
  onProbe: () => void
  onInstall: () => void
  onUpgrade: () => void
  onDelete: () => void
}) {
  const online = isOnlineStatus(n.status)
  const outdated = n.status !== 'pending' && isAgentOutdated(coreVersion(n), recommended)
  const wrapper = clientVersionLine(n)
  const canRemoteUpgrade = linuxReleaseUpgradeAllowed(n.client_type)
  const hasMetrics = hasFreshMetrics(n)
  
  const statusGlow = online
    ? 'shadow-emerald-950/20 border-emerald-950/30 bg-emerald-950/5'
    : n.status === 'unreachable' || n.status === 'unauthorized'
      ? 'shadow-red-950/20 border-red-950/30 bg-red-950/5'
      : n.status === 'pending'
        ? 'shadow-zinc-950/20 border-zinc-800 bg-zinc-900/10'
        : 'shadow-zinc-950/20 border-zinc-900 bg-zinc-950/20'

  const actionBusy = globallyDisabled || activeAction !== null

  return (
    <Card className={`relative overflow-hidden transition-all duration-300 hover:translate-y-[-2px] ${statusGlow} ${checked ? 'border-zinc-500 shadow-md shadow-zinc-950/50' : ''}`}>
      <CardContent className="p-5 space-y-4">
        {/* Card Header row */}
        <div className="flex items-start justify-between gap-3">
          <div className="flex items-center space-x-2.5">
            <Checkbox
              id={`chk-${n.id}`}
              checked={checked}
              onCheckedChange={onToggle}
            />
            <button
              type="button"
              onClick={onDetail}
              className="text-sm font-bold text-zinc-100 hover:text-primary hover:underline transition-colors text-left cursor-pointer"
            >
              {n.name}
            </button>
            {n.control_mode === 'uplink' ? (
              <Badge variant="outline" className="text-[10px]">HTTP 上行</Badge>
            ) : null}
          </div>
          <div className="flex items-center gap-1 shrink-0">
            <Badge variant={statusTheme(n.status)}>{statusLabel(n.status)}</Badge>
            <Badge variant={runtimeTheme(n.runtime_state)}>{runtimeLabel(n.runtime_state)}</Badge>
          </div>
        </div>

        {/* Address and metadata */}
        <div className="space-y-1 text-xs">
          <code className="font-mono text-zinc-300 block">
            {nodeEndpointLabel(n)}
          </code>
          {n.control_mode !== 'uplink' && n.public_address && n.public_address !== n.address && (
            <div className="text-[10px] text-zinc-500 font-mono">
              订阅: <code className="text-zinc-400">{n.public_address}</code>
            </div>
          )}
          {n.port_mappings && n.port_mappings.length > 0 && (
            <div className="text-[10px] text-zinc-500">
              遗留端口映射 {n.port_mappings.length}
            </div>
          )}
        </div>

        {/* Labels row */}
        {(n.labels?.length ?? 0) > 0 && (
          <div className="flex flex-wrap gap-1">
            {n.labels.map((l) => (
              <Badge key={l} variant="outline" className="text-[10px] border-zinc-800 text-zinc-400 px-2 py-0">
                {l}
              </Badge>
            ))}
          </div>
        )}

        {/* Metrics Grid */}
        <div className="grid grid-cols-3 gap-2 py-2.5 border-y border-zinc-900 bg-zinc-950/10 rounded px-1.5 text-center">
          <div className="space-y-1">
            <span className="text-[10px] text-zinc-500 block">入站 / 连接</span>
            <span className="text-xs font-semibold text-zinc-300 font-mono block">
              {n.inbound_count ?? 0} / {hasMetrics ? (n.connections ?? 0) : '—'}
            </span>
          </div>
          <div className="space-y-1">
            <span className="text-[10px] text-zinc-500 block">吞吐 (上传/下载)</span>
            <span className="text-xs font-semibold text-zinc-300 font-mono block">
              {hasMetrics ? `${formatBytes(n.uplink_bytes)} / ${formatBytes(n.downlink_bytes)}` : '—'}
            </span>
          </div>
          <div className="space-y-1">
            <span className="text-[10px] text-zinc-500 block">内存 / CPU</span>
            <span className="text-xs font-semibold text-zinc-300 font-mono block">
              {hasMetrics ? `${formatBytes(n.memory_rss_bytes)} / ${(n.cpu_percent ?? 0).toFixed(1)}%` : '—'}
            </span>
          </div>
        </div>

        {/* Bottom Agent Version information */}
        <div className="text-[10px] text-zinc-500 flex flex-wrap justify-between items-center gap-2 pt-1">
          <div className="flex flex-col gap-0.5">
            <span className="flex items-center gap-1">
              <span>核心 {coreVersion(n) || '—'}</span>
              {outdated && <StatusBadge value="outdated" />}
            </span>
            {wrapper ? <span className="font-mono">{wrapper}</span> : null}
          </div>
          <span>探测 {formatTime(n.last_seen_unix || n.metrics_at_unix)}</span>
        </div>

        {/* Error notification block if exists */}
        {n.last_error && (
          <div className="p-2 rounded bg-red-950/20 border border-red-900/30 text-[10px] font-mono text-red-400 break-all leading-normal">
            {n.last_error}
          </div>
        )}

        {/* Actions panel */}
        <div className="grid grid-cols-4 gap-1.5 pt-2.5 border-t border-border">
          <Button
            size="sm"
            variant="outline"
            onClick={onDetail}
            className="h-8 text-xs"
          >
            详情
          </Button>
          <Button
            size="sm"
            variant="outline"
            loading={activeAction === 'apply'}
            disabled={actionBusy && activeAction !== 'apply'}
            onClick={onApply}
            className="h-8 text-xs"
          >
            下发
          </Button>
          <Button
            size="sm"
            variant="outline"
            loading={activeAction === 'start'}
            disabled={actionBusy && activeAction !== 'start'}
            onClick={onStart}
            className="h-8 text-xs"
          >
            启动
          </Button>
          <Button
            size="sm"
            variant="outline"
            loading={activeAction === 'stop'}
            disabled={actionBusy && activeAction !== 'stop'}
            onClick={onStop}
            className="h-8 text-xs"
          >
            停止
          </Button>
          <Button
            size="sm"
            variant="outline"
            loading={activeAction === 'probe'}
            disabled={actionBusy && activeAction !== 'probe'}
            onClick={onProbe}
            className="h-8 text-xs"
          >
            探测
          </Button>
          <Button
            size="sm"
            variant="outline"
            loading={activeAction === 'install'}
            disabled={actionBusy && activeAction !== 'install'}
            onClick={onInstall}
            className="h-8 text-xs"
          >
            安装
          </Button>
          <Button
            size="sm"
            variant="outline"
            loading={activeAction === 'upgrade'}
            onClick={onUpgrade}
            title={
              !canRemoteUpgrade
                ? '该客户端不能使用 Linux 发布包远程升级'
                : outdated
                  ? '远程升级核心到最新推荐版本'
                  : `当前核心已是最新版本 (${coreVersion(n) || recommended})，点击可重新推送升级指令`
            }
            disabled={(actionBusy && activeAction !== 'upgrade') || !canRemoteUpgrade}
            className={`h-8 text-xs ${
              outdated ? 'text-amber-500 hover:text-amber-400 border-amber-500/30' : 'text-muted-foreground'
            }`}
          >
            {outdated ? '升级' : '重升'}
          </Button>
          <Button
            size="sm"
            variant="outline"
            loading={activeAction === 'delete'}
            disabled={actionBusy && activeAction !== 'delete'}
            onClick={onDelete}
            className="h-8 text-xs text-destructive hover:bg-destructive/10 border-destructive/30"
          >
            删除
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
