import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
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
import { Edit, KeyRound, Loader2, Plus, RefreshCw, ShieldCheck, Trash2, UserRound } from 'lucide-react'
import {
  createProxyUser,
  deleteProxyUser,
  listNodeAdmissions,
  listNodes,
  listProxyUsers,
  resetProxyUserCredentials,
  setNodeAdmission,
  updateProxyUser,
  type Node,
  type NodeAdmission,
  type ProxyUser,
} from '../api/client'
import { toast } from '../lib/toast'

type Editor = { open: boolean; id: string | null; name: string; note: string; enabled: boolean }

const CLOSED_EDITOR: Editor = { open: false, id: null, name: '', note: '', enabled: true }

const PUSH_HINT = '约 5 秒后自动下发到节点，受影响节点会短暂断线'

export default function Users() {
  const [users, setUsers] = useState<ProxyUser[]>([])
  const [nodes, setNodes] = useState<Node[]>([])
  const [admissions, setAdmissions] = useState<NodeAdmission[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [pending, setPending] = useState<Set<string>>(new Set())
  const [editor, setEditor] = useState<Editor>(CLOSED_EDITOR)
  const [editorError, setEditorError] = useState('')
  const [saving, setSaving] = useState(false)
  const [resetTarget, setResetTarget] = useState<ProxyUser | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<ProxyUser | null>(null)

  const admissionByNode = useMemo(
    () => new Map(admissions.map((item) => [item.node_id, item])),
    [admissions],
  )

  const load = useCallback(async () => {
    setLoading(true)
    setLoadError('')
    try {
      const [nextUsers, nextNodes, nextAdmissions] = await Promise.all([
        listProxyUsers(),
        listNodes(),
        listNodeAdmissions(),
      ])
      setUsers(nextUsers ?? [])
      setNodes(nextNodes ?? [])
      setAdmissions(nextAdmissions ?? [])
    } catch (err) {
      setLoadError(errorText(err, '用户加载失败'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  function markPending(id: string, on: boolean) {
    setPending((current) => {
      const next = new Set(current)
      if (on) next.add(id)
      else next.delete(id)
      return next
    })
  }

  function replaceUser(updated: ProxyUser) {
    setUsers((current) => current.map((item) => (item.id === updated.id ? updated : item)))
  }

  async function save() {
    const name = editor.name.trim()
    if (!name) {
      setEditorError('请填写用户名')
      return
    }
    setSaving(true)
    setEditorError('')
    try {
      if (editor.id) {
        const updated = await updateProxyUser(editor.id, { name, note: editor.note, enabled: editor.enabled })
        replaceUser(updated)
        toast.success('用户已保存')
      } else {
        const created = await createProxyUser({ name, note: editor.note, enabled: editor.enabled })
        setUsers((current) => [...current, created])
        toast.success('用户已创建，到「订阅」页为其新建订阅并绑定该用户')
      }
      setEditor(CLOSED_EDITOR)
    } catch (err) {
      setEditorError(errorText(err, '保存失败'))
    } finally {
      setSaving(false)
    }
  }

  async function toggleUser(user: ProxyUser, enabled: boolean) {
    markPending(user.id, true)
    try {
      replaceUser(await updateProxyUser(user.id, { enabled }))
      toast.success(enabled ? `用户已启用，${PUSH_HINT}` : `用户已停用，订阅链接随即失效，${PUSH_HINT}`)
    } catch (err) {
      toast.error(errorText(err, '操作失败'))
    } finally {
      markPending(user.id, false)
    }
  }

  async function confirmReset() {
    if (!resetTarget) return
    const user = resetTarget
    setResetTarget(null)
    markPending(user.id, true)
    try {
      replaceUser(await resetProxyUserCredentials(user.id))
      toast.success(`凭据已重置，该用户需刷新订阅；${PUSH_HINT}`)
    } catch (err) {
      toast.error(errorText(err, '重置失败'))
    } finally {
      markPending(user.id, false)
    }
  }

  async function confirmDelete() {
    if (!deleteTarget) return
    const user = deleteTarget
    setDeleteTarget(null)
    markPending(user.id, true)
    try {
      await deleteProxyUser(user.id)
      setUsers((current) => current.filter((item) => item.id !== user.id))
      toast.success('用户已删除')
    } catch (err) {
      toast.error(errorText(err, '删除失败'))
    } finally {
      markPending(user.id, false)
    }
  }

  async function toggleAdmission(node: Node, admissionOnly: boolean) {
    const key = `node:${node.id}`
    markPending(key, true)
    try {
      const updated = await setNodeAdmission(node.id, admissionOnly)
      setAdmissions((current) => {
        const rest = current.filter((item) => item.node_id !== updated.node_id)
        return [...rest, updated]
      })
      toast.success(
        admissionOnly
          ? `已开启仅准入用户：未绑定用户的订阅将不再包含该节点上的这些入站；${PUSH_HINT}`
          : `已关闭仅准入用户，该节点入站的共享凭据恢复可用；${PUSH_HINT}`,
      )
    } catch (err) {
      toast.error(errorText(err, '操作失败'))
    } finally {
      markPending(key, false)
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between border-b border-border pb-5">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">用户</h1>
          <p className="text-sm text-muted-foreground mt-1">
            为每个人分配独立的协议凭据（UUID / 密码），停用即可让其订阅和节点访问同时失效
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => void load()} loading={loading} className="gap-1.5">
            <RefreshCw className="h-4 w-4" /> 刷新
          </Button>
          <Button
            onClick={() => {
              setEditorError('')
              setEditor({ ...CLOSED_EDITOR, open: true })
            }}
            className="gap-1.5"
          >
            <Plus className="h-4 w-4" /> 新建用户
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
        <AlertDescription className="text-xs space-y-1">
          <p>
            用户没有登录入口，订阅链接就是全部。在「订阅」页新建订阅并选择绑定用户后，订阅里的本地节点会换成该用户自己的凭据；
            用户能访问哪些入站，由其订阅包含的入站和代理链决定。
          </p>
          <p>
            每次变更都会重新下发节点配置，sing-box 会整体重启，节点上所有连接短暂中断。外部机场节点不受影响。
          </p>
        </AlertDescription>
      </Alert>

      {loading && users.length === 0 ? (
        <div className="flex items-center justify-center py-20 gap-3 text-sm text-muted-foreground">
          <Loader2 className="h-5 w-5 animate-spin" /> 正在加载用户…
        </div>
      ) : users.length === 0 ? (
        <Card className="border-border bg-card">
          <CardContent className="py-16 text-center space-y-3">
            <UserRound className="h-10 w-10 text-muted-foreground mx-auto" />
            <h2 className="font-semibold text-foreground">暂无用户</h2>
            <p className="text-xs text-muted-foreground">新建用户后，在订阅里绑定它即可分发独立凭据。</p>
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-4">
          {users.map((user) => {
            const busy = pending.has(user.id)
            return (
              <Card key={user.id} className="border-border bg-card">
                <CardContent className="p-5 space-y-3">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        <h2 className="font-semibold text-foreground truncate">{user.name}</h2>
                        <Badge variant={user.enabled ? 'success' : 'outline'} className={user.enabled ? '' : 'text-muted-foreground'}>
                          {user.enabled ? '已启用' : '已停用'}
                        </Badge>
                      </div>
                      {user.note && <p className="text-xs text-muted-foreground mt-1 break-words">{user.note}</p>}
                      <p className="text-[11px] text-muted-foreground mt-1">
                        {user.subscription_count > 0 ? `绑定 ${user.subscription_count} 个订阅` : '尚未绑定订阅'}
                      </p>
                    </div>
                    <Switch
                      checked={user.enabled}
                      disabled={busy}
                      onCheckedChange={(value) => void toggleUser(user, value)}
                      aria-label={user.enabled ? '停用用户' : '启用用户'}
                    />
                  </div>
                  <div className="flex flex-wrap gap-1 pt-2 border-t border-border">
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={busy}
                      className="h-8 gap-1.5"
                      onClick={() => {
                        setEditorError('')
                        setEditor({ open: true, id: user.id, name: user.name, note: user.note, enabled: user.enabled })
                      }}
                    >
                      <Edit className="h-3.5 w-3.5" /> 编辑
                    </Button>
                    <Button size="sm" variant="ghost" disabled={busy} className="h-8 gap-1.5" onClick={() => setResetTarget(user)}>
                      <KeyRound className="h-3.5 w-3.5" /> 重置凭据
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={busy}
                      className="h-8 gap-1.5 text-destructive hover:text-destructive/80"
                      onClick={() => setDeleteTarget(user)}
                    >
                      <Trash2 className="h-3.5 w-3.5" /> 删除
                    </Button>
                  </div>
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}

      <Card className="border-border bg-card">
        <CardHeader className="p-5 pb-3">
          <CardTitle className="text-sm font-semibold flex items-center gap-2">
            <ShieldCheck className="h-4 w-4 text-muted-foreground" />
            节点准入
          </CardTitle>
          <CardDescription className="text-xs text-muted-foreground">
            对节点开启「仅准入用户」后，该节点上入站的共享凭据失效，只接受已绑定用户的凭据；未绑定用户的订阅不再包含这些入站。
            同一个入站在其他节点上不受影响。Shadowsocks 2022（AES）会切换为多用户模式，客户端需刷新订阅。
          </CardDescription>
        </CardHeader>
        <CardContent className="p-5 pt-0">
          {nodes.length === 0 ? (
            <p className="text-xs text-muted-foreground">暂无节点</p>
          ) : (
            <div className="divide-y divide-border rounded-md border border-border">
              {nodes.map((node) => {
                const admission = admissionByNode.get(node.id)
                const only = admission?.admission_only ?? false
                const shared = admission?.shared_inbounds ?? []
                const busy = pending.has(`node:${node.id}`)
                return (
                  <div key={node.id} className="flex items-start justify-between gap-3 px-3 py-2.5">
                    <div className="min-w-0 space-y-1">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-medium text-foreground truncate">{node.name}</span>
                        {only && <Badge variant="success" className="text-[10px]">仅准入用户</Badge>}
                      </div>
                      <p className="text-[11px] text-muted-foreground">
                        {only ? '只接受已绑定用户的凭据' : '共享凭据与用户凭据同时可用'}
                      </p>
                      {shared.length > 0 && (
                        <div className={`text-[11px] ${only ? 'text-amber-600 dark:text-amber-400' : 'text-muted-foreground'}`}>
                          {only ? '以下入站无法区分用户，仍使用共享凭据：' : '开启后以下入站无法区分用户，仍会使用共享凭据：'}
                          <ul className="mt-0.5 space-y-0.5">
                            {shared.map((inbound) => (
                              <li key={inbound.id} title={inbound.reason}>
                                <span className="font-medium">{inbound.name}</span>
                                <span className="font-mono"> ({inbound.protocol.toUpperCase()})</span>
                                <span> · {inbound.reason}</span>
                              </li>
                            ))}
                          </ul>
                        </div>
                      )}
                    </div>
                    <Switch
                      checked={only}
                      disabled={busy}
                      onCheckedChange={(value) => void toggleAdmission(node, value)}
                      aria-label={`${node.name} 仅准入用户`}
                    />
                  </div>
                )
              })}
            </div>
          )}
        </CardContent>
      </Card>

      <Dialog open={editor.open} onOpenChange={(open) => { if (!open && !saving) setEditor(CLOSED_EDITOR) }}>
        <DialogContent className="sm:max-w-md bg-background border-border text-foreground p-6 space-y-5">
          <DialogHeader>
            <DialogTitle>{editor.id ? '编辑用户' : '新建用户'}</DialogTitle>
          </DialogHeader>
          {editorError && <Alert variant="destructive"><AlertDescription>{editorError}</AlertDescription></Alert>}
          <div className="space-y-2">
            <Label htmlFor="user-name">用户名</Label>
            <Input id="user-name" value={editor.name} onChange={(event) => setEditor({ ...editor, name: event.target.value })} placeholder="例：alice" />
          </div>
          <div className="space-y-2">
            <Label htmlFor="user-note">备注（可选）</Label>
            <Input id="user-note" value={editor.note} onChange={(event) => setEditor({ ...editor, note: event.target.value })} placeholder="例：家里的路由器" />
          </div>
          <div className="flex items-center justify-between rounded-md border border-border bg-card px-3 py-2">
            <Label htmlFor="user-enabled" className="text-sm text-foreground">启用</Label>
            <Switch id="user-enabled" checked={editor.enabled} onCheckedChange={(value) => setEditor({ ...editor, enabled: value })} />
          </div>
          <DialogFooter className="gap-2 sm:gap-0">
            <Button variant="outline" disabled={saving} onClick={() => setEditor(CLOSED_EDITOR)} className="border-border">取消</Button>
            <Button loading={saving} onClick={() => void save()}>{editor.id ? '保存' : '创建用户'}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmModal
        open={!!resetTarget}
        title="重置凭据"
        description={`重置后「${resetTarget?.name || ''}」的所有 UUID / 密码都会更换，旧客户端配置立即失效，需要重新刷新订阅。`}
        confirmText="确认重置"
        confirmVariant="destructive"
        onConfirm={() => void confirmReset()}
        onCancel={() => setResetTarget(null)}
      />
      <ConfirmModal
        open={!!deleteTarget}
        title="删除用户"
        description={`确定删除「${deleteTarget?.name || ''}」吗？其订阅会解除绑定并改用共享凭据（仅准入用户节点上的入站将不再出现在这些订阅里）。`}
        confirmText="确认删除"
        confirmVariant="destructive"
        onConfirm={() => void confirmDelete()}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

function errorText(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback
}
