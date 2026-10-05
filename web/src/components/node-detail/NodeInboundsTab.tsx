import { Link } from 'react-router-dom'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { RefreshCw } from 'lucide-react'
import type {
  InboundConfig,
  NodeInboundTLSBinding,
  ManagedDomain,
  ProtocolCertificate,
} from '@/api/client'

const TLS_CERT_UNSET = '__unset__'

export interface InboundNATEdit {
  public_address: string
  public_port: number
  frp_enabled: boolean
  frpc_config: string
}

export interface NodeInboundsTabProps {
  nodeId: string
  busy: boolean
  inboundsLoading: boolean
  inboundsError: string
  allInbounds: InboundConfig[]
  inboundNAT: Record<string, InboundNATEdit>
  savedInboundNAT: Record<string, InboundNATEdit>
  tlsBindings: Record<string, NodeInboundTLSBinding>
  managedDomains: ManagedDomain[]
  protocolCertificates: ProtocolCertificate[]
  tlsBusy: string
  editPublic: string
  nodeAddress?: string
  toggleInbound: (inboundId: string) => void
  updateInboundNAT: (inboundId: string, patch: Partial<InboundNATEdit>) => void
  setTLSBindings: React.Dispatch<React.SetStateAction<Record<string, NodeInboundTLSBinding>>>
  onSaveTLS: (inboundId: string) => void
  onSaveInbounds: () => void
  retryInbounds: () => void
  supportsManagedTLS: (inbound: InboundConfig) => boolean
}

export function NodeInboundsTab({
  nodeId,
  busy,
  inboundsLoading,
  inboundsError,
  allInbounds,
  inboundNAT,
  savedInboundNAT,
  tlsBindings,
  managedDomains,
  protocolCertificates,
  tlsBusy,
  editPublic,
  nodeAddress,
  toggleInbound,
  updateInboundNAT,
  setTLSBindings,
  onSaveTLS,
  onSaveInbounds,
  retryInbounds,
  supportsManagedTLS,
}: NodeInboundsTabProps) {
  return (
    <div className="space-y-6">
      <div className="space-y-4 rounded-lg border border-border bg-card/40 p-5">
        <div className="space-y-1">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-semibold text-foreground">关联入站 + 对外暴露方式</h3>
            <Badge variant="success" className="text-[10px] px-2 py-0.5 font-medium">
              保存后自动下发并启动核心
            </Badge>
          </div>
          <p className="text-xs text-muted-foreground">
            勾选入站后可选择直接暴露（可设置订阅用公网地址和端口），或通过 FRP 暴露。保存后会自动下发 sing-box 配置并<strong>自动启动核心服务</strong>。
          </p>
        </div>

        {inboundsLoading ? (
          <div className="flex items-center justify-center p-6 space-x-2">
            <RefreshCw className="h-4 w-4 animate-spin text-muted-foreground" />
            <span className="text-sm text-muted-foreground">正在加载入站关联…</span>
          </div>
        ) : inboundsError ? (
          <Alert variant="destructive">
            <AlertTitle>入站关联暂不可用</AlertTitle>
            <AlertDescription>{inboundsError}</AlertDescription>
            <Button size="sm" variant="outline" className="mt-2 text-destructive border-destructive/30" onClick={retryInbounds}>
              重试
            </Button>
          </Alert>
        ) : allInbounds.length === 0 ? (
          <Alert className="bg-card border-border text-foreground">
            <AlertDescription>
              无入站配置。请先在{' '}
              <Link to="/inbounds" className="text-primary hover:underline underline-offset-4 font-semibold">
                入站配置管理
              </Link>{' '}
              中创建。
            </AlertDescription>
          </Alert>
        ) : (
          <div className="space-y-3">
            {allInbounds.map((inb) => {
              const listenPort = Number(inb.params?.port) || 0
              const checked = !!inboundNAT[inb.id]
              const nat = inboundNAT[inb.id]
              const frpSupported = inb.protocol !== 'hysteria2' && inb.protocol !== 'tuic'
              const attached = !!savedInboundNAT[inb.id]
              const tlsBinding = tlsBindings[inb.id] ?? {
                node_id: nodeId,
                inbound_id: inb.id,
                mode: 'legacy' as const,
              }
              const availableCertificates = protocolCertificates.filter(
                (certificate) =>
                  certificate.status === 'active' &&
                  managedDomains.some(
                    (domain) =>
                      domain.id === certificate.managed_domain_id &&
                      domain.state === 'ready',
                  ),
              )
              return (
                <div
                  key={inb.id}
                  className={`flex flex-col gap-3 p-4 rounded-lg border transition-all ${
                    checked
                      ? 'border-border bg-card/60 shadow-sm'
                      : 'border-border/40 bg-background/50 hover:border-border'
                  } ${!inb.enabled ? 'opacity-50' : ''}`}
                >
                  <div className="flex items-start justify-between">
                    <div className="flex items-center space-x-3">
                      <Checkbox
                        id={`inb-check-${inb.id}`}
                        checked={checked}
                        disabled={busy || inboundsLoading || (!inb.enabled && !checked)}
                        onCheckedChange={() => toggleInbound(inb.id)}
                      />
                      <div className="flex flex-col">
                        <Label
                          htmlFor={`inb-check-${inb.id}`}
                          className="text-sm font-semibold text-foreground flex items-center gap-2 cursor-pointer"
                        >
                          {inb.name}
                          {!inb.enabled && (
                            <Badge variant="secondary">已禁用</Badge>
                          )}
                        </Label>
                        <span className="text-xs text-muted-foreground font-mono">
                          {inb.protocol}
                          {listenPort ? ` · 协议端口 ${listenPort}` : ''}
                        </span>
                      </div>
                    </div>
                  </div>

                  {checked && (
                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 pl-7 pt-2 border-t border-border">
                      <div className="col-span-1 sm:col-span-2 space-y-3 rounded-md border border-border bg-background p-3">
                        <div className="flex items-center gap-2">
                          <Checkbox id={`inb-frp-${inb.id}`} checked={!!nat?.frp_enabled} disabled={busy || !frpSupported} onCheckedChange={(value) => updateInboundNAT(inb.id, { frp_enabled: Boolean(value) })} />
                          <Label htmlFor={`inb-frp-${inb.id}`}>通过 FRP 暴露此入站</Label>
                        </div>
                        {!frpSupported && <p className="text-xs text-muted-foreground">此协议使用 UDP/QUIC，当前仅支持 TCP 入站。</p>}
                        {nat?.frp_enabled && (
                          <div className="space-y-2 sm:col-span-2">
                            <Label htmlFor={`inb-frp-${inb.id}-config`}>完整 FRPC TOML 配置</Label>
                            <textarea
                              id={`inb-frp-${inb.id}-config`}
                              value={nat.frpc_config}
                              disabled={busy}
                              onChange={(event) => updateInboundNAT(inb.id, { frpc_config: event.target.value })}
                              rows={14}
                              spellCheck={false}
                              placeholder={'user = "s-account"\nauth.token = "..."\nserverAddr = "frp.example.com"\nserverPort = 8088\ntransport.tls.enable = false\ntransport.tls.disableCustomTLSFirstByte = false\n\n[[proxies]]\nname = "LadderPro"\ntype = "tcp"\nlocalIP = "192.168.1.10"\nlocalPort = 64192\nremotePort = 57115'}
                              className="w-full rounded-md bg-background border border-input p-3 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
                            />
                            <p className="text-xs text-muted-foreground">直接粘贴服务商给出的配置。每个入站只接受一条 TCP proxy；Ladder 会校验字段，并把 localIP/localPort 替换为 Agent 本机回环地址和自动分配的高位端口。暂不支持的字段会提示错误，不会被静默忽略。</p>
                          </div>
                        )}
                      </div>
                      {!nat?.frp_enabled && <div className="flex flex-col space-y-1.5">
                        <span className="text-xs text-muted-foreground">公网 IP / 域名</span>
                        <Input
                          disabled={busy || inboundsLoading}
                          value={nat?.public_address || ''}
                          placeholder="空 = 默认公网地址"
                          onChange={(e) =>
                            updateInboundNAT(inb.id, { public_address: e.target.value })
                          }
                          className="h-8"
                        />
                      </div>}
                      {!nat?.frp_enabled && <div className="flex flex-col space-y-1.5">
                        <span className="text-xs text-muted-foreground">公网端口</span>
                        <Input
                          type="number"
                          disabled={busy || inboundsLoading}
                          value={nat?.public_port || ''}
                          placeholder={listenPort ? String(listenPort) : '同监听口'}
                          onChange={(e) =>
                            updateInboundNAT(inb.id, {
                              public_port: Number(e.target.value) || 0,
                            })
                          }
                          className="h-8"
                        />
                      </div>}
                      {!nat?.frp_enabled && <div className="col-span-1 sm:col-span-2 text-xs text-muted-foreground font-mono mt-1">
                        订阅解析入口：
                        <code className="text-foreground bg-muted px-1 py-0.5 rounded font-mono">
                          {(nat?.public_address || editPublic || nodeAddress || '—') +
                            ':' +
                            String(
                              nat?.public_port && nat.public_port > 0
                                ? nat.public_port
                                : listenPort || '—',
                            )}
                        </code>
                      </div>}

                      {supportsManagedTLS(inb) && (
                        <div className="col-span-1 sm:col-span-2 space-y-3 rounded-md border border-border bg-background p-3">
                          <div className="flex items-center justify-between gap-3">
                            <div>
                              <div className="text-xs font-medium text-foreground">协议 TLS</div>
                              <div className="mt-1 text-xs text-muted-foreground">托管模式使用 Agent 本地私钥和自动续期证书。</div>
                            </div>
                            {!attached && <Badge variant="warning">请先保存入站关联</Badge>}
                          </div>
                          <div className="grid gap-3 sm:grid-cols-[160px_1fr_auto]">
                            <Select
                              value={tlsBinding.mode}
                              disabled={!attached || tlsBusy === inb.id}
                              onValueChange={(mode: 'legacy' | 'managed') => {
                                const first = availableCertificates[0]
                                setTLSBindings((current) => ({
                                  ...current,
                                  [inb.id]: {
                                    node_id: nodeId,
                                    inbound_id: inb.id,
                                    mode,
                                    certificate_id: mode === 'managed' ? current[inb.id]?.certificate_id || first?.id || '' : undefined,
                                  },
                                }))
                              }}
                            >
                              <SelectTrigger className="h-8 text-xs">
                                <SelectValue placeholder="配置模式" />
                              </SelectTrigger>
                              <SelectContent className="bg-popover border-border">
                                <SelectItem value="legacy">协议内置 / 手工</SelectItem>
                                <SelectItem value="managed">托管 TLS 证书</SelectItem>
                              </SelectContent>
                            </Select>

                            {tlsBinding.mode === 'managed' ? (
                              <Select
                                value={tlsBinding.certificate_id || TLS_CERT_UNSET}
                                disabled={!attached || tlsBusy === inb.id}
                                onValueChange={(certificateId) => {
                                  if (certificateId === TLS_CERT_UNSET) return
                                  setTLSBindings((current) => ({
                                    ...current,
                                    [inb.id]: {
                                      node_id: nodeId,
                                      inbound_id: inb.id,
                                      mode: 'managed',
                                      certificate_id: certificateId,
                                    },
                                  }))
                                }}
                              >
                                <SelectTrigger className="h-8 text-xs">
                                  <SelectValue placeholder="选择协议证书…" />
                                </SelectTrigger>
                                <SelectContent className="bg-popover border-border">
                                  {availableCertificates.length === 0 && (
                                    <SelectItem value={TLS_CERT_UNSET} disabled>
                                      暂无可用证书
                                    </SelectItem>
                                  )}
                                  {availableCertificates.map((cert) => (
                                    <SelectItem key={cert.id} value={cert.id}>
                                      {cert.domains.join(', ')} (r{cert.revision})
                                    </SelectItem>
                                  ))}
                                </SelectContent>
                              </Select>
                            ) : (
                              <div className="flex items-center text-xs text-muted-foreground">
                                使用入站原本的内置 TLS 或自行配置
                              </div>
                            )}

                            <Button
                              size="sm"
                              disabled={!attached || tlsBusy === inb.id || (tlsBinding.mode === 'managed' && !tlsBinding.certificate_id)}
                              loading={tlsBusy === inb.id}
                              onClick={() => onSaveTLS(inb.id)}
                              className="h-8 text-xs"
                            >
                              保存 TLS
                            </Button>
                          </div>
                        </div>
                      )}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </div>

      <div className="flex justify-end gap-2 pt-2">
        <Button onClick={onSaveInbounds} loading={busy} disabled={inboundsLoading}>
          保存关联并下发配置
        </Button>
      </div>
    </div>
  )
}
