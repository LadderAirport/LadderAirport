# 节点 DNS

节点 DNS 是这台节点上的 sing-box 用来解析两类名字的配置：

- 代理链下一跳的主机名（只解析出站的 `server`，不把用户目标解析成 IP）
- 路由最终落到本机 `direct` 时的用户目标

中间跳把域名原样交给下一跳。用户目标由出口那一跳、用那一跳自己的 DNS 解析。

DNS 是节点级设置，没有全局层。新建节点会把下面这组默认值写入数据库；已有节点在读出空配置时按同一组默认值处理。Panel 在节点详情「连接 / NAT」里编辑，保存不会立刻下发，下一次下发或节点拉取配置时生效。

## 默认上游

四个上游都是 DoH。地址用已核对过证书的 IPv4，TLS 用 `server_name`，这样拨号不需要先解析 DoH 域名，也可以和出口网卡的 `bind_interface` 一起用。

| 标签 | 地址 | server name | 接到的查询 |
|------|------|-------------|------------|
| `doh-dnspod` | `1.12.12.12:443` `/dns-query` | `doh.pub` | 域名后缀 `cn` |
| `doh-ali` | `223.5.5.5:443` `/dns-query` | `dns.alidns.com` | `geosite-cn` 里其余名字（例如 `baidu.com`） |
| `doh-google` | `8.8.8.8:443` `/dns-query` | `dns.google` | 非中国域名的 `AAAA` 和 `HTTPS` |
| `doh-cf` | `1.1.1.1:443` `/dns-query` | `cloudflare-dns.com` | `dns.final`：其余查询，主要是海外 A。下一跳主机名也用它 |

规则顺序是：`.cn` → DNSPod，然后 `geosite-cn` → AliDNS，然后非中国域名的 `AAAA`/`HTTPS` → Google，最后 final → Cloudflare。`.cn` 写在 geosite 前面，避免这类名字再打到 AliDNS。

sing-box 1.12 没有 DNS 服务器健康检查或故障转移。同组的第二台不是备份：Google 只接非中国域名的 `AAAA`/`HTTPS`，DNSPod 只接 `.cn`。Cloudflare 不可达时，落在 final 上的 A 查询会失败，不会自动改走 Google。

`geosite-cn` 是远程 rule-set，URL 与订阅渲染相同（`https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs`），`download_detour` 为 `direct`。节点启动时若下载失败，sing-box 不会起来。

如果把某台的 `server` 改成域名，生成配置会给它加上 `domain_resolver`，指向仍是 IP 的那一台（优先 Cloudflare，其次 Google、AliDNS、DNSPod），策略 `ipv4_only`。四台全是域名会被拒绝：至少要留一台 IP 做引导。

标签固定，Panel 只编辑地址、端口、路径和 TLS server name。缓存默认：`independent_cache`，容量 2048，`reverse_mapping` 开启，不禁用过期。地址策略默认 `prefer_ipv4`。ECS `client_subnet` 默认空；填了 IP 会存成 `/32` 或 `/128`，并写到 `dns.client_subnet`。

## 路由

sing-box 不低于 1.12 时，节点配置还会：

1. 入站嗅探内层 TLS / HTTP / QUIC（默认开，超时 300ms）。`sniff_override_destination` 默认开，把 IP 目标改成嗅探到的域名，域名路由计划才能命中。路由规则最前面还有一条 `action: sniff`。
2. 代理链入站规则仍在域名规则之前，避免链上流量被路由计划抢走。
3. 域名、后缀、关键字规则。
4. 计划里有 `ip_cidr` 时，在这些规则前插入 `action: resolve`，不指定 server，因此仍走上面的 DNS 规则。策略用节点上的地址策略。
5. `direct` 出站不设 `domain_resolver`，用户目标走 DNS 规则再走 final。
6. 链出站设置 `domain_resolver`，server 为 `doh-cf`，策略 `ipv4_only`。只解析下一跳主机名，不把 DNS detour 进这条链。Reality 握手主机名同样走这台解析器，不会绕进链本身。

出口网卡非空时，`direct`、链出站和四台 DoH 都带同一个 `bind_interface`。DNS 服务器自己拨号，不 detour 到 `direct`（那样会成环）。

## 平台

- Android 节点（`client_type=android`）不下发 `bind_interface`。Agent 在 Android 上还会从 `dns.servers` 里剥掉这个字段，避免推下来的旧配置拨号失败。空的 `client_type` 仍按 Linux 处理，可以绑定网卡。
- OpenWrt 节点不改 dnsmasq，不监听 53，不劫持局域网 DNS。DoH 由 sing-box 自己拨出。

## 版本

Panel 只在上报的 `singbox_version` 能解析且不低于 1.12.0 时写入新的 `dns` 块、rule-set、嗅探和 `domain_resolver`。版本为空或更低时，下发的 JSON 与原来一样，旧 Agent 不受影响。

新 Agent 在收到的配置没有 `dns`（或 `servers` 为空）时，会注入同一组 DoH 默认值，并补上 `geosite-cn` rule-set 和链出站的 `domain_resolver`。出口网卡会从 `direct` 出站抄到 DNS 服务器上。配置里已经有 DNS 时，Agent 原样保留。1.12 之前的 Agent 二进制没有这段逻辑，仍使用它们自己的旧默认值。
