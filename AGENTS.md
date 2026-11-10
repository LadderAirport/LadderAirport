# AGENTS.md

给在本仓库工作的编码 agent 的全局说明。面向用户的介绍见 [README.md](README.md)，各子系统的设计见 `docs/`。

## 项目概览

LadderAirport 是自建代理机群的控制面，是一个 Go workspace monorepo（`go.work`）：

| 目录 | 内容 |
|------|------|
| `panel/` | 控制面：HTTP API、SQLite 存储、配置生成、订阅、PKI、DNS/ACME。内嵌 `panel/web/dist` 前端 |
| `agent/` | 节点端 `ladder-agent`：进程内运行 sing-box 和可选的 FRPS |
| `agent/sing-box`、`agent/frp` | 上游子模块，通过 `agent/go.mod` 的 `replace` 引用 |
| `web/` | React 19 + Vite + Tailwind 4 前端源码 |
| `pkg/` | panel 和 agent 共享的库（auth、semver、nodedns、uplinkws 等） |
| `proto/` | gRPC 定义 `proto/agent/v1/agent.proto`，生成代码放在 `proto/gen/go` |
| `scripts/`、`deploy/` | 安装脚本、systemd 单元、环境变量示例 |

控制方式有两种：
- **push 节点**：Panel 通过 mTLS gRPC 主动连到 Agent。
- **uplink 节点**：Agent 通过 HTTP/WebSocket 上报并拉取配置，适合 NAT 场景，详见 `docs/agent-uplink.md`。

浏览器只连 Panel，不直连 Agent。

## 常用命令

```bash
make test       # pkg/panel/agent：go vet + go test -race（agent 带 with_quic,with_utls tags）
make web        # npm ci + build，并把 web/dist 复制到 panel/web/dist
make panel      # make web 之后编译 bin/panel
make panel-bin  # 不跑 npm，直接用已提交的 dist 编译 panel
make agent      # 编译 bin/ladder-agent
make e2e        # 端到端冒烟测试（scripts/e2e-smoke.sh）
make proto      # 修改 .proto 后重新生成 gRPC 代码
cd web && npm test && npm run typecheck   # 前端测试和类型检查
```

本地运行：`LADDER_ADMIN_PASSWORD=dev-pass ./bin/panel -listen :8080 -db ./data/panel.db`

单独测某个包：`cd panel && go test ./internal/subscription/ -run TestXxx`。agent 的测试必须带 `-tags with_quic,with_utls`。

## 必须遵守的约定

### 前端产物要和源码一起提交
`panel/web/dist` 已提交到仓库，并通过 go:embed 打进 panel 二进制。**只要改了 `web/src`，就要跑 `make web`，并把 `panel/web/dist` 一起提交。** CI 会用 `diff -qr web/dist panel/web/dist` 检查两者是否一致。

### 语言
- 面向用户的文案用简体中文，包括 API 错误信息（`writeError`）、`fmt.Errorf` 的报错、日志、前端界面和 `docs/` 文档。
- 代码注释和标识符用英文。
- 提交信息用英文，遵循 Conventional Commits：`feat:`、`fix(web):`、`chore:` 等。

### Panel 后端
- 路由统一在 `panel/internal/api/server.go` 注册，使用 Go 1.22+ 的 `mux.HandleFunc("GET /path/{id}", ...)` 写法。
- 返回响应用 `writeJSON` / `writeError`，解析请求体用 `decodeJSON`（有大小限制，并开启了 UseNumber）。
- **数据库迁移**都在 `panel/internal/store/store.go`：
  - 新表写 `CREATE TABLE IF NOT EXISTS`。
  - 新列追加到 `alters` 列表，必须写 `NOT NULL DEFAULT ...`。这些语句会对已有数据库重复执行，靠忽略 "duplicate column" 错误保证幂等。
  - 需要搬数据的一次性迁移，要先检测旧结构再执行。
  - 不要改动或删除已有的迁移语句。
- 敏感凭据（DNS/ACME/FRPS 令牌）经 `secretstore` 加密后才能存库，不要存明文。
- `nodeconfig` 负责把入站转换成节点的完整 sing-box 配置；`subscription` 负责订阅的解析、合并和渲染（Clash / sing-box / V2Ray 链接）。同一个协议的参数字段在这两处必须一致，`SingboxOutbound` 是单个节点客户端出站配置的标准写法。

### 用户准入
`proxyuser` 包是凭据派生、协议支持判断、服务端 `users` 和客户端参数替换的唯一实现。节点配置（`nodeconfig.inboundUsers`）和订阅渲染（`applySubscriptionCredentials`）都调用它，不要在别处另写一套。准入范围由用户订阅的选择决定（`proxyuser.BuildPlan`）。详见 `docs/proxy-users.md`。

### 订阅合并规则
处理顺序是：过滤占位地址 → 去重 → 节点改名 → 追加代理链（`AppendChains`）。
- 本地节点之间不去重。
- 外部节点只在**完整客户端配置**和已有节点相同时才删掉，判重不做 DNS 解析。
- 修改这部分时，要保持 `merge_test.go` 里的场景全部通过。

### Agent 和上游子模块
- `agent/sing-box` 和 `agent/frp` 是上游代码，**除非任务明确要求，否则不要改子模块里的文件**，也不要随手改子模块指向的提交。
- sing-box 的版本只以子模块的 git tag 为准，Makefile 和 CI 都用 `git describe --tags` 取版本。升级时要同时修改这几处：子模块指向的提交、`agent/go.mod` 的 `require`、`agent/README.md` 里的版本表。
- Agent 有 Linux、Android 等多个构建目标，Linux 专用代码（sysctl、systemd 等）要用构建约束隔离。

### 安全
- 节点之间强制使用 Panel CA 签发证书的 mTLS 通信，不要加入明文通信或自签证书的回退逻辑。
- 不要在日志中打印密码、令牌、私钥。唯一例外是首次启动时一次性打印的随机管理员密码。
- 根 CA 私钥按设计需要离线保存，不要加入让 Panel 常驻持有根 CA 私钥的代码。

## 验证要求

提交前至少做到：
1. 改了 Go 代码：跑 `make test`，至少要跑改动所在模块的 `go vet` 和 `go test`。
2. 改了前端：跑 `cd web && npm test`，再跑 `make web`，确认 `panel/web/dist` 已更新。
3. 改了 proto：跑 `make proto`，生成的代码一起提交。
4. 行为有变化时，同步更新 `docs/` 里对应的文档和 README 的功能列表。

## 发版

- 推送 `v*` tag 会触发 `.github/workflows/release.yml`，构建 linux/amd64、arm64、armhf 版本并发布到 GitHub Release。
- 版本号来自 git tag，通过 ldflags 注入，不需要手动改代码里的版本号。
- 小修复递增 patch 位（如 `v0.16.0` → `v0.16.1`），使用带注释的 tag：`git tag -a vX.Y.Z -m vX.Y.Z`。
- 发版前要确认：`make test` 通过；`panel/web/dist` 是最新构建；子模块指向的提交在上游存在。
- 推送 tag 和代码到 `main` 属于对外操作，必须由用户明确要求才能执行。

## 相关文档

- `docs/agent-uplink.md`：uplink 控制模式
- `docs/management-pki.md`：管理面 PKI 与 mTLS
- `docs/proxy-chains.md`：代理链（客户端组合）
- `docs/proxy-users.md`：用户与协议准入
- `docs/route-plans.md`：路由计划
- `docs/node-dns.md`：节点 DNS
- `docs/dns-acme.md`：DNS 自动解析与 ACME 证书
- `docs/embedded-frps.md`：内嵌 FRPS
- `docs/panel-frontend-optimization.md`：前端优化记录
