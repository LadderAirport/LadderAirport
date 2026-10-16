import { useRef, useEffect } from 'react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import {
  Play,
  Square,
  Eye,
  Activity,
  RefreshCw,
  Wifi,
  Terminal,
} from 'lucide-react'
import type { Node, Metrics, Task, NodeInstallInfo } from '@/api/client'
import {
  formatBytes,
  coreVersion,
  isAgentOutdated,
  isOnlineStatus,
  linuxReleaseUpgradeAllowed,
  taskKindLabel,
  taskStatusLabel,
} from '@/lib/nodeDisplay'

export interface LogEntry {
  id: number
  text: string
}

export interface NodeOpsTabProps {
  node: Node
  busy: boolean
  busyAction: string | null
  installInfo: NodeInstallInfo | null
  metrics: Metrics | null
  task: Task | null
  preview: string
  logs: LogEntry[]
  streaming: boolean
  logsFollowing: boolean
  setLogsFollowing: (v: boolean) => void
  runAction: (action: 'apply' | 'start' | 'stop') => void
  onPreview: () => void
  onMetrics: () => void
  onRemoteUpgrade: () => void
  startLogs: () => void
  stopLogs: () => void
}

export function NodeOpsTab({
  node,
  busy,
  busyAction,
  installInfo,
  metrics,
  task,
  preview,
  logs,
  streaming,
  logsFollowing,
  setLogsFollowing,
  runAction,
  onPreview,
  onMetrics,
  onRemoteUpgrade,
  startLogs,
  stopLogs,
}: NodeOpsTabProps) {
  const logViewerRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    if (logsFollowing && logViewerRef.current) {
      logViewerRef.current.scrollTop = logViewerRef.current.scrollHeight
    }
  }, [logs, logsFollowing])

  const isOutdated = installInfo
    ? isAgentOutdated(coreVersion(node), installInfo.recommended_agent_version)
    : false
  const canRemoteUpgrade = linuxReleaseUpgradeAllowed(node.client_type)

  return (
    <div className="space-y-6">
      {/* Lifecycle Control */}
      <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5">
        <div className="space-y-1">
          <h3 className="text-sm font-semibold text-foreground">生命周期与管控</h3>
          <p className="text-xs text-muted-foreground">启停核心服务、预览配置、刷新性能监控指标。</p>
        </div>
        <div className="grid grid-cols-2 sm:grid-cols-3 gap-2">
          <Button
            loading={busyAction === 'start'}
            disabled={busy}
            onClick={() => runAction('start')}
            className="w-full gap-1.5"
          >
            <Play className="h-4 w-4" /> 启动服务
          </Button>
          <Button
            variant="destructive"
            loading={busyAction === 'stop'}
            disabled={busy}
            onClick={() => runAction('stop')}
            className="w-full gap-1.5"
          >
            <Square className="h-4 w-4" /> 停止服务
          </Button>
          <Button
            variant="outline"
            loading={busyAction === 'preview'}
            disabled={busy}
            onClick={onPreview}
            className="w-full border-border text-foreground hover:bg-muted gap-1.5"
          >
            <Eye className="h-4 w-4" /> 预览配置
          </Button>
          <Button
            variant="outline"
            loading={busyAction === 'metrics'}
            disabled={busy || !isOnlineStatus(node.status)}
            onClick={onMetrics}
            className="w-full border-border text-foreground hover:bg-muted gap-1.5"
          >
            <Activity className="h-4 w-4" /> 刷新监控
          </Button>
          <Button
            variant="outline"
            loading={busyAction === 'apply'}
            disabled={busy}
            onClick={() => runAction('apply')}
            className="w-full border-border text-foreground hover:bg-muted gap-1.5"
          >
            <RefreshCw className="h-4 w-4" /> 重新下发
          </Button>
          <Button
            variant="outline"
            loading={busyAction === 'upgrade'}
            disabled={busy || !canRemoteUpgrade}
            onClick={onRemoteUpgrade}
            title={
              !canRemoteUpgrade
                ? '该客户端不能使用 Linux 发布包远程升级'
                : isOutdated
                  ? '远程升级核心到最新推荐版本'
                  : '当前核心已是最新版本，再次点击可重推送升级指令'
            }
            className={`w-full border-border gap-1.5 ${
              isOutdated
                ? 'text-warning hover:bg-muted'
                : 'text-muted-foreground hover:bg-muted hover:text-foreground'
            }`}
          >
            <Wifi className="h-4 w-4" /> {isOutdated ? '远程升级' : '重新升级 (已最新)'}
          </Button>
        </div>

        {task && (
          <div className="p-3.5 bg-background rounded border border-border text-xs font-mono space-y-1 text-foreground mt-3">
            <div className="font-semibold text-foreground">
              任务: {task.id.slice(0, 8)}… — {taskKindLabel(task.type)} ({taskStatusLabel(task.status)})
            </div>
            <ul className="list-disc pl-4 space-y-0.5 text-muted-foreground mt-1">
              {(task.results || []).map((r, i) => (
                <li key={i}>
                  {r.ok ? '✓' : '✗'} {r.message}
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>

      {/* Metrics Dashboard */}
      {metrics && isOnlineStatus(node.status) && (
        <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5">
          <h3 className="text-sm font-semibold text-foreground">监控指标</h3>
          <div className="grid grid-cols-2 sm:grid-cols-5 gap-4">
            <div className="bg-background p-3 rounded-lg border border-border">
              <span className="text-[10px] text-muted-foreground block uppercase tracking-wider">活动连接</span>
              <span className="text-lg font-bold text-foreground mt-1 block">{metrics.connections}</span>
            </div>
            <div className="bg-background p-3 rounded-lg border border-border">
              <span className="text-[10px] text-muted-foreground block uppercase tracking-wider">上行流量</span>
              <span className="text-lg font-bold text-foreground mt-1 block">{formatBytes(metrics.uplink_bytes)}</span>
            </div>
            <div className="bg-background p-3 rounded-lg border border-border">
              <span className="text-[10px] text-muted-foreground block uppercase tracking-wider">下行流量</span>
              <span className="text-lg font-bold text-foreground mt-1 block">{formatBytes(metrics.downlink_bytes)}</span>
            </div>
            <div className="bg-background p-3 rounded-lg border border-border">
              <span className="text-[10px] text-muted-foreground block uppercase tracking-wider">CPU 使用率</span>
              <span className="text-lg font-bold text-foreground mt-1 block">
                {metrics.cpu_percent?.toFixed?.(1) ?? metrics.cpu_percent}%
              </span>
            </div>
            <div className="bg-background p-3 rounded-lg border border-border col-span-2 sm:col-span-1">
              <span className="text-[10px] text-muted-foreground block uppercase tracking-wider">内存占用</span>
              <span className="text-lg font-bold text-foreground mt-1 block">{formatBytes(metrics.memory_rss_bytes)}</span>
            </div>
          </div>
        </div>
      )}

      {/* Preview configuration */}
      {preview && (
        <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5">
          <h3 className="text-sm font-semibold text-foreground">配置预览</h3>
          <pre className="p-4 rounded-lg bg-background border border-border text-xs font-mono text-foreground max-h-[360px] overflow-y-auto leading-relaxed whitespace-pre-wrap break-all">
            {preview}
          </pre>
        </div>
      )}

      {/* Live logs console */}
      <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5">
        <div className="flex flex-wrap gap-4 items-center justify-between pb-3 border-b border-border">
          <div className="space-y-1">
            <h3 className="text-sm font-semibold text-foreground flex items-center gap-1.5">
              <Terminal className="h-4 w-4 text-muted-foreground" />
              节点日志
            </h3>
            <p className="text-xs text-muted-foreground">实时流式拉取 Agent 日志。</p>
          </div>
          <div className="flex items-center gap-4">
            <div className="flex items-center space-x-2">
              <Checkbox
                id="logs-follow-chk"
                checked={logsFollowing}
                disabled={logs.length === 0 && !streaming}
                onCheckedChange={(checked) => setLogsFollowing(Boolean(checked))}
              />
              <Label htmlFor="logs-follow-chk" className="text-xs text-muted-foreground cursor-pointer">
                跟随日志
              </Label>
            </div>

            {!streaming ? (
              <Button size="sm" onClick={startLogs} className="gap-1.5 h-8 px-3">
                <Play className="h-3.5 w-3.5" /> 开启
              </Button>
            ) : (
              <Button
                size="sm"
                variant="outline"
                onClick={stopLogs}
                className="border-border text-foreground hover:bg-muted gap-1.5 h-8 px-3"
              >
                <Square className="h-3.5 w-3.5" /> 停止
              </Button>
            )}
          </div>
        </div>

        {/* Log viewer output */}
        <div
          ref={logViewerRef}
          className="p-4 rounded-lg bg-muted border border-border overflow-y-auto text-xs font-mono text-foreground h-[300px] leading-relaxed focus:outline-none"
          role="log"
          aria-label="节点实时日志"
          aria-relevant="additions"
          tabIndex={0}
          onScroll={(event) => {
            const viewer = event.currentTarget
            const distance = viewer.scrollHeight - viewer.scrollTop - viewer.clientHeight
            setLogsFollowing(distance < 24)
          }}
        >
          {logs.length === 0 ? (
            <div className="flex items-center justify-center h-full text-muted-foreground">
              {streaming ? '正在等待日志输出…' : '点击“开启”连接日志流'}
            </div>
          ) : (
            logs.map((line) => (
              <div key={line.id} className="border-b border-border/30 py-0.5 hover:bg-background/40 whitespace-pre-wrap">
                {line.text}
              </div>
            ))
          )}
        </div>
      </div>
    </div>
  )
}
