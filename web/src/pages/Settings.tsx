import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { ConfirmModal } from '@/components/ui/confirm-modal'
import {
  getMeta,
  getSettings,
  putSettings,
  type MetaInfo,
  type Settings as SettingsResponse,
} from '../api/client'
import { useUnsavedNavigation } from '../lib/useUnsavedNavigation'
import { toast } from '../lib/toast'
import { RefreshCw, Save, Key, Network, Shield } from 'lucide-react'

type NumericDraft = number | string

type SettingsDraft = {
  token: string
  timeoutSec: NumericDraft
  concurrency: NumericDraft
  listenAddr: string
  publicBase: string
}

type ValidationErrors = {
  timeoutSec: string
  concurrency: string
  publicBase: string
  newPassword: string
  confirmPassword: string
}

const DEFAULT_DRAFT: SettingsDraft = {
  token: '',
  timeoutSec: 10,
  concurrency: 8,
  listenAddr: '',
  publicBase: '',
}

export default function Settings() {
  const [meta, setMeta] = useState<MetaInfo | null>(null)
  const [draft, setDraft] = useState<SettingsDraft>(DEFAULT_DRAFT)
  const [savedDraft, setSavedDraft] = useState<SettingsDraft | null>(null)
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [saveError, setSaveError] = useState('')
  const loadSequence = useRef(0)

  const dirty = useMemo(
    () =>
      savedDraft !== null &&
      (!sameDraft(draft, savedDraft) || newPassword !== '' || confirmPassword !== ''),
    [confirmPassword, draft, newPassword, savedDraft],
  )

  useUnsavedNavigation({
    active: dirty,
    message: '离开设置页会丢失尚未保存的系统设置。',
  })

  const errors = useMemo(
    () => validate(draft, newPassword, confirmPassword),
    [confirmPassword, draft, newPassword],
  )
  const hasValidationErrors = Object.values(errors).some(Boolean)

  const load = useCallback(async () => {
    const sequence = ++loadSequence.current
    setLoading(true)
    setLoadError('')
    setSaveError('')
    try {
      const [settings, nextMeta] = await Promise.all([
        getSettings(),
        getMeta().catch(() => null),
      ])
      if (sequence !== loadSequence.current) return
      const nextDraft = draftFromResponse(settings)
      setDraft(nextDraft)
      setSavedDraft(nextDraft)
      setNewPassword('')
      setConfirmPassword('')
      if (nextMeta) setMeta(nextMeta)
    } catch (err) {
      if (sequence !== loadSequence.current) return
      setLoadError(err instanceof Error ? err.message : '加载系统设置失败')
    } finally {
      if (sequence === loadSequence.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    if (!dirty) return
    const warnBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', warnBeforeUnload)
    return () => window.removeEventListener('beforeunload', warnBeforeUnload)
  }, [dirty])

  useEffect(() => {
    setSaveError('')
  }, [confirmPassword, draft, newPassword])

  function updateDraft(patch: Partial<SettingsDraft>) {
    setDraft((current) => ({ ...current, ...patch }))
  }

  const [reloadConfirmOpen, setReloadConfirmOpen] = useState(false)

  function requestReload() {
    if (!dirty) {
      void load()
      return
    }
    setReloadConfirmOpen(true)
  }

  function confirmReloadSettings() {
    setReloadConfirmOpen(false)
    void load()
  }

  async function onSave() {
    if (busy || loading || savedDraft === null) return
    if (hasValidationErrors) {
      setSaveError('请先修正标红的设置项')
      return
    }

    setBusy(true)
    setSaveError('')
    try {
      const body: Parameters<typeof putSettings>[0] = {
        default_agent_token: draft.token,
        grpc_timeout_sec: Number(draft.timeoutSec),
        max_concurrency: Number(draft.concurrency),
        listen_addr: draft.listenAddr.trim(),
        public_base_url: normalizePublicBase(draft.publicBase),
      }
      if (newPassword) body.new_password = newPassword

      const settings = await putSettings(body)
      const nextDraft = draftFromResponse(settings)
      setDraft(nextDraft)
      setSavedDraft(nextDraft)
      setNewPassword('')
      setConfirmPassword('')
      toast.success('系统设置保存成功')
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : '保存系统设置失败')
    } finally {
      setBusy(false)
    }
  }

  const formDisabled = loading || busy || savedDraft === null

  return (
    <div className="space-y-6">
      {/* Page Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between border-b border-border pb-5">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">系统设置</h1>
          <p className="text-sm text-muted-foreground mt-1">管理节点默认连接令牌、系统超时时长、Panel 自身监听接口及控制台访问密码</p>
          {meta?.panel_version && (
            <p className="text-xs text-muted-foreground mt-2 flex flex-wrap gap-2 items-center">
              <span>Panel <code className="font-mono text-muted-foreground">{meta.panel_version}</code></span>
              {meta.panel_commit && meta.panel_commit !== 'unknown' && (
                <span>({meta.panel_commit})</span>
              )}
              {meta.recommended_agent_version && (
                <>
                  <span className="text-muted-foreground">•</span>
                  <span>推荐 Agent {meta.recommended_agent_version}</span>
                </>
              )}
            </p>
          )}
        </div>

        <div className="flex items-center gap-3 shrink-0">
          {dirty ? (
            <Badge variant="warning">有未保存更改</Badge>
          ) : savedDraft ? (
            <Badge variant="success">已保存</Badge>
          ) : null}
          <Button
            variant="outline"
            loading={loading}
            disabled={busy}
            onClick={requestReload}
            className="border-border text-foreground hover:bg-muted gap-1.5 h-9"
          >
            {dirty ? '放弃修改并重载' : '重新加载'}
          </Button>
          <Button
            loading={busy}
            disabled={loading || savedDraft === null || !dirty}
            onClick={() => void onSave()}
            className="gap-1.5 h-9"
          >
            <Save className="h-4 w-4" /> 保存设置
          </Button>
        </div>
      </div>

      {loadError && (
        <Alert variant="destructive">
          <AlertTitle>系统设置加载失败</AlertTitle>
          <AlertDescription>{loadError}</AlertDescription>
          <Button size="sm" variant="outline" className="mt-2 text-destructive border-destructive/30 hover:bg-destructive/10" onClick={() => void load()}>
            重试
          </Button>
        </Alert>
      )}

      {saveError && (
        <Alert variant="destructive">
          <AlertTitle>系统设置未保存</AlertTitle>
          <AlertDescription>{saveError}</AlertDescription>
        </Alert>
      )}

      {loading && savedDraft === null ? (
        <div className="flex flex-col items-center justify-center py-20 gap-3">
          <RefreshCw className="h-6 w-6 animate-spin text-muted-foreground" />
          <span className="text-sm text-muted-foreground">正在加载系统设置…</span>
        </div>
      ) : savedDraft ? (
        <div className="grid grid-cols-1 gap-6">
          {/* Card 1: Connection & Tasks */}
          <Card className="border-border bg-card">
            <CardHeader className="p-5 pb-3">
              <CardTitle className="text-sm font-semibold flex items-center gap-2">
                <Key className="h-4 w-4 text-muted-foreground" />
                连接与任务参数
              </CardTitle>
              <CardDescription className="text-xs text-muted-foreground">
                定义节点连接的默认 Agent 令牌与连接/部署的并发数量
              </CardDescription>
            </CardHeader>
            <CardContent className="p-5 pt-0 space-y-4">
              <div className="flex flex-col space-y-1.5">
                <Label htmlFor="settings-token">默认 Agent 访问令牌</Label>
                <Input
                  id="settings-token"
                  type="password"
                  value={draft.token}
                  disabled={formDisabled}
                  autoComplete="off"
                  onChange={(e) => updateDraft({ token: e.target.value })}
                  placeholder="新建节点未指定令牌时默认填补此项，请使用强随机串"
                />
              </div>

              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div className="flex flex-col space-y-1.5">
                  <Label htmlFor="settings-grpc-timeout">gRPC 管控超时（秒）</Label>
                  <div className="relative">
                    <Input
                      id="settings-grpc-timeout"
                      type="number"
                      min={1}
                      max={600}
                      value={draft.timeoutSec}
                      disabled={formDisabled}
                      onChange={(e) => updateDraft({ timeoutSec: e.target.value })}
                      className={`pr-10 ${errors.timeoutSec ? 'border-destructive focus-visible:ring-destructive' : ''}`}
                    />
                    <span className="absolute right-3 top-2.5 text-xs text-muted-foreground select-none">秒</span>
                  </div>
                  {errors.timeoutSec && (
                    <p className="text-xs text-destructive font-medium">{errors.timeoutSec}</p>
                  )}
                </div>

                <div className="flex flex-col space-y-1.5">
                  <Label htmlFor="settings-max-concurrency">最大并发任务数</Label>
                  <Input
                    id="settings-max-concurrency"
                    type="number"
                    min={1}
                    max={256}
                    value={draft.concurrency}
                    disabled={formDisabled}
                    onChange={(e) => updateDraft({ concurrency: e.target.value })}
                    className={errors.concurrency ? 'border-destructive focus-visible:ring-destructive' : ''}
                  />
                  {errors.concurrency && (
                    <p className="text-xs text-destructive font-medium">{errors.concurrency}</p>
                  )}
                </div>
              </div>
            </CardContent>
          </Card>

          {/* Card 2: Services & Subscriptions */}
          <Card className="border-border bg-card">
            <CardHeader className="p-5 pb-3">
              <CardTitle className="text-sm font-semibold flex items-center gap-2">
                <Network className="h-4 w-4 text-muted-foreground" />
                服务监听与订阅公开地址
              </CardTitle>
              <CardDescription className="text-xs text-muted-foreground">
                配置面板的本地监听端口和外部客户端用以访问订阅的公开地址
              </CardDescription>
            </CardHeader>
            <CardContent className="p-5 pt-0 space-y-4">
              <div className="flex flex-col space-y-1.5">
                <Label htmlFor="settings-public-base">Public Base URL</Label>
                <Input
                  id="settings-public-base"
                  value={draft.publicBase}
                  disabled={formDisabled}
                  onChange={(e) => updateDraft({ publicBase: e.target.value })}
                  placeholder="例如: https://panel.example.com"
                  className={errors.publicBase ? 'border-destructive focus-visible:ring-destructive' : ''}
                />
                <span className="text-[10px] text-muted-foreground">用于生成安装命令中 Agent 的上报端点以及分发 Clash/sing-box 订阅解析文件的 base 根路径</span>
                {errors.publicBase && (
                  <p className="text-xs text-destructive font-medium">{errors.publicBase}</p>
                )}
              </div>

              <div className="flex flex-col space-y-1.5">
                <Label htmlFor="settings-listen-address">面板网络监听地址</Label>
                <Input
                  id="settings-listen-address"
                  value={draft.listenAddr}
                  disabled={formDisabled}
                  onChange={(e) => updateDraft({ listenAddr: e.target.value })}
                  placeholder="例如: :8080 或 127.0.0.1:8080"
                />
                <span className="text-[10px] text-muted-foreground">本配置仅保存在设置文件中。修改后，您通常需要重启后端面板程序才会正式生效</span>
              </div>
            </CardContent>
          </Card>

          {/* Card 3: Security & Credentials */}
          <Card className="border-border bg-card">
            <CardHeader className="p-5 pb-3">
              <CardTitle className="text-sm font-semibold flex items-center gap-2">
                <Shield className="h-4 w-4 text-muted-foreground" />
                安全管理
              </CardTitle>
              <CardDescription className="text-xs text-muted-foreground">
                更新后端 Web 运维控制台的管理员登录凭证
              </CardDescription>
            </CardHeader>
            <CardContent className="p-5 pt-0 space-y-4">
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div className="flex flex-col space-y-1.5">
                  <Label htmlFor="settings-new-password">新管理员密码</Label>
                  <Input
                    id="settings-new-password"
                    type="password"
                    value={newPassword}
                    disabled={formDisabled}
                    autoComplete="new-password"
                    onChange={(e) => setNewPassword(e.target.value)}
                    placeholder="留空表示不修改管理员登录密码"
                    className={errors.newPassword ? 'border-destructive focus-visible:ring-destructive' : ''}
                  />
                  {errors.newPassword && (
                    <p className="text-xs text-destructive font-medium">{errors.newPassword}</p>
                  )}
                </div>

                <div className="flex flex-col space-y-1.5">
                  <Label htmlFor="settings-confirm-password">确认新密码</Label>
                  <Input
                    id="settings-confirm-password"
                    type="password"
                    value={confirmPassword}
                    disabled={formDisabled}
                    autoComplete="new-password"
                    onChange={(e) => setConfirmPassword(e.target.value)}
                    placeholder="请再次输入以确认新管理员密码"
                    className={errors.confirmPassword ? 'border-destructive focus-visible:ring-destructive' : ''}
                  />
                  {errors.confirmPassword && (
                    <p className="text-xs text-destructive font-medium">{errors.confirmPassword}</p>
                  )}
                </div>
              </div>
            </CardContent>
          </Card>
        </div>
      ) : null}

      {/* Reload Settings Confirm Modal */}
      <ConfirmModal
        open={reloadConfirmOpen}
        title="放弃未保存的更改？"
        description="重新加载会使用服务器上的最新设置覆盖您当前修改但尚未保存的内容。"
        confirmText="确认重新加载"
        confirmVariant="destructive"
        onConfirm={confirmReloadSettings}
        onCancel={() => setReloadConfirmOpen(false)}
      />
    </div>
  )
}

function draftFromResponse(settings: SettingsResponse): SettingsDraft {
  return {
    token: settings.default_agent_token || '',
    timeoutSec: settings.grpc_timeout_sec,
    concurrency: settings.max_concurrency,
    listenAddr: settings.listen_addr || '',
    publicBase: settings.public_base_url || '',
  }
}

function sameDraft(a: SettingsDraft, b: SettingsDraft): boolean {
  return (
    a.token === b.token &&
    String(a.timeoutSec) === String(b.timeoutSec) &&
    String(a.concurrency) === String(b.concurrency) &&
    a.listenAddr === b.listenAddr &&
    a.publicBase === b.publicBase
  )
}

function validate(
  draft: SettingsDraft,
  newPassword: string,
  confirmPassword: string,
): ValidationErrors {
  return {
    timeoutSec: validateInteger(draft.timeoutSec, 1, 600, 'gRPC 超时'),
    concurrency: validateInteger(draft.concurrency, 1, 256, '最大并发任务数'),
    publicBase: validatePublicBase(draft.publicBase),
    newPassword: validatePassword(newPassword, confirmPassword),
    confirmPassword: validatePasswordConfirmation(newPassword, confirmPassword),
  }
}

function validateInteger(value: NumericDraft, min: number, max: number, label: string): string {
  if (value === '' || String(value).trim() === '') return `${label}不能为空`
  const number = Number(value)
  if (!Number.isFinite(number) || !Number.isInteger(number)) return `${label}必须是整数`
  if (number < min || number > max) return `${label}必须在 ${min} 到 ${max} 之间`
  return ''
}

function validatePublicBase(value: string): string {
  const trimmed = value.trim()
  if (!trimmed) return ''
  try {
    const url = new URL(trimmed)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') {
      return '仅支持 http:// 或 https:// 地址'
    }
    if (url.username || url.password) return '地址中不能包含用户名或密码'
    if ((url.pathname && url.pathname !== '/') || url.search || url.hash) {
      return '请填写不含路径、查询参数或片段的 Panel 根地址'
    }
  } catch {
    return '请输入完整地址，例如 https://panel.example.com'
  }
  return ''
}

function normalizePublicBase(value: string): string {
  const trimmed = value.trim()
  return trimmed ? new URL(trimmed).origin : ''
}

function validatePassword(password: string, confirmation: string): string {
  if (!password) return confirmation ? '请先填写新密码' : ''
  if (Array.from(password).length < 8) return '密码至少需要 8 个字符'
  if (new TextEncoder().encode(password).length > 72) return '密码不能超过 72 字节'
  return ''
}

function validatePasswordConfirmation(password: string, confirmation: string): string {
  if (!password) return confirmation ? '请先填写新密码' : ''
  if (!confirmation) return '请再次输入新密码'
  if (password !== confirmation) return '两次输入的密码不一致'
  return ''
}
