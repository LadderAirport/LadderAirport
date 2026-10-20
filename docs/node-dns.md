# 节点 DNS

节点 DNS 是这台节点上的 sing-box 用来解析两类名字的配置：

- 代理链下一跳的主机名（只解析出站的 `server`，不把用户目标解析成 IP）
- 路由最终落到本机 `direct` 时的用户目标

中间跳把域名原样交给下一跳。用户目标由出口那一跳、用那一跳自己的 DNS 解析。

DNS 存在这条 Agent 上，没有全局层，也没有单独的设备表。Panel 里一台已注册的 Agent 就是 `nodes` 里的一行，对应一个 sing-box 进程。同一台物理机如果跑了两个 Agent，会是两行，各自一份 DNS，不会互相覆盖。因此配置放在 `nodes.dns_json`，只作用于这一台。

编辑入口在节点列表：点节点名或「详情」，在打开的 Agent 详情里选「DNS」页签。这一页和「连接 / NAT」分开。服务器是一份可增删、上下移动的列表，每行有名称、类型（`udp` / `tcp` / `tls` / `https` / `quic` / `h3`）、地址、端口、HTTPS 与 HTTP/3 的路径、TLS 的 server name，以及启用开关。「恢复默认」把列表换回下面四台 DoH，不改地址策略、嗅探和 ECS。保存只提交 DNS，不会立刻下发，下一次下发或节点拉取配置时生效。

## 列表怎么用

sing-box 1.12 没有 DNS 健康检查，也没有故障转移。列表顺序的含义只有一条：第一台启用的服务器是 `dns.final`。节点上的查询，以及链出站的 `domain_resolver`，都用这一台。

其余已启用的服务器会按顺序写进 `dns.servers`，但没有 DNS 规则指向它们，所以不会接到查询。它们是排好的备选：要换上游，把那一行启用并移到第一行。停用的行只留在 `dns_json` 里，不会下发。没有在 Agent 里做健康探测再改写 final：探测要反复重载 sing-box，超时和误判都不可靠。

默认四台都启用，顺序是 AliDNS、DNSPod、Cloudflare、Google。因此新建节点的 final 是 AliDNS（`doh-ali`）。这和以前按域名拆开用的规则不同：不再有 `.cn`、`geosite-cn` 或「AAAA 走 Google」。

| 顺序 | 标签 | 地址 | server name |
|------|------|------|-------------|
| 1，默认 final | `doh-ali` | `223.5.5.5:443` `/dns-query` | `dns.alidns.com` |
| 2 | `doh-dnspod` | `1.12.12.12:443` `/dns-query` | `doh.pub` |
| 3 | `doh-cf` | `1.1.1.1:443` `/dns-query` | `cloudflare-dns.com` |
| 4 | `doh-google` | `8.8.8.8:443` `/dns-query` | `dns.google` |

地址是已核对证书的 IPv4，拨号不需要先解析 DoH 域名，也可以和出口网卡的 `bind_interface` 一起用。

旧的 `dns_json` 没有 `enabled` 字段，读出来时每行都当作启用，顺序保持原样。以前强制的四台固定顺序若已写入数据库，第一台仍是 AliDNS，所以升级后 final 也是它。空的服务器列表会恢复这四台默认值；全部停用会被拒绝。

服务器名称由操作员填写，1 到 32 个字母、数字、`_` 或 `-`，不能重复。类型留空时按 `https`。端口留空时 UDP/TCP 用 53，TLS/QUIC 用 853，HTTPS/HTTP/3 用 443。

如果某一台的地址是主机名，启用的列表里还必须有另一台 IP 服务器。生成配置会给主机名加上 `domain_resolver`，指向第一台启用的 IP，策略 `ipv4_only`。全部是主机名会被拒绝。

缓存默认：`independent_cache`，容量 2048，`reverse_mapping` 开启，不禁用过期。地址策略默认 `prefer_ipv4`，用在路由计划的 `resolve` 动作上。ECS `client_subnet` 默认空；填了 IP 会存成 `/32` 或 `/128`，并写到 `dns.client_subnet`。

## 路由

sing-box 不低于 1.12 时，节点配置还会：

1. 入站嗅探内层 TLS / HTTP / QUIC（默认开，超时 300ms）。`sniff_override_destination` 默认开，把 IP 目标改成嗅探到的域名，域名路由计划才能命中。路由规则最前面还有一条 `action: sniff`。
2. 代理链入站规则仍在域名规则之前，避免链上流量被路由计划抢走。
3. 域名、后缀、关键字规则。
4. 计划里有 `ip_cidr` 时，在这些规则前插入 `action: resolve`，不指定 server，因此走 `dns.final`。策略用节点上的地址策略。
5. `direct` 出站不设 `domain_resolver`，用户目标走 `dns.final`。
6. 链出站设置 `domain_resolver`，server 为第一台启用的 DNS 服务器，策略 `ipv4_only`。只解析下一跳主机名，不把 DNS detour 进这条链。

出口网卡非空时，`direct`、链出站和每一台下发的 DNS 服务器都带同一个 `bind_interface`。DNS 服务器自己拨号，不 detour 到 `direct`（那样会成环）。节点 DNS 不再附带 `geosite-cn` rule-set。

## 平台

- Android 节点（`client_type=android`）不下发 `bind_interface`。Agent 在 Android 上还会从 `dns.servers` 里剥掉这个字段，避免推下来的旧配置拨号失败。空的 `client_type` 仍按 Linux 处理，可以绑定网卡。
- OpenWrt 节点不改 dnsmasq，不监听 53，不劫持局域网 DNS。DoH 由 sing-box 自己拨出。

## 版本

Panel 只在上报的 `singbox_version` 能解析且不低于 1.12.0 时写入新的 `dns` 块、嗅探和 `domain_resolver`。版本为空或更低时，下发的 JSON 与原来一样，旧 Agent 不受影响。

新 Agent 在收到的配置没有 `dns`（或 `servers` 为空）时，会注入同一组 DoH 默认值，final 为第一台，并给链出站补上指向它的 `domain_resolver`。出口网卡会从 `direct` 出站抄到 DNS 服务器上。配置里已经有 DNS 时，Agent 原样保留。1.12 之前的 Agent 二进制没有这段逻辑，仍使用它们自己的旧默认值。
