import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Badge } from '@/components/ui/badge'
import { StatusBadge } from '@/components/ui/status-badge'
import { ConfirmModal } from '@/components/ui/confirm-modal'
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from '@/components/ui/tabs'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import {
  Plus,
  Power,
  RefreshCw,
  Edit,
  Copy,
  Check,
  Eye,
  RotateCw,
  Trash2,
  QrCode,
  ExternalLink,
  Rss,
  Globe,
  Radio,
  Layers,
  Server,
} from 'lucide-react'
import {
  createExternalSource,
  createSubscription,
  deleteExternalSource,
  deleteSubscription,
  disableSubscription,
  enableSubscription,
  getSettings,
  listExternalSources,
  listInbounds,
  listProxyChains,
  listRoutePlans,
  listSubscriptions,
  previewExternalSource,
  previewSubscription,
  refreshExternalSource,
  rotateSubscriptionToken,
  updateExternalSource,
  updateSubscription,
  type ExternalSource,
  type InboundConfig,
  type ProxyChain,
  type RoutePlan,
  type Subscription,
} from '../api/client'
import { copyText } from '../lib/clipboard'
import { formatTime } from '../lib/nodeDisplay'
import { toast } from '../lib/toast'
import { QRCodeModal } from '../components/QRCodeModal'

type LocalMode = 'all' | 'custom' | 'none'
type NumericDraft = number | string

type SubscriptionEditor = {
  open: boolean
  id: string | null
  name: string
  enabled: boolean
  localMode: LocalMode
  inboundIds: Set<string>
  includeAllChains: boolean
  chainIds: Set<string>
  sourceIds: Set<string>
  routePlanId: string
}

type SourceEditor = {
  open: boolean
  id: string | null
  name: string
  url: string
  interval: NumericDraft
  headers: string
  enabled: boolean
}

type SourcePreview = Awaited<ReturnType<typeof previewExternalSource>>

const EMPTY_SUB_EDITOR: SubscriptionEditor = {
  open: false,
  id: null,
  name: '',
  enabled: true,
  localMode: 'none',
  inboundIds: new Set(),
  includeAllChains: true,
  chainIds: new Set(),
  sourceIds: new Set(),
  routePlanId: '',
}

const EMPTY_SOURCE_EDITOR: SourceEditor = {
  open: false,
  id: null,
  name: '',
  url: '',
  interval: 86400,
  headers: '',
  enabled: true,
}

export default function Subscriptions() {
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([])
  const [inbounds, setInbounds] = useState<InboundConfig[]>([])
  const [sources, setSources] = useState<ExternalSource[]>([])
  const [chains, setChains] = useState<ProxyChain[]>([])
  const [routePlans, setRoutePlans] = useState<RoutePlan[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [pending, setPending] = useState<Set<string>>(new Set())
  const [saving, setSaving] = useState(false)
  const [editorError, setEditorError] = useState('')
  const [subEditor, setSubEditor] = useState<SubscriptionEditor>(EMPTY_SUB_EDITOR)
  const [sourceEditor, setSourceEditor] = useState<SourceEditor>(EMPTY_SOURCE_EDITOR)
  const [textPreview, setTextPreview] = useState<{ subId: string; subName: string; format: string; text: string } | null>(null)
  const [sourcePreview, setSourcePreview] = useState<SourcePreview | null>(null)
  const [sourcePreviewTitle, setSourcePreviewTitle] = useState('')
  const [copiedId, setCopiedId] = useState<string | null>(null)
  
  // QR Code Modal state
  const [qrModal, setQrModal] = useState<{ open: boolean; title: string; url: string }>({
    open: false,
    title: '',
    url: '',
  })

  const loadVersion = useRef(0)
  const pendingRef = useRef<Set<string>>(new Set())
  const savingRef = useRef(false)
  const copyTimerRef = useRef<number | undefined>(undefined)
  const [publicBaseUrl, setPublicBaseUrl] = useState('')

  const load = useCallback(async () => {
    if (savingRef.current || pendingRef.current.size > 0) return
    const version = ++loadVersion.current
    setLoading(true)
    setLoadError('')
    const results = await Promise.allSettled([
      listSubscriptions(),
      listInbounds(),
      listExternalSources(),
      listProxyChains(),
      listRoutePlans(),
    ])
    if (version !== loadVersion.current) return

    const errors: string[] = []
    if (results[0].status === 'fulfilled') setSubscriptions(results[0].value ?? [])
    else errors.push(errorText(results[0].reason, '订阅列表加载失败'))
    if (results[1].status === 'fulfilled') setInbounds(results[1].value ?? [])
    else errors.push(errorText(results[1].reason, '入站列表加载失败'))
    if (results[2].status === 'fulfilled') setSources(results[2].value ?? [])
    else errors.push(errorText(results[2].reason, '外部源列表加载失败'))
    if (results[3].status === 'fulfilled') setChains(results[3].value ?? [])
    else errors.push(errorText(results[3].reason, '代理链列表加载失败'))
    if (results[4].status === 'fulfilled') setRoutePlans(results[4].value ?? [])
    else errors.push(errorText(results[4].reason, '路由计划列表加载失败'))
    setLoadError(errors.join('；'))
    setLoading(false)
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    let cancelled = false
    getSettings()
      .then((settings) => {
        if (!cancelled) setPublicBaseUrl((settings.public_base_url || '').replace(/\/+$/, ''))
      })
      .catch(() => {
        // Fall back to window.location.origin when settings are unavailable.
      })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    return () => {
      if (copyTimerRef.current !== undefined) window.clearTimeout(copyTimerRef.current)
    }
  }, [])

  function beginOperation(key: string): boolean {
    const separator = key.lastIndexOf(':')
    const namespace = key.startsWith('subscription-') ? 'subscription-' : 'source-'
    const entityID = separator >= 0 ? key.slice(separator + 1) : ''
    if (
      pendingRef.current.has(key) ||
      Array.from(pendingRef.current).some(
        (candidate) => candidate.startsWith(namespace) && candidate.endsWith(`:${entityID}`),
      )
    ) return false
    const next = new Set(pendingRef.current)
    next.add(key)
    pendingRef.current = next
    setPending(next)
    loadVersion.current += 1
    setLoading(false)
    return true
  }

  function endOperation(key: string) {
    const next = new Set(pendingRef.current)
    next.delete(key)
    pendingRef.current = next
    setPending(next)
  }

  function beginSaving(): boolean {
    if (savingRef.current) return false
    savingRef.current = true
    loadVersion.current += 1
    setLoading(false)
    setSaving(true)
    return true
  }

  function endSaving() {
    savingRef.current = false
    setSaving(false)
  }

  function entityOperationPending(namespace: 'subscription' | 'source', id: string): boolean {
    return Array.from(pending).some(
      (key) => key.startsWith(`${namespace}-`) && key.endsWith(`:${id}`),
    )
  }

  function openCreateSubscription() {
    setEditorError('')
    setSubEditor({
      ...EMPTY_SUB_EDITOR,
      open: true,
      inboundIds: new Set(),
      chainIds: new Set(),
      sourceIds: new Set(),
    })
  }

  function openEditSubscription(subscription: Subscription) {
    setEditorError('')
    const includeStandalone = subscription.include_standalone ?? true
    const includeAll = subscription.include_all_inbounds ?? subscription.inbound_ids.length === 0
    setSubEditor({
      open: true,
      id: subscription.id,
      name: subscription.name,
      enabled: subscription.enabled,
      localMode: !includeStandalone
        ? 'none'
        : includeAll
          ? 'all'
          : 'custom',
      inboundIds: new Set(subscription.inbound_ids ?? []),
      includeAllChains: subscription.include_all_chains ?? false,
      chainIds: new Set(subscription.chain_ids ?? []),
      sourceIds: new Set(subscription.external_source_ids ?? []),
      routePlanId: subscription.route_plan_id ?? '',
    })
  }

  function closeSubscriptionEditor() {
    if (!saving) setSubEditor({
      ...EMPTY_SUB_EDITOR,
      inboundIds: new Set(),
      chainIds: new Set(),
      sourceIds: new Set(),
    })
  }

  function toggleEditorInbound(id: string) {
    setSubEditor((current) => ({ ...current, inboundIds: toggleSet(current.inboundIds, id) }))
  }

  function toggleEditorSource(id: string) {
    setSubEditor((current) => ({ ...current, sourceIds: toggleSet(current.sourceIds, id) }))
  }

  function toggleEditorChain(id: string) {
    setSubEditor((current) => ({ ...current, chainIds: toggleSet(current.chainIds, id) }))
  }

  async function saveSubscription() {
    if (savingRef.current) return
    const name = subEditor.name.trim()
    if (!name) {
      setEditorError('请填写订阅名称')
      return
    }
    if (
      subEditor.localMode === 'none' &&
      !subEditor.includeAllChains &&
      subEditor.chainIds.size === 0 &&
      subEditor.sourceIds.size === 0
    ) {
      setEditorError('至少选择一个代理链、本地入站范围或外部源')
      return
    }
    if (
      subEditor.localMode === 'custom' &&
      subEditor.inboundIds.size === 0
    ) {
      setEditorError('请至少选择一个指定入站；仅使用外部源时请选择“不包含本地”')
      return
    }

    if (!beginSaving()) return
    setEditorError('')
    const body = {
      name,
      enabled: subEditor.enabled,
      include_all_inbounds: subEditor.localMode === 'all',
      inbound_ids:
        subEditor.localMode === 'custom' ? Array.from(subEditor.inboundIds) : [],
      include_standalone: subEditor.localMode !== 'none',
      include_all_chains: subEditor.includeAllChains,
      chain_ids: subEditor.includeAllChains ? [] : Array.from(subEditor.chainIds),
      external_source_ids: Array.from(subEditor.sourceIds),
      route_plan_id: subEditor.routePlanId,
    }
    try {
      const saved = subEditor.id
        ? await updateSubscription(subEditor.id, body)
        : await createSubscription(body)
      setSubscriptions((current) =>
        subEditor.id
          ? current.map((item) => (item.id === saved.id ? saved : item))
          : [...current, saved],
      )
      toast.success(subEditor.id ? '订阅已更新' : '订阅已创建')
      setSubEditor({
        ...EMPTY_SUB_EDITOR,
        inboundIds: new Set(),
        chainIds: new Set(),
        sourceIds: new Set(),
      })
    } catch (err) {
      setEditorError(errorText(err, '保存订阅失败'))
    } finally {
      endSaving()
    }
  }

  async function toggleSubscription(subscription: Subscription) {
    const key = `subscription-toggle:${subscription.id}`
    if (!beginOperation(key)) return
    try {
      const updated = await updateSubscription(subscription.id, { enabled: !subscription.enabled })
      setSubscriptions((current) => current.map((item) => (item.id === updated.id ? updated : item)))
      toast.success(updated.enabled ? '订阅已启用' : '订阅已禁用')
    } catch (err) {
      toast.error(errorText(err, '切换订阅状态失败'))
    } finally {
      endOperation(key)
    }
  }

  const [rotateTarget, setRotateTarget] = useState<Subscription | null>(null)
  const [rotatedLink, setRotatedLink] = useState<{ name: string; url: string } | null>(null)
  const [rotatedCopied, setRotatedCopied] = useState(false)
  const [deleteSubTarget, setDeleteSubTarget] = useState<Subscription | null>(null)

  function onRequestRotateToken(sub: Subscription) {
    setRotateTarget(sub)
  }

  async function confirmRotateSubscriptionToken() {
    if (!rotateTarget) return
    const subscription = rotateTarget
    setRotateTarget(null)
    const key = `subscription-rotate:${subscription.id}`
    if (!beginOperation(key)) return
    try {
      const rotated = await rotateSubscriptionToken(subscription.id)
      // 重新拉取以拿到后端下发的权威 URL；失败时退回 /sub/{token} 拼接。
      const latest = await listSubscriptions().catch(() => null)
      if (latest) setSubscriptions(latest)
      const refreshed = latest?.find((item) => item.id === subscription.id)
      const path = refreshed?.url || `/sub/${rotated.token}`
      setRotatedCopied(false)
      setRotatedLink({ name: subscription.name, url: getFullSubscriptionUrl(path) })
      toast.success('订阅链接已重置，旧链接已失效')
    } catch (err) {
      toast.error(errorText(err, '重置订阅链接失败'))
    } finally {
      endOperation(key)
    }
  }

  async function copyRotatedLink() {
    if (!rotatedLink) return
    try {
      await copyText(rotatedLink.url)
      setRotatedCopied(true)
      toast.success('已复制新订阅链接')
      if (copyTimerRef.current !== undefined) window.clearTimeout(copyTimerRef.current)
      copyTimerRef.current = window.setTimeout(() => setRotatedCopied(false), 2000)
    } catch {
      toast.error('复制失败')
    }
  }

  async function toggleSubscriptionDisabled(subscription: Subscription) {
    const key = `subscription-disable:${subscription.id}`
    if (!beginOperation(key)) return
    try {
      if (subscription.disabled) await enableSubscription(subscription.id)
      else await disableSubscription(subscription.id)
      const latest = await listSubscriptions().catch(() => null)
      if (latest) {
        setSubscriptions(latest)
      } else {
        setSubscriptions((current) =>
          current.map((item) =>
            item.id === subscription.id ? { ...item, disabled: !subscription.disabled } : item,
          ),
        )
      }
      toast.success(
        subscription.disabled ? '订阅已启用，公开链接恢复访问' : '订阅已停用，公开链接将返回 404',
      )
    } catch (err) {
      toast.error(errorText(err, subscription.disabled ? '启用订阅失败' : '停用订阅失败'))
    } finally {
      endOperation(key)
    }
  }

  function onRequestDeleteSubscription(sub: Subscription) {
    setDeleteSubTarget(sub)
  }

  async function confirmDeleteSubscription() {
    if (!deleteSubTarget) return
    const subscription = deleteSubTarget
    setDeleteSubTarget(null)
    const key = `subscription-delete:${subscription.id}`
    if (!beginOperation(key)) return
    try {
      await deleteSubscription(subscription.id)
      setSubscriptions((current) => current.filter((item) => item.id !== subscription.id))
      toast.success('订阅已删除')
    } catch (err) {
      toast.error(errorText(err, '删除订阅失败'))
    } finally {
      endOperation(key)
    }
  }

  async function onPreviewSubscription(subscription: Subscription, targetFormat = 'v2ray') {
    const key = `subscription-preview:${subscription.id}`
    if (!beginOperation(key)) return
    try {
      const res = await previewSubscription(subscription.id, targetFormat)
      setTextPreview({
        subId: subscription.id,
        subName: subscription.name,
        format: targetFormat,
        text: res,
      })
    } catch (err) {
      toast.error(errorText(err, '预览订阅失败'))
    } finally {
      endOperation(key)
    }
  }

  async function switchPreviewFormat(format: string) {
    if (!textPreview) return
    try {
      const res = await previewSubscription(textPreview.subId, format)
      setTextPreview({
        ...textPreview,
        format,
        text: res,
      })
    } catch (err) {
      toast.error(errorText(err, '切换预览格式失败'))
    }
  }

  function openCreateSource() {
    setEditorError('')
    setSourceEditor({ ...EMPTY_SOURCE_EDITOR, open: true })
  }

  function openEditSource(source: ExternalSource) {
    setEditorError('')
    setSourceEditor({
      open: true,
      id: source.id,
      name: source.name,
      url: source.url,
      interval: source.refresh_interval_sec,
      headers: formatHeadersObject(source.headers),
      enabled: source.enabled,
    })
  }

  function closeSourceEditor() {
    if (!saving) setSourceEditor(EMPTY_SOURCE_EDITOR)
  }

  async function saveSource() {
    if (savingRef.current) return
    const name = sourceEditor.name.trim()
    const url = sourceEditor.url.trim()
    if (!name || !url) {
      setEditorError('请填写名称与源 URL')
      return
    }
    const interval = Number(sourceEditor.interval)
    if (Number.isNaN(interval) || interval <= 0) {
      setEditorError('刷新间隔必须为大于 0 的数字（秒）')
      return
    }
    let headers: Record<string, string> = {}
    try {
      headers = parseHeadersText(sourceEditor.headers)
    } catch (err) {
      setEditorError(err instanceof Error ? err.message : 'HTTP 请求头格式不正确')
      return
    }

    if (!beginSaving()) return
    setEditorError('')
    const body = {
      name,
      url,
      refresh_interval_sec: Math.floor(interval),
      headers,
      enabled: sourceEditor.enabled,
    }
    try {
      const saved = sourceEditor.id
        ? await updateExternalSource(sourceEditor.id, body)
        : await createExternalSource(body)
      setSources((current) =>
        sourceEditor.id
          ? current.map((item) => (item.id === saved.id ? saved : item))
          : [...current, saved],
      )
      toast.success(sourceEditor.id ? '外部源已更新' : '外部源已创建')
      setSourceEditor(EMPTY_SOURCE_EDITOR)
    } catch (err) {
      setEditorError(errorText(err, '保存外部源失败'))
    } finally {
      endSaving()
    }
  }

  async function onRefreshSource(source: ExternalSource) {
    const key = `source-refresh:${source.id}`
    if (!beginOperation(key)) return
    try {
      const updated = await refreshExternalSource(source.id)
      setSources((current) => current.map((item) => (item.id === updated.id ? updated : item)))
      toast.success(`外部源「${source.name}」已刷新，发现 ${updated.cached_proxy_count} 个节点`)
    } catch (err) {
      toast.error(errorText(err, '刷新外部源失败'))
    } finally {
      endOperation(key)
    }
  }

  async function onPreviewSource(source: ExternalSource) {
    const key = `source-preview:${source.id}`
    if (!beginOperation(key)) return
    try {
      const res = await previewExternalSource(source.id)
      setSourcePreview(res)
      setSourcePreviewTitle(`外部源解析预览 · ${source.name}`)
    } catch (err) {
      toast.error(errorText(err, '预览外部源失败'))
    } finally {
      endOperation(key)
    }
  }

  const [deleteSourceTarget, setDeleteSourceTarget] = useState<ExternalSource | null>(null)

  function onRequestDeleteSource(source: ExternalSource) {
    setDeleteSourceTarget(source)
  }

  async function confirmDeleteSource() {
    if (!deleteSourceTarget) return
    const source = deleteSourceTarget
    setDeleteSourceTarget(null)
    const key = `source-delete:${source.id}`
    if (!beginOperation(key)) return
    try {
      await deleteExternalSource(source.id)
      setSources((current) => current.filter((item) => item.id !== source.id))
      toast.success('外部源已删除')
    } catch (err) {
      toast.error(errorText(err, '删除外部源失败'))
    } finally {
      endOperation(key)
    }
  }

  function getFullSubscriptionUrl(path: string | undefined): string {
    if (!path) return ''
    if (path.startsWith('http://') || path.startsWith('https://')) return path
    const base = publicBaseUrl || (typeof window !== 'undefined' ? window.location.origin : '')
    return `${base}${path.startsWith('/') ? '' : '/'}${path}`
  }

  async function copySubscriptionUrl(path: string | undefined, id: string) {
    const fullUrl = getFullSubscriptionUrl(path)
    try {
      await copyText(fullUrl)
      setCopiedId(id)
      toast.success('已复制完整订阅链接')
      if (copyTimerRef.current !== undefined) window.clearTimeout(copyTimerRef.current)
      copyTimerRef.current = window.setTimeout(() => setCopiedId(null), 2000)
    } catch {
      toast.error('复制失败')
    }
  }

  return (
    <div className="space-y-6">
      {/* Header Title Section */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight leading-snug text-foreground flex items-center gap-2.5">
            <Rss className="h-6 w-6 shrink-0 text-primary" />
            订阅配置管理
          </h1>
          <p className="text-sm text-muted-foreground mt-1 leading-relaxed">
            发布面向客户端的代理订阅分发链接 · 聚合与解析第三方外部订阅源
          </p>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <Button
            variant="outline"
            onClick={() => void load()}
            loading={loading}
            disabled={saving || pending.size > 0}
            className="border-border text-foreground hover:bg-muted gap-1.5 h-9 cursor-pointer"
          >
            <RefreshCw className="h-4 w-4" /> 刷新
          </Button>
        </div>
      </div>

      {loadError && (
        <Alert variant="destructive">
          <AlertTitle>数据加载遇到问题</AlertTitle>
          <AlertDescription>{loadError}</AlertDescription>
        </Alert>
      )}

      {/* Primary Tabs */}
      <Tabs defaultValue="publish" className="w-full space-y-6">
        <div>
          <TabsList className="inline-flex h-10 items-center justify-center rounded-lg bg-muted p-1 border border-border text-muted-foreground">
            <TabsTrigger
              value="publish"
              className="inline-flex items-center justify-center gap-2 text-xs font-semibold px-4 py-1.5 rounded-md transition-all cursor-pointer data-[state=active]:bg-background data-[state=active]:text-foreground text-muted-foreground hover:text-foreground data-[state=active]:shadow-xs"
            >
              <Radio className="h-4 w-4 text-primary" />
              发布订阅 ({subscriptions.length})
            </TabsTrigger>
            <TabsTrigger
              value="sources"
              className="inline-flex items-center justify-center gap-2 text-xs font-semibold px-4 py-1.5 rounded-md transition-all cursor-pointer data-[state=active]:bg-background data-[state=active]:text-foreground text-muted-foreground hover:text-foreground data-[state=active]:shadow-xs"
            >
              <Globe className="h-4 w-4 text-primary" />
              外部订阅源 ({sources.length})
            </TabsTrigger>
          </TabsList>
        </div>

        {/* Tab 1: Publish Subscriptions */}
        <TabsContent value="publish" className="space-y-6 mt-0">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div className="min-w-0 space-y-1">
              <h2 className="text-sm font-semibold leading-snug text-foreground">面向客户端的公网订阅分发</h2>
              <p className="text-xs leading-relaxed text-muted-foreground">
                支持编译出标准的 Clash (YAML) 与 sing-box (JSON) 订阅。客户端发起请求时动态包含在线节点。
              </p>
            </div>
            <Button onClick={openCreateSubscription} className="gap-1.5 h-9 cursor-pointer">
              <Plus className="h-4 w-4" /> 新建订阅
            </Button>
          </div>

          {loading && subscriptions.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-16 space-y-3">
              <RefreshCw className="h-6 w-6 animate-spin text-muted-foreground" />
              <span className="text-sm text-muted-foreground">正在加载订阅分发列表…</span>
            </div>
          ) : subscriptions.length === 0 ? (
            <Card className="border-border bg-card py-12 text-center">
              <CardContent className="space-y-3">
                <Rss className="h-10 w-10 text-muted-foreground mx-auto" />
                <div className="space-y-1">
                  <h3 className="text-base font-semibold text-foreground">暂无已发布的订阅</h3>
                  <p className="text-xs text-muted-foreground max-w-sm mx-auto">
                    点击右上角“新建订阅”按钮，创建一个支持 Clash 或 sing-box 的公网订阅链接。
                  </p>
                </div>
                <Button onClick={openCreateSubscription} className="gap-1.5 mt-2 cursor-pointer">
                  <Plus className="h-4 w-4" /> 创建第一个订阅
                </Button>
              </CardContent>
            </Card>
          ) : (
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-5">
              {subscriptions.map((sub) => {
                const subPending = entityOperationPending('subscription', sub.id)
                const fullUrl = getFullSubscriptionUrl(sub.url)
                const clashScheme = `clash://install-config?url=${encodeURIComponent(fullUrl + '?flag=clash')}`
                const singboxScheme = `sing-box://import-remote?url=${encodeURIComponent(fullUrl + '?flag=singbox')}`

                return (
                  <Card
                    key={sub.id}
                    className="relative transition-all duration-300 hover:translate-y-[-2px] border-border bg-card shadow-sm"
                  >
                    <CardHeader className="p-5 pb-3">
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0 flex-1 space-y-1">
                          <div className="flex flex-wrap items-center gap-2">
                            <CardTitle className="text-base font-bold leading-snug text-foreground">
                              {sub.name}
                            </CardTitle>
                            <Badge
                              variant="outline"
                              className="h-auto whitespace-normal leading-snug border-primary/30 bg-primary/10 text-primary font-mono text-[11px]"
                            >
                              通用分发 (Clash / sing-box / V2Ray)
                            </Badge>
                            {sub.disabled && <StatusBadge value="disabled" />}
                          </div>
                        </div>

                        <div className="flex items-center gap-2 shrink-0">
                          <Switch
                            checked={sub.enabled}
                            disabled={subPending || saving}
                            onCheckedChange={() => void toggleSubscription(sub)}
                          />
                        </div>
                      </div>
                    </CardHeader>

                    <CardContent className="p-5 pt-0 space-y-4">
                      {/* Subscription URL Box */}
                      <div className="space-y-1.5">
                        <span className="text-[11px] font-medium text-muted-foreground block">公网订阅地址</span>
                        <div className="flex items-center gap-2 p-2 rounded-lg bg-background border border-border">
                          <code className="text-xs font-mono text-foreground truncate flex-1 select-all px-1">
                            {fullUrl}
                          </code>
                          <TooltipProvider>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button
                                  size="sm"
                                  variant="ghost"
                                  onClick={() => void copySubscriptionUrl(sub.url, sub.id)}
                                  className="h-7 px-2.5 text-xs text-muted-foreground hover:text-foreground shrink-0 cursor-pointer"
                                >
                                  {copiedId === sub.id ? (
                                    <Check className="h-3.5 w-3.5 text-success" />
                                  ) : (
                                    <Copy className="h-3.5 w-3.5" />
                                  )}
                                  <span className="ml-1.5">{copiedId === sub.id ? '已复制' : '复制'}</span>
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>复制完整公网订阅 URL</TooltipContent>
                            </Tooltip>
                          </TooltipProvider>
                        </div>
                      </div>

                      {/* Client Quick Action Bar */}
                      <div className="flex flex-wrap items-center gap-2 pt-1">
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => setQrModal({ open: true, title: sub.name, url: fullUrl })}
                          className="h-8 text-xs border-border bg-background hover:bg-muted text-foreground gap-1.5 cursor-pointer"
                        >
                          <QrCode className="h-3.5 w-3.5 text-success" /> 扫码
                        </Button>

                        <a
                          href={clashScheme}
                          target="_blank"
                          rel="noreferrer"
                          className="inline-flex items-center justify-center rounded-md text-xs font-medium border border-border bg-background hover:bg-muted text-foreground h-8 px-3 transition-colors gap-1.5 cursor-pointer"
                        >
                          <ExternalLink className="h-3.5 w-3.5 text-primary" />
                          导入 Clash
                        </a>

                        <a
                          href={singboxScheme}
                          target="_blank"
                          rel="noreferrer"
                          className="inline-flex items-center justify-center rounded-md text-xs font-medium border border-border bg-background hover:bg-muted text-foreground h-8 px-3 transition-colors gap-1.5 cursor-pointer"
                        >
                          <ExternalLink className="h-3.5 w-3.5 text-primary" />
                          导入 sing-box
                        </a>

                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => void onPreviewSubscription(sub)}
                          loading={subPending}
                          disabled={subPending}
                          className="h-8 text-xs text-muted-foreground hover:text-foreground gap-1.5 ml-auto cursor-pointer"
                        >
                          <Eye className="h-3.5 w-3.5" /> 预览结构
                        </Button>
                      </div>

                      {/* Content Aggregation Summary */}
                      <div className="grid grid-cols-2 gap-2 pt-3 border-t border-border text-xs">
                        <div className="space-y-0.5">
                          <span className="text-[10px] text-muted-foreground block">服务端代理链</span>
                          <span className="text-foreground font-medium flex items-center gap-1">
                            <Server className="h-3 w-3 text-muted-foreground" />
                            {sub.include_all_chains
                              ? `全部已启用链 (${chains.filter((chain) => chain.enabled).length} 条)`
                              : (sub.chain_ids?.length ?? 0) > 0
                                ? `指定 ${sub.chain_ids.length} 条链`
                                : '不包含代理链'}
                          </span>
                        </div>
                        <div className="space-y-0.5">
                          <span className="text-[10px] text-muted-foreground block">原始节点与外部源</span>
                          <span className="text-foreground font-medium flex items-center gap-1">
                            <Layers className="h-3 w-3 text-muted-foreground" />
                            {sub.include_standalone
                              ? (sub.include_all_inbounds ? '全部原始节点' : `指定 ${sub.inbound_ids.length} 个入站`)
                              : '不含原始节点'}
                            {(sub.external_source_ids?.length ?? 0) > 0
                              ? ` + ${sub.external_source_ids?.length ?? 0} 个外部源`
                              : ''}
                          </span>
                        </div>
                      </div>

                      {/* Footer Actions */}
                      <div className="flex items-center justify-between pt-3 border-t border-border text-xs text-muted-foreground">
                        <div className="flex items-center gap-1">
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => onRequestRotateToken(sub)}
                            loading={subPending}
                            disabled={subPending}
                            className="h-7 text-xs text-muted-foreground hover:text-warning px-2 cursor-pointer gap-1"
                          >
                            <RotateCw className="h-3 w-3" /> 重置链接
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => void toggleSubscriptionDisabled(sub)}
                            loading={subPending}
                            disabled={subPending}
                            className={`h-7 text-xs px-2 cursor-pointer gap-1 ${
                              sub.disabled
                                ? 'text-success hover:text-success/80'
                                : 'text-muted-foreground hover:text-destructive'
                            }`}
                          >
                            <Power className="h-3 w-3" /> {sub.disabled ? '启用' : '停用'}
                          </Button>
                        </div>

                        <div className="flex items-center gap-1">
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => openEditSubscription(sub)}
                            disabled={subPending}
                            className="h-7 text-xs text-muted-foreground hover:text-foreground px-2.5 cursor-pointer gap-1"
                          >
                            <Edit className="h-3.5 w-3.5" /> 编辑
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => onRequestDeleteSubscription(sub)}
                            disabled={subPending}
                            className="h-7 text-xs text-destructive hover:text-destructive/80 px-2.5 cursor-pointer gap-1"
                          >
                            <Trash2 className="h-3.5 w-3.5" /> 删除
                          </Button>
                        </div>
                      </div>
                    </CardContent>
                  </Card>
                )
              })}
            </div>
          )}
        </TabsContent>

        {/* Tab 2: External Sources */}
        <TabsContent value="sources" className="space-y-6 mt-0">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div className="min-w-0 space-y-1">
              <h2 className="text-sm font-semibold leading-snug text-foreground">第三方外部订阅源聚合</h2>
              <p className="text-xs leading-relaxed text-muted-foreground">
                配置外部机场或提供者的订阅 URL，系统后台会定时抓取并解析节点，合并下发给指定订阅。
              </p>
            </div>
            <Button onClick={openCreateSource} className="gap-1.5 h-9 cursor-pointer">
              <Plus className="h-4 w-4" /> 添加外部源
            </Button>
          </div>

          {loading && sources.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-16 space-y-3">
              <RefreshCw className="h-6 w-6 animate-spin text-muted-foreground" />
              <span className="text-sm text-muted-foreground">正在加载外部订阅源列表…</span>
            </div>
          ) : sources.length === 0 ? (
            <Card className="border-border bg-card py-12 text-center">
              <CardContent className="space-y-3">
                <Globe className="h-10 w-10 text-muted-foreground stroke-[1.5] mx-auto" />
                <div className="space-y-1">
                  <h3 className="text-base font-semibold text-foreground">暂无第三方外部订阅源</h3>
                  <p className="text-xs text-muted-foreground max-w-sm mx-auto">
                    添加外部订阅源链接后，面板可以自动抓取并将其中的节点合并到您发布的订阅中。
                  </p>
                </div>
                <Button onClick={openCreateSource} className="gap-1.5 mt-2 cursor-pointer">
                  <Plus className="h-4 w-4" /> 添加第一个外部源
                </Button>
              </CardContent>
            </Card>
          ) : (
            <Card className="border-border bg-card">
              <CardContent className="p-0">
                <div className="overflow-x-auto">
                  <Table>
                    <TableHeader className="bg-muted/40 border-border">
                      <TableRow className="hover:bg-transparent">
                        <TableHead className="min-w-[140px]">源名称</TableHead>
                        <TableHead className="min-w-[220px]">抓取 URL</TableHead>
                        <TableHead className="w-24 text-center">解析节点数</TableHead>
                        <TableHead className="w-28 text-center">刷新间隔</TableHead>
                        <TableHead className="w-32">最后更新</TableHead>
                        <TableHead className="min-w-[5.5rem]">状态</TableHead>
                        <TableHead className="w-[200px] text-right">操作</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody className="divide-y divide-border">
                      {sources.map((src) => {
                        const srcPending = entityOperationPending('source', src.id)
                        return (
                          <TableRow key={src.id} className="hover:bg-muted/50 border-border">
                            <TableCell className="font-semibold text-foreground">
                              {src.name}
                            </TableCell>
                            <TableCell>
                              <code className="text-xs font-mono text-muted-foreground break-all block max-w-xs truncate">
                                {src.url}
                              </code>
                              {src.last_error && (
                                <span className="text-[10px] font-mono text-destructive block truncate mt-0.5">
                                  抓取失败: {src.last_error}
                                </span>
                              )}
                            </TableCell>
                            <TableCell className="text-center font-mono font-bold text-foreground">
                              {src.cached_proxy_count ?? 0}
                            </TableCell>
                            <TableCell className="text-center font-mono text-xs text-muted-foreground">
                              {formatInterval(src.refresh_interval_sec)}
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground font-mono">
                              {formatTime(src.updated_at_unix)}
                            </TableCell>
                            <TableCell>
                              <StatusBadge value={src.enabled ? 'enabled' : 'disabled'} />
                            </TableCell>
                            <TableCell className="text-right">
                              <div className="flex items-center justify-end gap-1">
                                <Button
                                  size="sm"
                                  variant="ghost"
                                  loading={srcPending}
                                  disabled={srcPending}
                                  onClick={() => void onRefreshSource(src)}
                                  className="h-8 px-2 text-xs text-primary hover:text-primary/80 cursor-pointer gap-1"
                                >
                                  <RefreshCw className="h-3.5 w-3.5" /> 刷新
                                </Button>
                                <Button
                                  size="sm"
                                  variant="ghost"
                                  loading={srcPending}
                                  disabled={srcPending}
                                  onClick={() => void onPreviewSource(src)}
                                  className="h-8 px-2 text-xs text-muted-foreground hover:text-foreground cursor-pointer gap-1"
                                >
                                  <Eye className="h-3.5 w-3.5" /> 预览
                                </Button>
                                <Button
                                  size="sm"
                                  variant="ghost"
                                  disabled={srcPending}
                                  onClick={() => openEditSource(src)}
                                  className="h-8 px-2 text-xs text-muted-foreground hover:text-foreground cursor-pointer gap-1"
                                >
                                  <Edit className="h-3.5 w-3.5" /> 编辑
                                </Button>
                                <Button
                                  size="sm"
                                  variant="ghost"
                                  disabled={srcPending}
                                  onClick={() => onRequestDeleteSource(src)}
                                  className="h-8 px-2 text-xs text-destructive hover:text-destructive/80 cursor-pointer gap-1"
                                >
                                  <Trash2 className="h-3.5 w-3.5" /> 删除
                                </Button>
                              </div>
                            </TableCell>
                          </TableRow>
                        )
                      })}
                    </TableBody>
                  </Table>
                </div>
              </CardContent>
            </Card>
          )}
        </TabsContent>
      </Tabs>

      {/* Subscription Editor Dialog */}
      <Dialog open={subEditor.open} onOpenChange={(v) => !v && closeSubscriptionEditor()}>
        <DialogContent className="sm:max-w-lg bg-zinc-950 border-zinc-900 text-zinc-100 p-6 space-y-5 shadow-xl">
          <DialogHeader className="space-y-1">
            <DialogTitle className="text-lg font-bold flex items-center gap-2 text-zinc-100">
              <Rss className="h-5 w-5 text-cyan-400" />
              {subEditor.id ? '编辑订阅设置' : '创建新订阅'}
            </DialogTitle>
            <p className="text-xs text-zinc-400">
              面向客户端生成标准 Clash (YAML) 或 sing-box (JSON) 订阅配置链接。
            </p>
          </DialogHeader>

          {editorError && (
            <Alert variant="destructive">
              <AlertDescription>{editorError}</AlertDescription>
            </Alert>
          )}

          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label className="text-xs text-zinc-300">订阅名称</Label>
              <Input
                value={subEditor.name}
                onChange={(e) => setSubEditor({ ...subEditor, name: e.target.value })}
                placeholder="例：常用客户端订阅 / 通用节点池"
                className="bg-zinc-900 border-zinc-800 text-sm text-zinc-100"
              />
            </div>

            <div className="space-y-2 pt-2 border-t border-border">
              <div className="flex items-center justify-between">
                <div>
                  <Label className="text-xs text-muted-foreground block">服务端代理链</Label>
                  <p className="text-[10px] text-muted-foreground mt-0.5">客户端只连接链的入口，不会看到中间跳</p>
                </div>
                <div className="flex items-center gap-2">
                  <Label htmlFor="sub-all-chains" className="text-[11px] text-muted-foreground">全部已启用链</Label>
                  <Switch
                    id="sub-all-chains"
                    checked={subEditor.includeAllChains}
                    onCheckedChange={(value) => setSubEditor({ ...subEditor, includeAllChains: value })}
                  />
                </div>
              </div>
              {!subEditor.includeAllChains && (
                <div className="p-3 bg-muted/60 rounded-lg border border-border max-h-40 overflow-y-auto space-y-2">
                  {chains.length === 0 ? (
                    <div className="text-xs text-muted-foreground text-center py-2">暂无代理链，请先在“代理链”页面创建</div>
                  ) : (
                    chains.map((chain) => (
                      <div key={chain.id} className="flex items-center space-x-2 text-xs">
                        <Checkbox
                          id={`chain-${chain.id}`}
                          checked={subEditor.chainIds.has(chain.id)}
                          onCheckedChange={() => toggleEditorChain(chain.id)}
                        />
                        <label htmlFor={`chain-${chain.id}`} className="text-foreground cursor-pointer flex-1">
                          {chain.name}
                          <span className="text-muted-foreground ml-1">({chain.hops.length} 跳 · {chainStateText(chain.state)})</span>
                        </label>
                      </div>
                    ))
                  )}
                </div>
              )}
            </div>

            <div className="space-y-2 pt-2 border-t border-border">
              <Label className="text-xs text-muted-foreground block">包含本地入站范围</Label>
              <div className="grid grid-cols-3 gap-2">
                <button
                  type="button"
                  onClick={() => setSubEditor({ ...subEditor, localMode: 'all' })}
                  className={`p-2 text-center rounded border text-xs font-medium transition-all cursor-pointer ${
                    subEditor.localMode === 'all'
                      ? 'border-primary bg-primary/10 text-primary'
                      : 'border-border bg-muted/40 text-muted-foreground hover:text-foreground'
                  }`}
                >
                  包含全部入站
                </button>
                <button
                  type="button"
                  onClick={() => setSubEditor({ ...subEditor, localMode: 'custom' })}
                  className={`p-2 text-center rounded border text-xs font-medium transition-all cursor-pointer ${
                    subEditor.localMode === 'custom'
                      ? 'border-primary bg-primary/10 text-primary'
                      : 'border-border bg-muted/40 text-muted-foreground hover:text-foreground'
                  }`}
                >
                  选择指定入站
                </button>
                <button
                  type="button"
                  onClick={() => setSubEditor({ ...subEditor, localMode: 'none' })}
                  className={`p-2 text-center rounded border text-xs font-medium transition-all cursor-pointer ${
                    subEditor.localMode === 'none'
                      ? 'border-primary bg-primary/10 text-primary'
                      : 'border-border bg-muted/40 text-muted-foreground hover:text-foreground'
                  }`}
                >
                  不包含本地入站
                </button>
              </div>

              {subEditor.localMode === 'custom' && (
                <div className="p-3 bg-muted/60 rounded-lg border border-border max-h-40 overflow-y-auto space-y-2 mt-2">
                  {inbounds.length === 0 ? (
                    <div className="text-xs text-muted-foreground text-center py-2">暂无可用入站</div>
                  ) : (
                    inbounds.map((ib) => (
                      <div key={ib.id} className="flex items-center space-x-2 text-xs">
                        <Checkbox
                          id={`ib-${ib.id}`}
                          checked={subEditor.inboundIds.has(ib.id)}
                          onCheckedChange={() => toggleEditorInbound(ib.id)}
                        />
                        <label htmlFor={`ib-${ib.id}`} className="text-foreground font-mono cursor-pointer">
                          {ib.name} ({ib.protocol})
                        </label>
                      </div>
                    ))
                  )}
                </div>
              )}
            </div>

            <div className="space-y-2 pt-2 border-t border-border">
              <Label className="text-xs text-muted-foreground block">合并第三方外部订阅源</Label>
              {sources.length === 0 ? (
                <p className="text-xs text-muted-foreground italic">暂无已启用的外部订阅源</p>
              ) : (
                <div className="p-3 bg-muted/60 rounded-lg border border-border max-h-40 overflow-y-auto space-y-2">
                  {sources.map((src) => (
                    <div key={src.id} className="flex items-center space-x-2 text-xs">
                      <Checkbox
                        id={`src-${src.id}`}
                        checked={subEditor.sourceIds.has(src.id)}
                        onCheckedChange={() => toggleEditorSource(src.id)}
                      />
                      <label htmlFor={`src-${src.id}`} className="text-foreground cursor-pointer">
                        {src.name} <span className="text-muted-foreground font-mono text-[10px]">({src.cached_proxy_count} 个节点)</span>
                      </label>
                    </div>
                  ))}
                </div>
              )}
            </div>

            <div className="space-y-2 pt-2 border-t border-zinc-900">
              <Label className="text-xs text-zinc-300 block">路由计划</Label>
              <p className="text-[10px] text-zinc-500 -mt-0.5">
                按规则决定订阅内流量走代理链、直连或拦截；仅可选择订阅级路由计划
              </p>
              <select
                value={subEditor.routePlanId}
                onChange={(e) => setSubEditor({ ...subEditor, routePlanId: e.target.value })}
                className="h-9 w-full rounded-md border border-zinc-800 bg-zinc-900 px-3 text-sm text-zinc-100 cursor-pointer"
              >
                <option value="">无</option>
                {routePlans
                  .filter((plan) => plan.scope === 'subscription')
                  .map((plan) => (
                    <option key={plan.id} value={plan.id}>
                      {plan.name}
                      {plan.enabled ? '' : '（已停用）'}
                    </option>
                  ))}
              </select>
            </div>

            <div className="flex items-center justify-between pt-2 border-t border-border">
              <Label className="text-xs text-muted-foreground">启用该订阅分发</Label>
              <Switch
                checked={subEditor.enabled}
                onCheckedChange={(v) => setSubEditor({ ...subEditor, enabled: v })}
              />
            </div>
          </div>

          <DialogFooter className="pt-2">
            <Button variant="outline" onClick={closeSubscriptionEditor} disabled={saving}>
              取消
            </Button>
            <Button onClick={() => void saveSubscription()} loading={saving}>
              {subEditor.id ? '更新订阅' : '创建订阅'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* External Source Editor Dialog */}
      <Dialog open={sourceEditor.open} onOpenChange={(v) => !v && closeSourceEditor()}>
        <DialogContent className="sm:max-w-lg p-6 space-y-5 shadow-xl">
          <DialogHeader className="space-y-1">
            <DialogTitle className="text-lg font-bold flex items-center gap-2">
              <Globe className="h-5 w-5 text-primary" />
              {sourceEditor.id ? '编辑外部订阅源' : '添加外部订阅源'}
            </DialogTitle>
            <p className="text-xs text-muted-foreground">
              抓取并解析外部 HTTP/HTTPS 机场订阅链接，将其中的节点合并入本系统的订阅分发中。
            </p>
          </DialogHeader>

          {editorError && (
            <Alert variant="destructive">
              <AlertDescription>{editorError}</AlertDescription>
            </Alert>
          )}

          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label className="text-xs">源名称</Label>
              <Input
                value={sourceEditor.name}
                onChange={(e) => setSourceEditor({ ...sourceEditor, name: e.target.value })}
                placeholder="例：香港备用节点池 / 外部机场"
              />
            </div>

            <div className="space-y-1.5">
              <Label className="text-xs">抓取订阅 URL</Label>
              <Input
                value={sourceEditor.url}
                onChange={(e) => setSourceEditor({ ...sourceEditor, url: e.target.value })}
                placeholder="https://example.com/api/v1/client/subscribe?token=..."
                className="font-mono text-xs"
              />
            </div>

            <div className="space-y-1.5">
              <Label className="text-xs">后台自动刷新间隔 (秒)</Label>
              <Input
                type="number"
                value={sourceEditor.interval}
                onChange={(e) => setSourceEditor({ ...sourceEditor, interval: e.target.value })}
                placeholder="86400 (默认 24 小时)"
                className="font-mono text-xs"
              />
              <span className="text-[10px] text-muted-foreground block">
                常用提示：86400 秒 = 24 小时，43200 秒 = 12 小时。
              </span>
            </div>

            <div className="space-y-1.5">
              <Label className="text-xs">自定义 HTTP 请求头 (可选，每行 Key: Value)</Label>
              <textarea
                value={sourceEditor.headers}
                onChange={(e) => setSourceEditor({ ...sourceEditor, headers: e.target.value })}
                rows={3}
                placeholder="User-Agent: ClashMeta&#10;Authorization: Bearer xyz"
                className="w-full rounded-md bg-background border border-input p-2.5 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
              />
            </div>

            <div className="flex items-center justify-between pt-2 border-t border-border">
              <Label className="text-xs text-muted-foreground">启用自动抓取</Label>
              <Switch
                checked={sourceEditor.enabled}
                onCheckedChange={(v) => setSourceEditor({ ...sourceEditor, enabled: v })}
              />
            </div>
          </div>

          <DialogFooter className="pt-2">
            <Button variant="outline" onClick={closeSourceEditor} disabled={saving}>
              取消
            </Button>
            <Button onClick={() => void saveSource()} loading={saving}>
              {sourceEditor.id ? '更新外部源' : '创建外部源'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* QR Code Modal */}
      <QRCodeModal
        open={qrModal.open}
        onClose={() => setQrModal({ open: false, title: '', url: '' })}
        title={qrModal.title}
        url={qrModal.url}
      />

      {/* Text Preview Modal */}
      <Dialog open={textPreview !== null} onOpenChange={(v) => !v && setTextPreview(null)}>
        <DialogContent className="sm:max-w-2xl p-6 space-y-4 shadow-xl">
          <DialogHeader className="space-y-1">
            <DialogTitle className="text-base font-bold">
              订阅内容预览 · {textPreview?.subName}
            </DialogTitle>
          </DialogHeader>

          <div className="flex items-center gap-2 pb-2 border-b border-border">
            <span className="text-xs text-muted-foreground mr-1 font-medium">预览格式：</span>
            {(['v2ray', 'clash', 'singbox'] as const).map((f) => (
              <button
                key={f}
                type="button"
                onClick={() => void switchPreviewFormat(f)}
                className={`px-3 py-1 rounded text-xs font-mono font-medium transition-all cursor-pointer ${
                  textPreview?.format === f
                    ? f === 'clash'
                      ? 'bg-cyan-500/20 text-cyan-500 border border-cyan-500/40'
                      : f === 'singbox'
                        ? 'bg-violet-500/20 text-violet-500 border border-violet-500/40'
                        : 'bg-emerald-500/20 text-emerald-500 border border-emerald-500/40'
                    : 'bg-muted text-muted-foreground border border-border hover:text-foreground'
                }`}
              >
                {f === 'v2ray' ? 'V2Ray 链接' : f === 'clash' ? 'Clash YAML' : 'sing-box JSON'}
              </button>
            ))}
          </div>

          <div className="relative">
            <pre className="p-4 bg-muted/60 border border-border rounded-lg max-h-[60vh] overflow-y-auto text-xs font-mono text-foreground whitespace-pre-wrap break-all leading-relaxed">
              {textPreview?.text}
            </pre>
          </div>

          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                if (textPreview) {
                  void copyText(textPreview.text)
                  toast.success('已复制预览内容')
                }
              }}
              className="border-zinc-800 cursor-pointer"
            >
              <Copy className="h-4 w-4 mr-2" /> 复制全部
            </Button>
            <Button onClick={() => setTextPreview(null)} className="cursor-pointer">关闭</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Source Preview Modal */}
      <Dialog open={sourcePreview !== null} onOpenChange={(v) => !v && setSourcePreview(null)}>
        <DialogContent className="sm:max-w-2xl bg-zinc-950 border-zinc-900 text-zinc-100 p-6 space-y-4 shadow-xl">
          <DialogHeader className="space-y-1">
            <DialogTitle className="text-base font-bold text-zinc-100">
              {sourcePreviewTitle}
            </DialogTitle>
          </DialogHeader>

          {sourcePreview && (
            <div className="space-y-3">
              <div className="text-xs text-zinc-400 font-mono">
                此外部源成功解析出 <strong className="text-cyan-400">{sourcePreview.count}</strong> 个节点。
              </div>

              <div className="p-3 bg-zinc-900 border border-zinc-800 rounded-lg max-h-[50vh] overflow-y-auto space-y-2">
                {sourcePreview.names.map((name, idx) => (
                  <div key={idx} className="p-2 rounded bg-zinc-950 border border-zinc-900 text-xs flex items-center justify-between font-mono">
                    <span className="font-bold text-zinc-200">{name}</span>
                    <Badge variant="outline" className="text-[10px] border-zinc-800 text-zinc-400">
                      外部节点
                    </Badge>
                  </div>
                ))}
              </div>
            </div>
          )}

          <DialogFooter>
            <Button onClick={() => setSourcePreview(null)} className="cursor-pointer">关闭</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Rotated Link Result Modal */}
      <Dialog open={rotatedLink !== null} onOpenChange={(v) => !v && setRotatedLink(null)}>
        <DialogContent className="sm:max-w-lg bg-zinc-950 border-zinc-900 text-zinc-100 p-6 space-y-4 shadow-xl">
          <DialogHeader className="space-y-1">
            <DialogTitle className="text-base font-bold text-zinc-100">
              订阅链接已重置 · {rotatedLink?.name}
            </DialogTitle>
            <p className="text-xs text-zinc-400">
              旧链接已立即作废，请将下方新链接更新到所有客户端。
            </p>
          </DialogHeader>

          <div className="flex items-center gap-2 p-2 rounded-lg bg-zinc-900 border border-zinc-800">
            <code className="text-xs font-mono text-zinc-100 break-all flex-1 select-all px-1">
              {rotatedLink?.url}
            </code>
          </div>

          <DialogFooter className="gap-2">
            <Button
              variant="outline"
              onClick={() => setRotatedLink(null)}
              className="border-zinc-800 cursor-pointer"
            >
              关闭
            </Button>
            <Button onClick={() => void copyRotatedLink()} className="cursor-pointer gap-1.5">
              {rotatedCopied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
              一键复制
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Rotate Token Confirm Modal */}
      <ConfirmModal
        open={!!rotateTarget}
        title="重置订阅链接"
        description={`确定重置「${rotateTarget?.name || ''}」的订阅链接吗？重置后旧链接将立即作废，所有使用旧链接的客户端都需要更新。`}
        confirmText="确认重置"
        confirmVariant="default"
        onConfirm={() => void confirmRotateSubscriptionToken()}
        onCancel={() => setRotateTarget(null)}
      />

      {/* Delete Subscription Confirm Modal */}
      <ConfirmModal
        open={!!deleteSubTarget}
        title="删除订阅"
        description={`确定要删除订阅「${deleteSubTarget?.name || ''}」吗？此操作无法撤销。`}
        confirmText="确认删除"
        confirmVariant="destructive"
        onConfirm={() => void confirmDeleteSubscription()}
        onCancel={() => setDeleteSubTarget(null)}
      />

      {/* Delete External Source Confirm Modal */}
      <ConfirmModal
        open={!!deleteSourceTarget}
        title="删除外部源"
        description={`确定要删除外部源「${deleteSourceTarget?.name || ''}」吗？此操作无法撤销。`}
        confirmText="确认删除"
        confirmVariant="destructive"
        onConfirm={() => void confirmDeleteSource()}
        onCancel={() => setDeleteSourceTarget(null)}
      />
    </div>
  )
}

function toggleSet<T>(set: Set<T>, item: T): Set<T> {
  const next = new Set(set)
  if (next.has(item)) next.delete(item)
  else next.add(item)
  return next
}

function formatHeadersObject(headers: Record<string, string> | undefined): string {
  if (!headers) return ''
  return Object.entries(headers)
    .map(([k, v]) => `${k}: ${v}`)
    .join('\n')
}

function parseHeadersText(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  const lines = text.split('\n')
  for (const raw of lines) {
    const line = raw.trim()
    if (!line) continue
    const idx = line.indexOf(':')
    if (idx <= 0) {
      throw new Error(`请求头格式错误 (缺少冒号)：${line}`)
    }
    const key = line.slice(0, idx).trim()
    const val = line.slice(idx + 1).trim()
    if (!key) {
      throw new Error(`请求头 Key 不能为空：${line}`)
    }
    out[key] = val
  }
  return out
}

function formatInterval(seconds: number): string {
  if (!seconds || seconds <= 0) return '—'
  if (seconds >= 86400 && seconds % 86400 === 0) {
    return `${seconds / 86400} 天`
  }
  if (seconds >= 3600 && seconds % 3600 === 0) {
    return `${seconds / 3600} 小时`
  }
  if (seconds >= 60 && seconds % 60 === 0) {
    return `${seconds / 60} 分钟`
  }
  return `${seconds} 秒`
}

function chainStateText(state: string): string {
  if (state === 'healthy') return '健康'
  if (state === 'degraded') return '降级'
  if (state === 'deploying') return '部署中'
  return '已停用'
}

function errorText(err: unknown, fallback: string): string {
  if (!err) return fallback
  if (typeof err === 'string') return err
  if (err instanceof Error) return err.message
  return fallback
}
