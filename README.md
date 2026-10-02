# LadderAirport

自建代理机群控制面：Go Panel（内嵌 React + SQLite）+ 内嵌 [sing-box](https://github.com/SagerNet/sing-box) / [frp](https://github.com/fatedier/frp) 的节点 Agent。push 节点由 Panel 拨号 gRPC；uplink 节点复用证书续签已在走的 Panel HTTP 定时上报并拉配置。

```
浏览器 ──HTTP──► Panel ──mTLS gRPC──► Agent（push）
                ▲
                └── HTTP 上报 / 拉配置（uplink，与 PKI 续签同一入口）
```

## 功能

- **节点**：登记 / 探测 / 启停 / 远程升级，系统指标与 BBR 拥塞控制开关，卡片与表格总览；NAT 节点可选 uplink（见 [Agent 上行](docs/agent-uplink.md)）
- **入站模板**：SS / Trojan / VLESS(Reality) / Hysteria2 / TUIC / AnyTLS / VMess / HTTP / SOCKS5 / Auto(HTTP+SOCKS5)
- **配置下发**：关联入站 → 完整 sing-box JSON → push 走 gRPC，uplink 等下次 HTTP 拉取；启动时自动同步并重试
- **FRP Server**：节点内嵌 FRPS，Panel 加密保存认证令牌并独立下发、启停和查看状态
- **订阅**：Clash / sing-box 链接，基础 CN 分流；可聚合外部机场订阅源；订阅令牌支持轮换与停用
- **路由计划**：全局计划下发节点 sing-box 路由规则，订阅级计划注入订阅渲染输出；域名 / 关键字 / IP / 进程名匹配，代理链 / 直连 / 拦截动作（见 [路由计划](docs/route-plans.md)）
- **DNS / ACME**：AliDNS、DNSPod、Cloudflare 自动解析；DNS-01 自动签发和续期协议 TLS 证书，私钥只留在 Agent
- **部署**：一键装成 systemd 服务；OpenWrt 路由节点见兄弟仓 [LadderAirportOpenwrt](https://github.com/LadderAirport/LadderAirportOpenwrt)

## 快速安装

无需克隆仓库，从 [GitHub Release](https://github.com/LadderAirport/LadderAirport/releases) 拉最新二进制。

**Panel**

```bash
curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-panel.sh \
  | sudo bash
```

浏览器打开 `http://<host>:8080`。首次启动自动生成随机管理员密码并打印一次到日志（`journalctl -u ladder-panel` 查看）；要预置需在首次启动前把 `LADDER_ADMIN_PASSWORD` 写入 `/etc/ladder-panel/panel.env`（安装脚本不覆盖已有 panel.env）。登录后立刻在「设置」修改。详见 [deploy/README-panel.md](deploy/README-panel.md)。

**Agent**

在 Panel「设置」配置 HTTPS Public Base URL，再到「节点」添加节点并执行生成的一键安装命令。Agent 强制使用 Panel CA 管理的 mTLS：节点本地生成私钥，Panel 通过 CSR 签发证书并自动续期；不支持明文或节点自签 CA。详见 [deploy/README-agent.md](deploy/README-agent.md) 与 [管理面 PKI](docs/management-pki.md)。

装完后：创建入站 → 关联到节点 → 下发。NAT 场景可在节点上拆分控制面地址与订阅公网地址。需要反向代理入口时，在节点详情的「FRPS」页签配置，详见 [内嵌 FRPS](docs/embedded-frps.md)。

## 本地开发

```bash
git clone --recurse-submodules https://github.com/LadderAirport/LadderAirport.git
cd LadderAirport

make agent   # → bin/ladder-agent（tags: with_quic,with_utls）
make panel   # 先 npm 构建 web，再 → bin/panel；离线用 make panel-bin（编译已提交的 embed dist，无需 npm）
make test    # go vet + go test -race（agent 带 with_quic,with_utls tags）
make e2e     # panel-bin + agent 后执行 scripts/e2e-smoke.sh
make proto   # 由 proto/agent/v1/agent.proto 重新生成 gRPC 代码
```

```bash
./bin/panel -listen :8080 -db ./data/panel.db
```

本地开发首次启动同样会生成随机管理员密码并打印到控制台；可用 `LADDER_ADMIN_PASSWORD=dev-pass ./bin/panel ...` 预置。

### 常用 Panel 参数

| 参数 | 默认 | 说明 |
|------|------|------|
| `-listen` | 空 → settings.listen_addr → `:8080` | HTTP 监听地址 |
| `-db` | `./data/panel.db` | SQLite 路径 |
| `-session-secret` | 空 → `LADDER_SESSION_SECRET` → `<db目录>/session.secret` → 临时随机值 | JWT 会话 HMAC 密钥 |
| `-bootstrap` / `-bootstrap-retry` | `true` / `true` | 启动时全量下发 + Start；定时重试未就绪节点 |
| `-bootstrap-timeout` / `-bootstrap-retry-interval` | `3m` / `30s` | 首次下发超时；重试间隔 |
| `-pki-dir` | `<db目录>/pki` | 管理 PKI 目录 |
| `-pki-rotate-intermediate` | `false` | 轮换在线中间 CA 和 Panel 客户端证书后退出 |
| `-credentials-key-file` | `<db目录>/secrets/credentials.key` | DNS/ACME/FRPS 凭据主密钥文件（亦可用 `LADDER_CREDENTIALS_KEY`） |
| `-version` | — | 显示版本后退出 |

| 目录 | 作用 |
|------|------|
| `panel/` | 控制面 API、存储、转换器、gRPC 客户端 |
| `agent/` | `ladder-agent` + sing-box / frp 子模块 |
| `web/` | React 前端（构建进 `panel/web/dist`） |
| `pkg/` `proto/` | 共享库与 gRPC 定义 |
| `scripts/` `deploy/` | 安装脚本与 systemd 单元 |

打 `v*` tag 会触发 Release 构建（linux/amd64、linux/arm64、linux/armhf）。

## 安全

- 管理员密码无默认值：首次启动随机生成并打印一次到日志（或预置 `LADDER_ADMIN_PASSWORD`），登录后立刻修改；Agent Token 由 Panel 签发随机值，拒绝空值与弱默认值
- 会话密钥未设置时自动生成并持久化（`session.secret`，与 `panel.db` 同目录）；备份 `panel.db` 时一并备份
- 节点强制使用 Panel 管理 CA 和 mTLS；不兼容的旧 Agent 需全清卸载后重新创建并注册
- 首次初始化后离线保存并移走根 CA 私钥；Panel 日常只保留中间 CA 私钥
- 公网 Panel 必须反代 HTTPS；代理入站公网证书继续使用 ACME，不与管理 CA 混用
- 备份 `panel.db` 时同时备份 `secrets/credentials.key`；缺少该密钥将无法解密 DNS / ACME 凭据和 FRPS 认证令牌
- 浏览器不直连 Agent，仅 Panel 访问控制口

DNS 自动解析和协议证书的配置、权限与恢复说明见 [DNS / ACME 运维指南](docs/dns-acme.md)。

## 许可证

Agent 运行时基于 [sing-box](https://github.com/SagerNet/sing-box) 和 [frp](https://github.com/fatedier/frp) 上游项目。控制面代码为 monorepo 中独立部分。
