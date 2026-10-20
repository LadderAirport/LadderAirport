import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from './ui/dialog'
import { Button } from './ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from './ui/tabs'
import { Badge } from './ui/badge'
import { StatusBadge } from './ui/status-badge'
import { Alert, AlertDescription, AlertTitle } from './ui/alert'
import { ConfirmModal } from './ui/confirm-modal'
import { RefreshCw } from 'lucide-react'
import {
  applyNode,
  getNodeInstallCommand,
  getNodeInboundTLS,
  getNodeMetrics,
  getNodeFRPS,
  getNodeFRPSMappings,
  listInbounds,
  listNodeInbounds,
  listNodeInterfaces,
  listManagedDomains,
  listNodes,
  listProtocolCertificates,
  previewNodeConfig,
  setNodeInboundBindings,
  startNode,
  stopNode,
  streamNodeLogs,
  putNodeInboundTLS,
  putNodeFRPS,
  getNodeFRPSStatus,
  startNodeFRPS,
  stopNodeFRPS,
  revealNodeFRPSToken,
  updateNode,
  upgradeNode,
  type InboundConfig,
  type FRPServerConfig,
  type FRPServerMappings,
  type PutFRPServerConfigInput,
  type Metrics,
  type NetworkInterface,
  type Node,
  type NodeInboundBinding,
  type NodeInstallInfo,
  type NodeInboundTLSBinding,
  type ManagedDomain,
  type ProtocolCertificate,
  type Task,
  type UpdateNodeInput,
} from '../api/client'
import {
  clientVersionLine,
  coreVersion,
  isAgentOutdated,
  linuxReleaseUpgradeAllowed,
  nodeEndpointLabel,
  runtimeLabel,
  runtimeTheme,
  statusLabel,
  statusTheme,
  taskStatusLabel,
} from '../lib/nodeDisplay'
import { copyText } from '../lib/clipboard'
import { toast } from '../lib/toast'
import { NodeOverviewTab } from './node-detail/NodeOverviewTab'
import { NodeDNSTab } from './node-detail/NodeDNSTab'
import { NodeInboundsTab, type InboundNATEdit } from './node-detail/NodeInboundsTab'
import { NodeFRPSTab } from './node-detail/NodeFRPSTab'
import { NodeSystemTab } from './node-detail/NodeSystemTab'
import { NodeOpsTab, type LogEntry } from './node-detail/NodeOpsTab'

type Props = {
  nodeId: string | null
  onClose: () => void
  onChanged: () => void
}

type LoadOptions = {
  syncConnection?: boolean
  syncInbounds?: boolean
  fatal?: boolean
}

type DrawerAction =
  | 'save-connection'
  | 'save-inbounds'
  | 'preview'
  | 'apply'
  | 'start'
  | 'stop'
  | 'metrics'
  | 'upgrade'
  | 'install'
  | 'save-frps'
  | 'start-frps'
  | 'stop-frps'
  | 'refresh-frps'
  | 'reveal-frps-token'

export type ConnectionErrors = Partial<
  Record<'name' | 'address' | 'grpcPort' | 'publicAddress', string>
>

export function hostValidationError(value: string, label: string): string {
  const host = value.trim()
  if (!host) return ''
  if (host.includes('://')) return `${label}只填写主机名或 IP，不要包含协议`
  if (/[\s/?#@]/.test(host)) return `${label}包含无效字符`
  if (host.startsWith('[') || host.endsWith(']')) return `${label}中的 IPv6 地址无需方括号`

  if (host.includes(':')) {
    const [address, zone, ...extra] = host.split('%')
    const colonCount = (address.match(/:/g) || []).length
    if (
      extra.length > 0 ||
      colonCount < 2 ||
      !/^[0-9a-f:.]+$/i.test(address) ||
      (zone !== undefined && !/^[a-z0-9_.-]+$/i.test(zone))
    ) {
      return `${label}不是有效的 IPv6 地址`
    }
    return ''
  }

  if (host.length > 253 || !/^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$/i.test(host)) {
    return `${label}不是有效的主机名或 IP`
  }
  return ''
}

function supportsManagedTLS(inbound: InboundConfig): boolean {
  if (['trojan', 'hysteria2', 'tuic', 'anytls'].includes(inbound.protocol)) return true
  if (['vless', 'vmess'].includes(inbound.protocol)) return inbound.params?.tls_mode !== 'reality'
  return false
}

function frpcDraft(raw: string): string {
  if (!raw.trimStart().startsWith('{')) return raw
  try {
    const config = JSON.parse(raw) as Record<string, unknown>
    const lines = [
      `user = ${JSON.stringify(String(config.user ?? ''))}`,
      `auth.token = ${JSON.stringify(String(config.token ?? ''))}`,
      `serverAddr = ${JSON.stringify(String(config.server_addr ?? ''))}`,
      `serverPort = ${Number(config.server_port) || 7000}`,
    ]
    if (typeof config.tls_enable === 'boolean') lines.push(`transport.tls.enable = ${config.tls_enable}`)
    if (typeof config.tls_disable_custom_tls_first_byte === 'boolean') {
      lines.push(`transport.tls.disableCustomTLSFirstByte = ${config.tls_disable_custom_tls_first_byte}`)
    }
    lines.push('', '[[proxies]]')
    if (config.proxy_name) lines.push(`name = ${JSON.stringify(String(config.proxy_name))}`)
    lines.push('type = "tcp"', `remotePort = ${Number(config.remote_port) || 0}`)
    return lines.join('\n')
  } catch {
    return raw
  }
}

export default function NodeDetailDrawer({ nodeId, onClose, onChanged }: Props) {
  const open = !!nodeId
  const id = nodeId ?? ''

  const [node, setNode] = useState<Node | null>(null)
  const [allInbounds, setAllInbounds] = useState<InboundConfig[]>([])
  const [inboundNAT, setInboundNAT] = useState<Record<string, InboundNATEdit>>({})
  const [savedInboundNAT, setSavedInboundNAT] = useState<Record<string, InboundNATEdit>>({})
  const [inboundsLoading, setInboundsLoading] = useState(false)
  const [inboundsError, setInboundsError] = useState('')
  const [tlsBindings, setTLSBindings] = useState<Record<string, NodeInboundTLSBinding>>({})
  const [managedDomains, setManagedDomains] = useState<ManagedDomain[]>([])
  const [protocolCertificates, setProtocolCertificates] = useState<ProtocolCertificate[]>([])
  const [tlsBusy, setTLSBusy] = useState('')
  const [preview, setPreview] = useState('')
  const [metrics, setMetrics] = useState<Metrics | null>(null)
  const [task, setTask] = useState<Task | null>(null)
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [streaming, setStreaming] = useState(false)
  const [logsFollowing, setLogsFollowing] = useState(true)
  const [busyAction, setBusyAction] = useState<DrawerAction | null>(null)
  const busy = busyAction !== null
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState('')
  const [activeTab, setActiveTab] = useState<string>('connection')
  const abortRef = useRef<AbortController | null>(null)
  const copyTimersRef = useRef<number[]>([])
  const nodeIdRef = useRef<string | null>(nodeId)
  const generationRef = useRef(0)
  const loadRequestRef = useRef(0)
  const inboundsRequestRef = useRef(0)
  const interfacesRequestRef = useRef(0)
  const frpsMappingsRequestRef = useRef(0)
  const logRequestRef = useRef(0)
  const logSequenceRef = useRef(0)
  nodeIdRef.current = nodeId

  const [editName, setEditName] = useState('')
  const [editLabels, setEditLabels] = useState<string[]>([])
  const [editLabelDraft, setEditLabelDraft] = useState('')
  const [editToken, setEditToken] = useState('')
  const [editTokenChanged, setEditTokenChanged] = useState(false)
  const [tokenVisible, setTokenVisible] = useState(false)
  const [editAddress, setEditAddress] = useState('')
  const [editPort, setEditPort] = useState<number | string>(50051)
  const [editPublic, setEditPublic] = useState('')
  const [editEgress, setEditEgress] = useState('')
  const [editDDNS, setEditDDNS] = useState(true)
  const [dnsDirty, setDNSDirty] = useState(false)
  const [editControlMode, setEditControlMode] = useState<'push' | 'uplink'>('push')
  const [connectionErrors, setConnectionErrors] = useState<ConnectionErrors>({})
  const [ifaces, setIfaces] = useState<NetworkInterface[]>([])
  const [installInfo, setInstallInfo] = useState<NodeInstallInfo | null>(null)
  const [copied, setCopied] = useState(false)
  const [copiedUpgrade, setCopiedUpgrade] = useState(false)
  const [frps, setFRPS] = useState<FRPServerConfig | null>(null)
  const [frpsDraft, setFRPSDraft] = useState<PutFRPServerConfigInput | null>(null)
  const [frpsLoading, setFRPSLoading] = useState(false)
  const [frpsError, setFRPSError] = useState('')
  const [frpsMappings, setFRPSMappings] = useState<FRPServerMappings | null>(null)
  const [frpsMappingsLoading, setFRPSMappingsLoading] = useState(false)
  const [frpsMappingsError, setFRPSMappingsError] = useState('')
  const [closeConfirm, setCloseConfirm] = useState(false)

  useEffect(() => {
    if (!open) setCloseConfirm(false)
  }, [open])

  const isCurrentNode = useCallback(
    (targetId: string, generation: number) =>
      nodeIdRef.current === targetId && generationRef.current === generation,
    [],
  )

  const loadInterfaces = useCallback(async (targetId: string, generation: number) => {
    if (!targetId) return
    const request = ++interfacesRequestRef.current
    try {
      const res = await listNodeInterfaces(targetId)
      if (!isCurrentNode(targetId, generation) || request !== interfacesRequestRef.current) return
      setIfaces(res.interfaces ?? [])
    } catch {
      if (!isCurrentNode(targetId, generation) || request !== interfacesRequestRef.current) return
      setIfaces([])
    }
  }, [isCurrentNode])

  const loadFRPSMappings = useCallback(async (targetId: string, generation: number) => {
    if (!targetId) return
    const request = ++frpsMappingsRequestRef.current
    if (isCurrentNode(targetId, generation)) {
      setFRPSMappingsLoading(true)
      setFRPSMappingsError('')
    }
    try {
      const mappings = await getNodeFRPSMappings(targetId)
      if (!isCurrentNode(targetId, generation) || request !== frpsMappingsRequestRef.current) return
      setFRPSMappings(mappings)
    } catch (err) {
      if (!isCurrentNode(targetId, generation) || request !== frpsMappingsRequestRef.current) return
      setFRPSMappingsError(err instanceof Error ? err.message : '无法刷新 FRPS 在线列表')
    } finally {
      if (isCurrentNode(targetId, generation) && request === frpsMappingsRequestRef.current) {
        setFRPSMappingsLoading(false)
      }
    }
  }, [isCurrentNode])

  const loadFRPS = useCallback(async (targetId: string, generation: number) => {
    if (!targetId) return
    if (isCurrentNode(targetId, generation)) {
      setFRPSLoading(true)
      setFRPSError('')
    }
    try {
      const config = await getNodeFRPS(targetId)
      if (!isCurrentNode(targetId, generation)) return
      setFRPS(config)
      setFRPSDraft({
        enabled: config.enabled,
        bind_addr: config.bind_addr || '0.0.0.0',
        bind_port: config.bind_port || 7000,
        proxy_bind_addr: config.proxy_bind_addr || '0.0.0.0',
        allow_ports: config.allow_ports || [],
        tls_force: config.tls_force ?? true,
        max_ports_per_client: config.max_ports_per_client || 0,
        managed_domain_id: config.managed_domain_id || '',
      })
      if (config.runtime_state === 'running') {
        void loadFRPSMappings(targetId, generation)
      }
    } catch (err) {
      if (!isCurrentNode(targetId, generation)) return
      setFRPS(null)
      setFRPSDraft(null)
      setFRPSError(err instanceof Error ? err.message : '无法拉取 FRPS 配置')
    } finally {
      if (isCurrentNode(targetId, generation)) {
        setFRPSLoading(false)
      }
    }
  }, [isCurrentNode, loadFRPSMappings])

  const syncConnectionState = useCallback((targetNode: Node) => {
    setEditName(targetNode.name || '')
    setEditLabels(targetNode.labels ? [...targetNode.labels] : [])
    setEditLabelDraft('')
    setEditToken(targetNode.token || '')
    setEditTokenChanged(false)
    setTokenVisible(false)
    setEditAddress(targetNode.address || '')
    setEditPort(targetNode.grpc_port || 50051)
    setEditPublic(targetNode.public_address || '')
    setEditEgress(targetNode.egress_interface || '')
    setEditDDNS(targetNode.ddns_enabled ?? true)
    setEditControlMode(targetNode.control_mode === 'uplink' ? 'uplink' : 'push')
    setConnectionErrors({})
  }, [])

  const loadInboundsState = useCallback(async (targetId: string, generation: number) => {
    const request = ++inboundsRequestRef.current
    if (isCurrentNode(targetId, generation)) {
      setInboundsLoading(true)
      setInboundsError('')
    }
    try {
      const [allList, attachedList, managedDomainsRes, protocolCertsRes] = await Promise.all([
        listInbounds(),
        listNodeInbounds(targetId),
        listManagedDomains(),
        listProtocolCertificates(),
      ])
      if (!isCurrentNode(targetId, generation) || request !== inboundsRequestRef.current) return
      setAllInbounds(allList)
      setManagedDomains(managedDomainsRes)
      setProtocolCertificates(protocolCertsRes)

      const natMap: Record<string, InboundNATEdit> = {}
      const tlsMap: Record<string, NodeInboundTLSBinding> = {}
      for (const item of attachedList) {
        natMap[item.id] = {
          public_address: item.public_address || '',
          public_port: item.public_port || 0,
          frp_enabled: item.frp_enabled || false,
          frpc_config: frpcDraft(item.frpc_config || ''),
        }
      }
      const tlsResults = await Promise.all(
        attachedList.map((item) =>
          getNodeInboundTLS(targetId, item.id).catch(() => null),
        ),
      )
      if (!isCurrentNode(targetId, generation) || request !== inboundsRequestRef.current) return
      attachedList.forEach((item, index) => {
        const binding = tlsResults[index]
        if (binding) tlsMap[item.id] = binding
      })
      setInboundNAT(natMap)
      setSavedInboundNAT(JSON.parse(JSON.stringify(natMap)) as Record<string, InboundNATEdit>)
      setTLSBindings(tlsMap)
    } catch (err) {
      if (!isCurrentNode(targetId, generation) || request !== inboundsRequestRef.current) return
      setInboundsError(err instanceof Error ? err.message : '无法拉取入站与 TLS 配置')
    } finally {
      if (isCurrentNode(targetId, generation) && request === inboundsRequestRef.current) {
        setInboundsLoading(false)
      }
    }
  }, [isCurrentNode])

  const load = useCallback(
    async (targetId: string, generation: number, opts: LoadOptions = {}) => {
      const { syncConnection = true, syncInbounds = true, fatal = false } = opts
      const request = ++loadRequestRef.current
      if (fatal && isCurrentNode(targetId, generation)) {
        setLoading(true)
        setLoadError('')
      }
      try {
        const nodes = await listNodes()
        if (!isCurrentNode(targetId, generation) || request !== loadRequestRef.current) return
        const found = nodes.find((n) => n.id === targetId)
        if (!found) {
          setLoadError('节点已被删除或不存在')
          setNode(null)
          return
        }
        setNode(found)
        if (syncConnection) syncConnectionState(found)

        if (syncInbounds) {
          void loadInboundsState(targetId, generation)
        }
        void loadFRPS(targetId, generation)
        void loadInterfaces(targetId, generation)
      } catch (err) {
        if (!isCurrentNode(targetId, generation) || request !== loadRequestRef.current) return
        if (fatal) {
          setLoadError(err instanceof Error ? err.message : '数据拉取失败')
          setNode(null)
        }
      } finally {
        if (fatal && isCurrentNode(targetId, generation) && request === loadRequestRef.current) {
          setLoading(false)
        }
      }
    },
    [isCurrentNode, loadFRPS, loadInboundsState, loadInterfaces, syncConnectionState],
  )

  useEffect(() => {
    generationRef.current += 1
    const currentGen = generationRef.current

    if (abortRef.current) {
      abortRef.current.abort()
      abortRef.current = null
    }

    setStreaming(false)
    setLogs([])
    setLogsFollowing(true)
    setPreview('')
    setMetrics(null)
    setTask(null)
    setInstallInfo(null)

    if (nodeId) {
      setActiveTab('connection')
      void load(nodeId, currentGen, { fatal: true })
      void getNodeInstallCommand(nodeId).then((info) => {
        if (isCurrentNode(nodeId, currentGen)) setInstallInfo(info)
      }).catch(() => {})
    } else {
      setNode(null)
    }

    return () => {
      if (abortRef.current) {
        abortRef.current.abort()
        abortRef.current = null
      }
    }
  }, [nodeId, load, isCurrentNode])

  useEffect(() => {
    const timers = copyTimersRef.current
    return () => {
      for (const timer of timers) window.clearTimeout(timer)
    }
  }, [])

  const clearConnectionError = (field: keyof ConnectionErrors) => {
    setConnectionErrors((current) => {
      if (!current[field]) return current
      const next = { ...current }
      delete next[field]
      return next
    })
  }

  const onSaveConnection = async () => {
    if (busy || !node) return
    const name = editName.trim()
    const address = editAddress.trim()
    const publicAddress = editPublic.trim()
    const port = Number(editPort)

    const errors: ConnectionErrors = {}
    if (!name) errors.name = '请填写节点名称'
    if (editControlMode !== 'uplink') {
      const addrErr = hostValidationError(address, '控制面地址')
      if (addrErr) errors.address = addrErr
    }
    const pubErr = hostValidationError(publicAddress, '默认公网地址')
    if (pubErr) errors.publicAddress = pubErr
    if (editControlMode !== 'uplink' && (!Number.isInteger(port) || port < 1 || port > 65535)) {
      errors.grpcPort = 'gRPC 端口必须在 1 到 65535 之间'
    }

    if (Object.keys(errors).length > 0) {
      setConnectionErrors(errors)
      toast.warning('请修正表单中的错误后再保存')
      return
    }

    setConnectionErrors({})
    setBusyAction('save-connection')
    const currentGen = generationRef.current
    try {
      const input: UpdateNodeInput = {
        name,
        labels: editLabels,
        public_address: publicAddress || undefined,
        egress_interface: editEgress || undefined,
        ddns_enabled: editDDNS,
        control_mode: editControlMode,
      }
      if (editControlMode !== 'uplink') {
        input.address = address || undefined
        if (Number.isInteger(port) && port >= 1 && port <= 65535) {
          input.grpc_port = port
        }
      }
      if (editTokenChanged) input.token = editToken
      const updated = await updateNode(id, input)
      if (isCurrentNode(id, currentGen)) {
        setNode(updated)
        setEditToken(updated.token || '')
        setEditTokenChanged(false)
      }
      toast.success('节点连接设置保存成功')
      onChanged()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '保存失败')
    } finally {
      if (isCurrentNode(id, currentGen)) setBusyAction(null)
    }
  }

  const toggleInbound = (inboundId: string) => {
    setInboundNAT((current) => {
      const next = { ...current }
      if (next[inboundId]) delete next[inboundId]
      else next[inboundId] = { public_address: '', public_port: 0, frp_enabled: false, frpc_config: '' }
      return next
    })
  }

  const updateInboundNAT = (inboundId: string, patch: Partial<InboundNATEdit>) => {
    setInboundNAT((current) => {
      const active = current[inboundId]
      if (!active) return current
      return { ...current, [inboundId]: { ...active, ...patch } }
    })
  }

  const onSaveTLS = async (inboundId: string) => {
    const binding = tlsBindings[inboundId]
    if (!binding || busy) return
    if (binding.mode === 'managed' && !binding.certificate_id) {
      toast.warning('请先选择一张可用的托管证书')
      return
    }
    setTLSBusy(inboundId)
    try {
      const certificate = binding.mode === 'managed'
        ? protocolCertificates.find((item) => item.id === binding.certificate_id)
        : undefined
      await putNodeInboundTLS(id, inboundId, {
        mode: binding.mode,
        certificate_id: binding.mode === 'managed' ? binding.certificate_id : undefined,
        managed_domain_id: certificate?.managed_domain_id,
      })
      toast.success('协议 TLS 配置已保存')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '保存 TLS 配置失败')
    } finally {
      setTLSBusy('')
    }
  }

  const onSaveInbounds = async () => {
    if (busy || inboundsLoading || !node) return
    for (const [inboundId, binding] of Object.entries(inboundNAT)) {
      if (!binding.frp_enabled) continue
      if (!binding.frpc_config.trim()) {
        toast.warning(`入站「${allInbounds.find((item) => item.id === inboundId)?.name ?? inboundId}」需要填写完整 FRPC TOML 配置`)
        return
      }
    }
    setBusyAction('save-inbounds')
    const currentGen = generationRef.current
    const bindings: NodeInboundBinding[] = Object.entries(inboundNAT).map(
      ([inboundId, nat]) => {
        return {
          inbound_id: inboundId,
          public_address: nat.public_address.trim() || undefined,
          public_port: Number(nat.public_port) || undefined,
          frp_enabled: nat.frp_enabled,
          frpc_config: nat.frp_enabled ? nat.frpc_config.trim() : '',
        }
      },
    )
    try {
      const res = await setNodeInboundBindings(id, bindings)
      if (isCurrentNode(id, currentGen)) {
        if (res.apply_task) setTask(res.apply_task)
        else if (res.start_task) setTask(res.start_task)
        setSavedInboundNAT(JSON.parse(JSON.stringify(inboundNAT)) as Record<string, InboundNATEdit>)
      }
      const base = res.deploy_message || (res.deployed ? '关联已保存，配置已下发且核心服务已自动拉起' : '关联已保存')
      if (!res.deployed && res.apply_task?.status === 'failed') {
        toast.error(base)
      } else {
        toast.success(base)
      }
      void load(id, currentGen, { syncConnection: false, syncInbounds: false })
      onChanged()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '保存失败')
    } finally {
      if (isCurrentNode(id, currentGen)) setBusyAction(null)
    }
  }

  const updateFRPSDraft = (patch: Partial<PutFRPServerConfigInput>) => {
    setFRPSDraft((current) => (current ? { ...current, ...patch } : current))
  }

  const runAction = async (action: 'apply' | 'start' | 'stop') => {
    if (busy) return
    setBusyAction(action)
    const currentGen = generationRef.current
    try {
      const fn = action === 'apply' ? applyNode : action === 'start' ? startNode : stopNode
      const resTask = await fn(id)
      if (isCurrentNode(id, currentGen)) setTask(resTask)
      const ok = resTask.results?.[0]?.ok
      const text = `${action === 'apply' ? '配置下发' : action === 'start' ? '启动' : '停止'} ${
        ok ? (action === 'apply' ? '成功（核心已自动拉起）' : '成功') : '失败'
      }: ${resTask.results?.[0]?.message || taskStatusLabel(resTask.status)}`
      if (ok) toast.success(text)
      else toast.error(text)
      void load(id, currentGen, { syncConnection: false, syncInbounds: false })
      onChanged()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '操作失败')
    } finally {
      if (isCurrentNode(id, currentGen)) setBusyAction(null)
    }
  }

  const frpsDirty = Boolean(
    frpsDraft &&
      frps &&
      (frpsDraft.enabled !== frps.enabled ||
        frpsDraft.bind_addr !== (frps.bind_addr || '0.0.0.0') ||
        frpsDraft.bind_port !== (frps.bind_port || 7000) ||
        frpsDraft.proxy_bind_addr !== (frps.proxy_bind_addr || '0.0.0.0') ||
        frpsDraft.tls_force !== (frps.tls_force ?? true) ||
        frpsDraft.max_ports_per_client !== (frps.max_ports_per_client || 0) ||
        (frpsDraft.managed_domain_id || '') !== (frps.managed_domain_id || '') ||
        JSON.stringify(frpsDraft.allow_ports) !== JSON.stringify(frps.allow_ports || [])),
  )

  const saveFRPS = async () => {
    if (busy || !frpsDraft) return
    setBusyAction('save-frps')
    const currentGen = generationRef.current
    try {
      const updated = await putNodeFRPS(id, frpsDraft)
      if (isCurrentNode(id, currentGen)) {
        setFRPS(updated)
        setFRPSDraft({
          enabled: updated.enabled,
          bind_addr: updated.bind_addr || '0.0.0.0',
          bind_port: updated.bind_port || 7000,
          proxy_bind_addr: updated.proxy_bind_addr || '0.0.0.0',
          allow_ports: updated.allow_ports || [],
          tls_force: updated.tls_force ?? true,
          max_ports_per_client: updated.max_ports_per_client || 0,
          managed_domain_id: updated.managed_domain_id || '',
        })
      }
      toast.success('FRPS Server 配置保存并下发成功')
      onChanged()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '保存 FRPS 配置失败')
    } finally {
      if (isCurrentNode(id, currentGen)) setBusyAction(null)
    }
  }

  const runFRPSAction = async (action: 'refresh' | 'start' | 'stop') => {
    if (busy) return
    setBusyAction(`${action}-frps` as DrawerAction)
    const currentGen = generationRef.current
    try {
      let updated: FRPServerConfig
      if (action === 'start') updated = await startNodeFRPS(id)
      else if (action === 'stop') updated = await stopNodeFRPS(id)
      else updated = await getNodeFRPSStatus(id)

      if (isCurrentNode(id, currentGen)) {
        setFRPS(updated)
      }
      toast.success(`FRPS ${action === 'start' ? '启动' : action === 'stop' ? '停止' : '状态刷新'}成功`)
      onChanged()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '操作失败')
    } finally {
      if (isCurrentNode(id, currentGen)) setBusyAction(null)
    }
  }

  const revealFRPSToken = async (): Promise<string> => {
    const res = await revealNodeFRPSToken(id)
    return res.auth_token
  }

  const rotateFRPSToken = async () => {
    if (busy || !frpsDraft) return
    setBusyAction('save-frps')
    const currentGen = generationRef.current
    try {
      const updated = await putNodeFRPS(id, { ...frpsDraft, rotate_auth_token: true })
      if (isCurrentNode(id, currentGen)) {
        setFRPS(updated)
      }
      toast.success('FRPS 认证 Token 已生成新密钥，旧客户端必须同步更新 Token')
      onChanged()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '轮换 Token 失败')
    } finally {
      if (isCurrentNode(id, currentGen)) setBusyAction(null)
    }
  }

  const onPreview = async () => {
    if (busy) return
    setBusyAction('preview')
    try {
      const res = await previewNodeConfig(id)
      const formatted = typeof res === 'string' ? res : JSON.stringify(res, null, 2)
      setPreview(formatted || '')
      toast.success('配置生成成功')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '预览失败')
    } finally {
      setBusyAction(null)
    }
  }

  const onMetrics = async () => {
    if (busy) return
    setBusyAction('metrics')
    try {
      const res = await getNodeMetrics(id)
      setMetrics(res)
      toast.success('监控数据已刷新')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '刷新失败')
    } finally {
      setBusyAction(null)
    }
  }

  const onRemoteUpgrade = async () => {
    if (busy) return
    if (node && !linuxReleaseUpgradeAllowed(node.client_type)) {
      toast.error('该节点是 Android 或 OpenWrt 客户端，不能使用 Linux 发布包远程升级')
      return
    }
    setBusyAction('upgrade')
    try {
      const res = await upgradeNode(id)
      if (res.ok) toast.success(res.message || '远程升级指令已成功下发')
      else toast.error(`远程升级失败: ${res.message || '未知原因'}`)
      onChanged()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '升级请求失败')
    } finally {
      setBusyAction(null)
    }
  }

  const startLogs = async () => {
    setStreaming(true)
    setLogs([])
    setLogsFollowing(true)
    const currentGen = generationRef.current
    const request = ++logRequestRef.current

    if (abortRef.current) abortRef.current.abort()
    const ac = new AbortController()
    abortRef.current = ac

    try {
      await streamNodeLogs(id, {
        tail: 100,
        signal: ac.signal,
        onLine: (line) => {
          const ts = line.ts ? new Date(line.ts).toISOString() : new Date().toISOString()
          setLogs((prev) => {
            const nextId = ++logSequenceRef.current
            const list = [...prev, { id: nextId, text: `[${ts}] ${line.level || 'info'} ${line.message}` }]
            return list.length > 600 ? list.slice(list.length - 500) : list
          })
        },
      })
    } catch (err) {
      if (request === logRequestRef.current && isCurrentNode(id, currentGen) && (err as Error).name !== 'AbortError') {
        toast.error(err instanceof Error ? err.message : '日志流连接失败')
      }
    } finally {
      if (request === logRequestRef.current && isCurrentNode(id, currentGen)) {
        setStreaming(false)
        abortRef.current = null
      }
    }
  }

  const stopLogs = () => {
    setStreaming(false)
    if (abortRef.current) {
      abortRef.current.abort()
      abortRef.current = null
    }
  }

  const copyInstallCommand = async () => {
    if (!installInfo?.install_command) return
    try {
      await copyText(installInfo.install_command)
      setCopied(true)
      toast.success('安装命令已复制')
      copyTimersRef.current.push(window.setTimeout(() => setCopied(false), 2000))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '复制失败')
    }
  }

  const copyUpgradeCommand = async () => {
    if (!installInfo?.upgrade_command) return
    try {
      await copyText(installInfo.upgrade_command)
      setCopiedUpgrade(true)
      toast.success('升级命令已复制')
      copyTimersRef.current.push(window.setTimeout(() => setCopiedUpgrade(false), 2000))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '复制失败')
    }
  }

  const connectionDirty = Boolean(
    node &&
      (editName.trim() !== (node.name || '') ||
        editAddress.trim() !== (node.address || '') ||
        Number(editPort) !== (node.grpc_port || 50051) ||
        editPublic.trim() !== (node.public_address || '') ||
        editEgress !== (node.egress_interface || '') ||
        editDDNS !== (node.ddns_enabled !== false) ||
        editControlMode !== (node.control_mode || 'push') ||
        editTokenChanged ||
        JSON.stringify(editLabels) !== JSON.stringify(node.labels || [])),
  )
  const inboundsDirty = JSON.stringify(inboundNAT) !== JSON.stringify(savedInboundNAT)
  const drawerDirty = connectionDirty || inboundsDirty || frpsDirty || dnsDirty

  const requestClose = () => {
    if (busy) return
    if (drawerDirty) {
      setCloseConfirm(true)
      return
    }
    onClose()
  }

  const attachedCount = Object.keys(inboundNAT).length
  const mappingCount = Object.values(inboundNAT).filter(
    (nat) => nat.public_address.trim() || nat.public_port > 0,
  ).length

  return (
    <>
    <Dialog open={open} onOpenChange={(v) => { if (!v) requestClose() }}>
      <DialogContent className="max-w-4xl w-[92vw] h-[85vh] max-h-[85vh] bg-background border-border text-foreground p-0 flex flex-col focus-visible:outline-none overflow-hidden sm:rounded-lg shadow-2xl">
        {loading ? (
          <div className="flex flex-col items-center justify-center flex-1 space-y-3">
            <RefreshCw className="h-6 w-6 animate-spin text-muted-foreground" />
            <p className="text-sm text-muted-foreground">正在加载节点详情…</p>
          </div>
        ) : loadError || !node ? (
          <div className="flex-1 p-6 flex flex-col justify-between">
            <div className="space-y-4">
              <Alert variant="destructive">
                <AlertTitle>无法打开节点详情</AlertTitle>
                <AlertDescription>{loadError || '节点详情暂不可用'}</AlertDescription>
              </Alert>
            </div>
            <div className="flex justify-end gap-2">
              <Button onClick={() => void load(id, generationRef.current, { fatal: true })} className="gap-1">
                <RefreshCw className="h-4 w-4" /> 重试
              </Button>
              <Button variant="outline" onClick={onClose}>
                关闭
              </Button>
            </div>
          </div>
        ) : (
          <div className="flex flex-col h-full overflow-hidden">
            {/* Header */}
            <header className="p-6 border-b border-border bg-card/40">
              <div className="flex items-start justify-between pr-8">
                <div className="space-y-1">
                  <DialogTitle className="text-xl font-bold tracking-tight text-foreground">{node.name}</DialogTitle>
                  <div className="flex flex-wrap gap-1.5 pt-1">
                    <Badge variant={statusTheme(node.status)}>{statusLabel(node.status)}</Badge>
                    {node.runtime_state ? (
                      <Badge variant={runtimeTheme(node.runtime_state)}>{runtimeLabel(node.runtime_state)}</Badge>
                    ) : null}
                    {node.control_mode === 'uplink' ? (
                      node.uplink_ws_connected ? (
                        <Badge variant="outline" className="border-success/40 text-success">WS 实时通道</Badge>
                      ) : (
                        <Badge variant="outline">上行离线 · HTTP 回退</Badge>
                      )
                    ) : null}
                    {installInfo ? (
                      isAgentOutdated(coreVersion(node), installInfo.recommended_agent_version) ? (
                        <StatusBadge
                          value="outdated"
                          label={
                            installInfo.recommended_agent_version
                              ? `可升级 → ${installInfo.recommended_agent_version}`
                              : undefined
                          }
                        />
                      ) : coreVersion(node) ? (
                        <StatusBadge value="success" label="已最新" />
                      ) : null
                    ) : null}
                  </div>
                </div>
              </div>

              <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 mt-4 text-xs">
                {node.control_mode === 'uplink' ? null : (
                  <div className="space-y-1">
                    <span className="text-muted-foreground block">控制面</span>
                    <code className="text-foreground font-mono">
                      {nodeEndpointLabel(node)}
                    </code>
                  </div>
                )}
                <div className="space-y-1">
                  <span className="text-muted-foreground block">订阅入口</span>
                  <code className="text-foreground font-mono break-all">
                    {node.public_address || node.address || '—'}
                    {mappingCount > 0 ? ` (${mappingCount} 条入站 NAT)` : ''}
                  </code>
                </div>
                <div className="space-y-1">
                  <span className="text-muted-foreground block">版本</span>
                  <span className="text-foreground">
                    核心 <code className="text-foreground font-mono">{coreVersion(node) || '—'}</code>
                    {node.core_commit ? (
                      <> (<code className="text-foreground font-mono">{node.core_commit}</code>)</>
                    ) : null}
                    {clientVersionLine(node) ? (
                      <>
                        {' '}
                        · {clientVersionLine(node)}
                      </>
                    ) : null}
                    {node.luci_version ? (
                      <>
                        {' '}
                        · LuCI <code className="text-foreground font-mono">{node.luci_version}</code>
                      </>
                    ) : null}
                    {node.singbox_version ? (
                      <> · sing-box <code className="text-foreground font-mono">{node.singbox_version}</code></>
                    ) : null}
                  </span>
                </div>
              </div>
            </header>

            {/* Content Tabs */}
            <Tabs value={activeTab} onValueChange={setActiveTab} className="flex-1 flex flex-col overflow-hidden">
              <div className="px-6 pt-4 border-b border-border bg-muted/20 overflow-x-auto">
                <TabsList className="bg-muted border border-border w-max">
                  <TabsTrigger value="connection">连接 / NAT</TabsTrigger>
                  <TabsTrigger value="dns">DNS</TabsTrigger>
                  <TabsTrigger value="inbounds">
                    入站{attachedCount ? ` (${attachedCount})` : ''}
                  </TabsTrigger>
                  <TabsTrigger value="frps">FRPS</TabsTrigger>
                  <TabsTrigger value="system">系统状态</TabsTrigger>
                  <TabsTrigger value="ops">运维 & 日志</TabsTrigger>
                </TabsList>
              </div>

              <div className="flex-1 overflow-y-auto p-6 space-y-6">
                <TabsContent value="connection" className="mt-0">
                  <NodeOverviewTab
                    busy={busy}
                    editName={editName}
                    setEditName={setEditName}
                    editLabels={editLabels}
                    setEditLabels={setEditLabels}
                    editLabelDraft={editLabelDraft}
                    setEditLabelDraft={setEditLabelDraft}
                    editToken={editToken}
                    setEditToken={setEditToken}
                    setEditTokenChanged={setEditTokenChanged}
                    tokenVisible={tokenVisible}
                    setTokenVisible={setTokenVisible}
                    editAddress={editAddress}
                    setEditAddress={setEditAddress}
                    editPort={editPort}
                    setEditPort={setEditPort}
                    editPublic={editPublic}
                    setEditPublic={setEditPublic}
                    editEgress={editEgress}
                    setEditEgress={setEditEgress}
                    editDDNS={editDDNS}
                    setEditDDNS={setEditDDNS}
                    editControlMode={editControlMode}
                    setEditControlMode={setEditControlMode}
                    canUplink={(node.capabilities || []).includes('uplink-v1') || node.control_mode === 'uplink'}
                    connectionErrors={connectionErrors}
                    clearConnectionError={clearConnectionError}
                    onSaveConnection={onSaveConnection}
                    ifaces={ifaces}
                    installInfo={installInfo}
                    copied={copied}
                    copiedUpgrade={copiedUpgrade}
                    copyInstallCommand={copyInstallCommand}
                    copyUpgradeCommand={copyUpgradeCommand}
                  />
                </TabsContent>

                <TabsContent value="dns" forceMount className="mt-0 data-[state=inactive]:hidden">
                  <NodeDNSTab
                    node={node}
                    onSaved={(updated) => {
                      if (updated.id === id) setNode(updated)
                      onChanged()
                    }}
                    onDirtyChange={setDNSDirty}
                  />
                </TabsContent>

                <TabsContent value="inbounds" className="mt-0">
                  <NodeInboundsTab
                    nodeId={id}
                    busy={busy}
                    inboundsLoading={inboundsLoading}
                    inboundsError={inboundsError}
                    allInbounds={allInbounds}
                    inboundNAT={inboundNAT}
                    savedInboundNAT={savedInboundNAT}
                    tlsBindings={tlsBindings}
                    managedDomains={managedDomains}
                    protocolCertificates={protocolCertificates}
                    tlsBusy={tlsBusy}
                    editPublic={editPublic}
                    nodeAddress={node.address}
                    toggleInbound={toggleInbound}
                    updateInboundNAT={updateInboundNAT}
                    setTLSBindings={setTLSBindings}
                    onSaveTLS={onSaveTLS}
                    onSaveInbounds={onSaveInbounds}
                    retryInbounds={() => void loadInboundsState(id, generationRef.current)}
                    supportsManagedTLS={supportsManagedTLS}
                  />
                </TabsContent>

                <TabsContent value="frps" className="mt-0">
                  <NodeFRPSTab
                    node={node}
                    nodeId={id}
                    busy={busy}
                    busyAction={busyAction}
                    frps={frps}
                    frpsDraft={frpsDraft}
                    frpsDirty={frpsDirty}
                    frpsLoading={frpsLoading}
                    frpsError={frpsError}
                    frpsMappings={frpsMappings}
                    frpsMappingsLoading={frpsMappingsLoading}
                    frpsMappingsError={frpsMappingsError}
                    managedDomains={managedDomains.filter((domain) => domain.node_id === id)}
                    updateFRPSDraft={updateFRPSDraft}
                    loadFRPS={() => void loadFRPS(id, generationRef.current)}
                    loadFRPSMappings={() => void loadFRPSMappings(id, generationRef.current)}
                    saveFRPS={() => void saveFRPS()}
                    runFRPSAction={(act) => void runFRPSAction(act)}
                    revealFRPSToken={revealFRPSToken}
                    rotateFRPSToken={() => void rotateFRPSToken()}
                  />
                </TabsContent>

                <TabsContent value="system" className="mt-0">
                  <NodeSystemTab key={id} nodeId={id} />
                </TabsContent>

                <TabsContent value="ops" className="mt-0">
                  <NodeOpsTab
                    node={node}
                    busy={busy}
                    busyAction={busyAction}
                    installInfo={installInfo}
                    metrics={metrics}
                    task={task}
                    preview={preview}
                    logs={logs}
                    streaming={streaming}
                    logsFollowing={logsFollowing}
                    setLogsFollowing={setLogsFollowing}
                    runAction={runAction}
                    onPreview={onPreview}
                    onMetrics={onMetrics}
                    onRemoteUpgrade={onRemoteUpgrade}
                    startLogs={startLogs}
                    stopLogs={stopLogs}
                  />
                </TabsContent>
              </div>
            </Tabs>
          </div>
        )}
      </DialogContent>
    </Dialog>
    <ConfirmModal
      open={closeConfirm}
      title="放弃未保存的修改？"
      description="节点连接、DNS、入站关联或 FRPS 草稿尚未保存，关闭后会丢失这些更改。"
      confirmText="放弃并关闭"
      confirmVariant="destructive"
      onConfirm={() => {
        setCloseConfirm(false)
        onClose()
      }}
      onCancel={() => setCloseConfirm(false)}
    />
    </>
  )
}
