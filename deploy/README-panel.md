# 快速部署 Panel（systemd）

## 一键安装（推荐：从 Release 拉最新二进制）

控制面机器上**无需 Go / Node / 源码**，只要能访问 GitHub：

```bash
curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-panel.sh \
  | sudo bash
```

指定 HTTP 监听与版本（可选）：

```bash
curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-panel.sh \
  | sudo LADDER_LISTEN=':8080' LADDER_VERSION=v0.3.1 bash
```

预置初始管理员密码（可选）：安装时透传即可，如 `curl -fsSL .../install-panel.sh | sudo LADDER_ADMIN_PASSWORD='...' bash`，脚本会在 install 时把它写入 panel.env（已存在的 panel.env 不覆盖，仅补缺失键）；也可在**首次启动前**手动写入 `/etc/ladder-panel/panel.env`。不预置则随机生成并打印一次到日志。

### 行为说明

| 步骤 | 内容 |
|------|------|
| 下载 | `panel-linux-amd64`、`panel-linux-arm64` 或 `panel-linux-armhf`（按 `uname -m`） |
| 校验 | 若 Release 含 `SHA256SUMS.txt` 则自动校验 |
| 安装 | `/usr/local/bin/ladder-panel` |
| 配置 | `/etc/ladder-panel/panel.env`（已存在则不覆盖，仅补缺失键） |
| 数据 | `/var/lib/ladder-panel/panel.db`（SQLite，`modernc` 纯 Go，无 CGO） |
| 服务 | `ladder-panel.service` enable + restart |
| 前端 | 二进制内嵌 SPA，无需单独部署 Nginx 静态文件 |

### 其他安装方式

使用本地已有二进制：

```bash
sudo ./scripts/install-panel.sh /path/to/panel
```

在源码仓库内本地编译再装（使用已提交的 `panel/web/dist`，无需 npm）：

```bash
cd LadderAirport
sudo LADDER_FROM=local ./scripts/install-panel.sh
```

## 装完后

1. 浏览器打开 `http://<panel-host>:8080`
2. 管理员密码**没有默认值**：首次启动自动生成随机初始密码并打印一次到日志
   - 查看：`journalctl -u ladder-panel | grep 初始密码`
   - 预置：安装时透传 `sudo LADDER_ADMIN_PASSWORD=... bash install-panel.sh`（脚本写入 panel.env），或首次启动前手动写入 `/etc/ladder-panel/panel.env`（仅数据库尚无密码哈希的首次初始化生效）
   - 登录后立刻在「设置」修改
3. 「设置」填写 **Public Base URL**（如 `https://panel.example.com`）
   - 用于生成完整订阅 URL
   - 用于「添加节点并生成安装命令」完成 PKI 注册
4. 再装 Agent：见 [README-agent.md](README-agent.md)

```bash
systemctl status ladder-panel
journalctl -u ladder-panel -f
sudo cat /etc/ladder-panel/panel.env   # 含 session secret，权限 640
```

## 环境变量

| 变量 | 默认 | 说明 |
|------|------|------|
| `LADDER_ACTION` | `install` | `install` / `upgrade` / `uninstall`（也可用脚本首参） |
| `LADDER_PURGE` | `0` | 仅卸载：`1` 时删除 conf/data/SQLite/用户 |
| `LADDER_LISTEN` | `:8080` | HTTP 监听地址 |
| `LADDER_DB` | `/var/lib/ladder-panel/panel.db` | SQLite 路径 |
| `LADDER_ADMIN_PASSWORD` | 空 | 初始管理员密码，仅首次初始化生效：install 时透传（`sudo LADDER_ADMIN_PASSWORD=... bash install-panel.sh`）则写入 panel.env，也可手动写入 panel.env；为空则随机生成并打印一次到日志 |
| `LADDER_SESSION_SECRET` | 自动生成 | JWT 会话 HMAC；install/upgrade 时缺失则生成随机值补写入 panel.env（已有值不改），Panel 进程兜底持久化到 `session.secret` |
| `LADDER_BOOTSTRAP` | `true` | 启动时全量下发 + Start |
| `LADDER_BOOTSTRAP_TIMEOUT` | `3m` | 首次 bootstrap 超时 |
| `LADDER_BOOTSTRAP_RETRY` | `true` | 定时重试未就绪节点 |
| `LADDER_BOOTSTRAP_RETRY_INTERVAL` | `30s` | 重试间隔 |
| `LADDER_VERSION` | `latest` | Release 标签，如 `v0.3.1` |
| `LADDER_FROM` | `release` | `release` 下载；`local` 源码/本地 bin |
| `LADDER_REPO` | `LadderAirport/LadderAirport` | GitHub 仓库 |
| `LADDER_USER` / `LADDER_GROUP` | `ladder-panel` | 运行用户/组 |
| `INSTALL_BIN` | `/usr/local/bin/ladder-panel` | 安装路径 |

对应 CLI 参数见主 README「常用 Panel 参数」。

## 常用命令

```bash
systemctl status ladder-panel
journalctl -u ladder-panel -f
systemctl restart ladder-panel
```

### 升级

只替换二进制、刷新 systemd unit 并 restart；**保留** `panel.env` 与 SQLite。旧二进制备份为 `/usr/local/bin/ladder-panel.bak`。

```bash
# 升到最新
curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-panel.sh \
  | sudo env LADDER_ACTION=upgrade bash

# 升到指定版本
curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-panel.sh \
  | sudo env LADDER_ACTION=upgrade LADDER_VERSION=v0.3.1 bash
```

回滚：

```bash
sudo mv /usr/local/bin/ladder-panel.bak /usr/local/bin/ladder-panel
sudo systemctl restart ladder-panel
```

### 卸载

默认只停服务、删 unit 与二进制，**保留** conf 与 SQLite。

```bash
# 保留 conf/data（推荐）
curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-panel.sh \
  | sudo env LADDER_ACTION=uninstall bash

# 全清（会删除 panel.db，务必先备份）
curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-panel.sh \
  | sudo env LADDER_ACTION=uninstall LADDER_PURGE=1 bash
```

备份（升级/迁移/purge 前）：

```bash
sudo cp -a /var/lib/ladder-panel/panel.db /root/panel.db.bak
sudo cp -a /etc/ladder-panel/panel.env /root/panel.env.bak
```

## 防火墙 / 反代

```bash
# 仅放行管理网段访问 Panel HTTP（示例）
sudo ufw allow from 10.0.0.0/8 to any port 8080 proto tcp
```

生产建议用 **Caddy / Nginx** 终结 HTTPS，反代到 `127.0.0.1:8080`，并把 `LADDER_LISTEN` 改为 `127.0.0.1:8080`。

Caddy 示例（TLS 在 Caddy 终结；Agent 的 keep-alive 复用的是到 Caddy 的 HTTPS 连接）：

```caddy
{
    servers {
        timeouts {
            idle 3m
        }
    }
}

panel.example.com {
    reverse_proxy 127.0.0.1:8080 {
        transport http {
            keepalive 3m
            keepalive_idle_conns 8
        }
    }
}
```

`idle` 是 Caddy 对浏览器 / Agent 的 HTTPS 空闲超时，需大于 Agent 上报间隔（默认 15s）和配置探测间隔（默认 60s）。后面的 `keepalive` 只作用于 Caddy → `127.0.0.1:8080` 这一段明文，与 TLS 握手无关。

Nginx 示例：

```nginx
server {
    listen 443 ssl http2;
    server_name panel.example.com;
    # ssl_certificate / ssl_certificate_key ...

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

设置好反代后，在 Panel「设置」里把 **Public Base URL** 写成 `https://panel.example.com`。

## 安全建议

- 登录后立刻在「设置」修改初始管理员密码
- 备份 panel.env（含 `LADDER_SESSION_SECRET`，丢失会导致所有会话失效）与 `/var/lib/ladder-panel/panel.db`
- 公网务必经反代 HTTPS；不要把 `:8080` 直接暴露在 `0.0.0.0`
- Panel 需要能访问各 Agent 的 gRPC 端口（默认 50051）
