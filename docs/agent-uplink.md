# Agent 上行（uplink）

LadderAirport 默认用 push 模式管控节点：`浏览器 → Panel → mTLS gRPC → Agent`，由 Panel 主动拨号 Agent 的 gRPC 控制口。这要求 Agent 有公网可达地址或端口映射，NAT / 无公网 IP 的节点无法接入。

uplink 模式反过来由 Agent 主动出站访问 Panel：HTTP 上报，再用 WebSocket 长连接收配置和即时操作。认证是节点 Bearer 令牌，默认不初始化管理面 TLS。不新开端口，也不要求 Panel 能拨到节点。

```text
浏览器 ──HTTP──► Panel ──mTLS gRPC──► Agent（push）
                 ▲
                 └── HTTP 上报 + WebSocket（uplink，Bearer，无管理面 TLS）
```

## push 与 uplink 对比

| 维度 | push（默认） | uplink |
|------|------|------|
| 发起方 | Panel 拨号 Agent gRPC | Agent 出站访问 Panel HTTP |
| 网络要求 | Agent 公网可达 / 端口映射 | 仅需 Agent 能出站到 Panel（NAT 友好） |
| 下发时延 | 即时 | 配置类最长一个拉取周期（默认 60 秒）；即时操作走命令队列，通常亚秒级 |
| 即时操作（接口枚举 / 升级 / 系统指标 / BBR / FRPS 管理动作） | 支持 | 支持（命令队列异步下发 + 结果回传，见下） |
| 探测（probe） | 拨号即时 | 读最近一次上报快照 |
| 日志流 | 支持 | 暂不支持（无即时通道；`409`） |
| 出站探测 / 协议证书部署 / DNS 公网探测 | 支持 | 暂不支持（`409`） |
| 认证 | mTLS 客户端证书 + Bearer | 节点 Bearer 令牌（不初始化管理面 TLS） |
| 控制面 gRPC 端口 | 必填（默认 50051） | 可不填 |

选择原则：能被 Panel 稳定拨到（公网 IP、端口映射、VPN、DNAT）就用 push，拿到即时性；否则用 uplink，牺牲下发实时性换取 NAT 穿透与零入站端口。

## 认证与传输复用

- **传输层**：Agent 侧用 `panelhttp.NewClient()`（`RequestTimeout=30s`、`IdleConnTimeout=3m`、HTTP/2、TLS 会话缓存）访问 Panel。Panel 侧 HTTP server `IdleTimeout` 相应设为 3 分钟，匹配 keep-alive 窗口。这里的 TLS 是访问 Panel HTTPS 的普通传输，不是节点管理面证书。
- **认证层**：uplink 端点用节点长期控制令牌（`Authorization: Bearer <token>`），Panel 侧以常量时间比对，拒绝空令牌。这些端点免 admin 会话，但必须通过节点令牌校验；错误令牌返回 `401`。
- 默认 uplink 只需要 `-panel-url` / `-node-id` / `LADDER_TOKEN`。管理面证书只在 push，或显式打开 `LADDER_UPLINK_SERVE_GRPC` 时才初始化。

## 数据上报

`POST /api/v1/agent/report`（Agent → Panel，默认每 15 秒一次）。请求体（可选指标字段用指针区分「未上报」与「零值」）：

```json
{
  "node_id": "...",
  "collected_at_unix": 1700000000,
  "runtime_state": "running",
  "config_hash": "...",
  "last_error": "",
  "agent_version": "v0.15.12",
  "core_version": "v0.15.12",
  "core_commit": "2372916",
  "client_type": "linux",
  "client_version": "v0.15.12",
  "singbox_version": "...",
  "capabilities": ["uplink-v1", "..."],
  "connections": 12,
  "uplink_bytes": 0,
  "downlink_bytes": 0,
  "cpu_percent": 3.1,
  "memory_rss_bytes": 0
}
```

Panel 侧 `ApplyNodeReport` 的处理要点：

- **单调时间戳防乱序**：仅当 `collected_at_unix >= 已记录的 uplink_last_seen_unix` 才写入，旧样本直接丢弃（响应 `applied=false`）。允许最多 300 秒时钟偏移，超前太多的时间戳拒绝。
- **部分更新**：用 SQL `CASE WHEN` 只更新本次真正带上来的列（能力、指标、配置哈希、版本各自独立判断），避免一次心跳把其它字段清空。`core_version`、`core_commit`、`client_type`、`client_version`、`luci_version` 同样只在非空时写入；旧客户端不带这些字段时，已保存的值保持不变。
- **版本拆分**：`agent_version` 与 `core_version` 都是上游 Ladder 核心版本（`git describe --tags --always`：精确标签为 `vX.Y.Z`，超前提交为 `vX.Y.Z-N-gSHA`）。过期判断只比较核心版本和主仓 Release 标签。`client_type` 为 `linux`、`android` 或 `openwrt`；缺省不猜测平台。`client_version` 是外壳版本（Android `versionName`、OpenWrt `PKG_VERSION-rN`）。`luci_version` 仅 OpenWrt 在装了 LuCI 时上报，空值省略。`android` 与 `openwrt` 不接受 Linux 发布包远程升级。旧 Panel 忽略多出来的 JSON 键；新 Panel 在 `core_version` 为空时用 `agent_version` 充当核心版本。WebSocket 上报使用同一组 JSON 键。gRPC `PingResponse` 用新字段号携带同名信息：`core_version = 5`、`core_commit = 6`、`client_type = 7`、`client_version = 8`、`luci_version = 9`。
- **状态刷新**：接受上报即置节点 `status=online`，刷新 `last_seen_unix` 与 `uplink_last_seen_unix`。
- **数值校验**：CPU 百分比限定 0–100，连接数 / 流量 / 内存等不得为负，非法值拒绝，防脏数据。

## 配置下发

统一走 `/api/v1/agent/config-sync`，三个方法分工：

- **`HEAD`**（廉价探测）：只返回响应头 `X-Config-Hash` / `X-FRPS-Hash` / `X-Desired-State`。Agent 每周期先 HEAD，哈希未变时只做期望运行态对齐（start/stop），不传输大 JSON。
- **`GET`**：语义同 HEAD，但以 JSON body 返回哈希与期望态，便于调试与 e2e 校验。
- **`POST`**（真正拉取）：Agent 带上 `applied_config_hash` / `applied_frps_hash`，Panel 比对后决定：
  - 两者都未变 → `{ "changed": false, "desired_state": "..." }`
  - sing-box 配置变了 → 返回 `config_json` + `config_hash` + `replace: true`，并 `SaveSnapshot` 落一份配置快照
  - FRPS 配置变了 → 返回 `frps { ... }` + `frps_hash`（`auth_token` 由 Panel 用凭据主密钥解密后下发，密文不落快照明文）

请求示例：

```json
{ "node_id": "...", "applied_config_hash": "...", "applied_frps_hash": "..." }
```

Agent 侧 `applySync` 收到后依次执行 `ApplyConfig` → `ApplyFRPServerConfig` → 按 `desired_state` 决定 `Start` / `Stop`，成功后本地记住新哈希。

**收敛闭环**：Panel 下发 hash → Agent 应用 → 下次上报带回自身的 `config_hash` → Panel 据此判断是否收敛。这是 uplink 版「配置是否生效」的判据，对应 push 里 gRPC 的即时返回。

## 期望运行态（desired_runtime）

push 的启停是即时 RPC；uplink 没有即时通道，因此引入节点级 `desired_runtime`（`running` | `stopped`）作为期望态：

- 操作员在面板点「启动 / 停止」→ Panel 只更新 `desired_runtime` 并建一条 `success` 状态的任务，提示「已记录，节点将在下次拉取时应用」。单节点操作与批处理各有一份等价实现。
- Agent 每次 config-sync 用 `X-Desired-State`（或 body 中的 `desired_state`）对齐本地 core 运行态：期望 `stopped` 则 `Stop`，期望 `running` 且当前未运行则 `Start`。

## 命令队列（即时操作）

配置下发是**声明式收敛**（期望态 hash，Agent 周期对齐），但接口枚举、Agent 升级、系统指标、BBR 开关、FRPS 管理动作这类**一次性、有副作用、要拿实时返回**的操作不适合塞进声明式配置。为此 uplink 增加一条独立的**命令队列**通道，语义是「请求—应答式的幂等任务队列」：

```text
浏览器 ──HTTP──► Panel ──入队──► agent_commands（pending）
                         │
Agent ◄──长轮询 lease──── ┘   GET  /api/v1/agent/commands?node_id=..&wait=25s
Agent ──执行本地操作──► 回传结果  POST /api/v1/agent/commands/{id}/result
浏览器 ──轮询────────► 命令结果    GET  /api/v1/commands/{id}
```

- **入队**：面板触发 uplink 节点的即时操作时，对应 handler 不再返回 `409`，而是写一条 `agent_commands` 记录并返回 `202 { queued, command_id, type }`。前端 `client.ts` 透明识别 202，改为轮询 `GET /api/v1/commands/{id}` 直到终态，把结果解析成与 push 模式同构的返回值，调用方无感知。
- **长轮询下发**：Agent 用节点 Bearer 长轮询 `GET /api/v1/agent/commands`，`wait` 上限 25 秒（低于 Panel HTTP `ReadTimeout`）。Panel 侧维护每节点唤醒 channel，入队即唤醒挂起的轮询，实现亚秒级下发；无命令则到点返回空。
- **投递语义**：at-least-once + 幂等。`LeaseAgentCommands` 用可见性超时（`defaultLeaseSec=60`）租约，租约到期未回传结果则重新投递并累加 `attempt`。命令带 TTL（`defaultCommandTTLSec=300`），过期未取直接置 `expired`。
- **结果回传**：`CompleteAgentCommand` 写终态（`succeeded` / `failed`）且幂等——已终态的命令再次回传是 no-op（`applied=false`），避免重投把结果覆盖。节点只能完成发给自己的命令（按 `node_id` + Bearer 校验）。
- **命令类型**：`interfaces` / `upgrade` / `sysmetrics` / `bbr-status` / `bbr-set` / `frps-mappings` / `frps-start` / `frps-stop`。Agent 侧 `dispatchCommand` 按 type 调本地 `control.Server` 的等价方法，结果 marshal 成 JSON 回传。
- **局限**：唤醒 channel 是 pod-local 的，多 Panel 实例部署时跨实例入队只能靠轮询到点兜底（≤`wait`），不影响正确性只影响时延。probe 仍走「读最近上报」，日志流暂未纳入队列。

## Panel 侧行为差异

对 uplink 节点，Panel 把即时拨号型操作分成两类：**命令队列**（接口枚举 / 升级 / 系统指标 / BBR / FRPS 管理动作，入队异步执行，见上）与**仍不支持**（日志流、出站探测、协议证书部署、DNS 公网探测，返回 `409`）：

- **命令队列**：接口枚举、Agent 升级、系统指标、BBR 读/写、FRPS mappings/start/stop 入队并返回 `202 + command_id`，前端轮询结果。
- **仍拒绝即时拨号**：日志流、出站探测、协议证书部署、DNS 公网探测对 uplink 返回 `409`（无即时通道或未纳入队列）。
- **读最近上报**：探测（probe）返回最近一次上报的 `status` / 版本 / 能力，不发起拨号；节点指标直接回落库的 `connections` / 流量 / CPU / 内存。
- **落快照代替直连**：代理链下发对 uplink 节点写配置快照而非拨号。
- **fleet 刷新**：不拨号，只看 `uplink_last_seen_unix` 是否超过 45 秒（`uplinkStaleAfter`），超时用带 cutoff 的 `MarkUplinkUnreachable` 置 `unreachable`，cutoff 条件避免并发上报被覆盖。
- **bootstrap 跳过**：启动时的全量下发只针对 push 节点；uplink 节点自行拉取，不进 bootstrap 队列。
- **切换保护**：把节点改成 uplink 时，要求它已上报过 `uplink-v1` 能力，否则拒绝并提示先升级 Agent。

## Agent 侧运行时

- 开关：`-uplink` 命令行标志，或环境变量 `LADDER_UPLINK=1`。
- 间隔：`-uplink-report-interval`（默认 15 秒）、`-uplink-config-interval`（默认 60 秒，带 ±20% 抖动打散并发）。
- 能力：`Ping` 的能力表包含 `uplink-v1`，供 Panel 判断该节点是否支持 uplink 与是否允许切换。
- config-sync 连续失败按次数退避：第 1 次 1 分钟，第 2 次 5 分钟，第 3 次起 15 分钟；成功后清零。
- 命令队列：`Run()` 额外拉起 `runCommandLoop`，用一个**无单请求超时**的 client（`CommandWait` 默认 25 秒，由 context 兜底）长轮询命令，收到即 `dispatchCommand` 调本地 `control.Server` 执行并回传结果；轮询失败按退避重试。

### 控制端口监听（gRPC）

uplink 节点通常位于 NAT 后，Panel 拨不进它的 gRPC 控制口，那个监听是死重。因此：

- **默认行为**：uplink 节点**不监听 gRPC 控制端口**，也不初始化管理面 TLS。安装只把一次性注册令牌换成长期控制令牌（`POST /api/v1/agent/enroll`），不生成私钥、不申请证书。控制面认证就是节点 Bearer。
- **开关**：`-uplink-serve-grpc` 命令行标志，或环境变量 `LADDER_UPLINK_SERVE_GRPC=1`，令 uplink 节点**仍然监听** gRPC 控制口并走完整 PKI / mTLS（例如节点其实公网可达、或想随时回切 push）。
- push 节点（未开 `-uplink`）**始终监听**，并始终初始化管理面 TLS。
- 已经带着证书跑起来的旧 uplink 节点会继续续签，直到安装时清掉 TLS 路径。没有管理面证书的 uplink 节点不能直接改成 push。

判定逻辑：`serveGRPC = 非 uplink || uplink-serve-grpc`。

## 部署

1. **新建 uplink 节点**：在「节点 → 添加节点」把「控制模式」选为 `uplink`，控制面地址与 gRPC 端口可留空。生成的一键安装命令会自动带上 `LADDER_UPLINK=1`。
2. **已有节点切换**：在节点详情「概览」把控制模式改为 `uplink`（需该节点先上报过 `uplink-v1`）。
3. **手动配置**：在 `agent.env` 设 `LADDER_UPLINK=1`（安装脚本会透传该变量），重启 `ladder-agent`。默认不再监听 gRPC 控制口；若该节点公网可达且想保留 push 能力，再加 `LADDER_UPLINK_SERVE_GRPC=1`。

push 节点仍走 PKI 注册、证书续签和 mTLS，见 [部署 · Agent](../deploy/README-agent.md) 与 [管理面 PKI](management-pki.md)。默认 uplink 安装不走这些步骤。

## 安全考量

- uplink 端点免 admin 会话，但强制节点令牌常量时间校验，错误令牌返回 `401`。
- 上报数值全部做边界校验（CPU 0–100、指标非负、时间戳窗口），拒绝脏数据。
- FRPS `auth_token` 由 Panel 用凭据主密钥解密后才下发，配置快照中不落明文。
- 浏览器仍不直连 Agent；uplink 只是把 Agent → Panel 的方向复用到 HTTP，Panel 仍是唯一控制中心。
