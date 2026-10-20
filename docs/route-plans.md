# 路由计划

路由计划是一组有序的分流规则，按「匹配类型 + 动作」决定流量走代理、直连还是拦截。计划有两种作用范围：**全局**计划下发到节点 sing-box 的 `route.rules`，**订阅级**计划注入订阅渲染输出（Clash / sing-box），由客户端执行。

Web 端在「路由计划」页面维护。订阅级计划创建时必须选择一个所属订阅（决定归属与级联删除）；要真正生效，还需在「订阅」页面的编辑对话框中把该计划选为订阅的「路由计划」（`route_plan_id`）——渲染只认这个绑定，已停用或非订阅级的绑定会被忽略。

## 作用范围

| 范围 | 生效位置 | 影响面 |
|------|----------|--------|
| `global` | 节点 sing-box 配置 | 该节点所有入站收到的流量 |
| `subscription` | 订阅渲染输出 | 绑定了该计划（`route_plan_id`）的订阅的客户端配置 |

- 新增 / 修改 / 删除一个**已启用的全局计划**后，Panel 自动对所有节点触发一次重新下发（任务列表可见，失败不影响计划本身的保存）。
- 订阅级计划只影响渲染结果，不触发节点下发；客户端刷新订阅后生效。
- 计划的 scope 与所属订阅在创建后不可修改；删除订阅会级联删除其名下订阅级计划。每条计划和每条规则都可以单独停用，停用后不进入任何输出。

## 规则

规则在计划内按顺序排列，逐条匹配、命中即止。

匹配类型（`match_type`）：

| 类型 | 含义 | 示例 `match_value` |
|------|------|--------------------|
| `domain` | 完整域名 | `www.example.com` |
| `domain_suffix` | 域名后缀 | `example.com` |
| `domain_keyword` | 域名关键字 | `google` |
| `ip_cidr` | IP 段（须为合法 CIDR） | `203.0.113.0/24` |
| `process_name` | 客户端进程名（仅订阅输出生效，见下） | `telegram.exe` |

动作（`action`）：

| 动作 | 含义 | 说明 |
|------|------|------|
| `proxy` | 走代理 | 必须提供 `target_chain_id`，引用一条**已启用**的代理链 |
| `direct` | 直连 | |
| `block` | 拦截 | |

## 节点侧行为（全局计划）

- `proxy` 动作按代理链解析到本节点应使用的出站：中间跳指向链上下一跳节点的出站，末跳（链出口）映射为 `direct`。节点不在目标代理链上时跳过该规则并记录 debug 日志。
- 代理链中间跳把目标域名原样交给下一跳，不在本机解析。只有出站变成 `direct` 的那一跳才用本节点 DNS 解析用户目标。节点 DNS 的配置、DoH 上游和版本门槛见 [node-dns.md](node-dns.md)。
- `process_name` 规则在节点侧无意义（节点看到的是入站流量，不是本机进程），下发时直接跳过。
- 全局计划与既有的代理链配置互补：代理链定义「怎么走」，路由计划定义「哪些流量走」。

## 订阅侧行为（订阅级计划）

- 规则注入到渲染输出的默认 CN 分流规则**之前**，保证优先于内置分流命中。
- `proxy` 动作在客户端落到「节点选择」策略组（Clash 选择组 / sing-box `proxy` selector），最终出口仍由客户端用户选择；代理链是服务端概念，不会出现在客户端配置里。
- `process_name` 仅在订阅输出中生效，匹配客户端本机进程（Clash 的 `PROCESS-NAME`、sing-box 的 `process_name`）。

## API

```
GET    /api/v1/route-plans
POST   /api/v1/route-plans
GET    /api/v1/route-plans/{id}
PUT    /api/v1/route-plans/{id}
DELETE /api/v1/route-plans/{id}
```

创建全局计划示例：

```bash
curl -X POST "${PANEL}/api/v1/route-plans" \
  -H "Cookie: ..." -H 'Content-Type: application/json' \
  -d '{
    "name": "流媒体走 IPLC 链",
    "scope": "global",
    "rules": [
      {"match_type": "domain_suffix", "match_value": "example.com", "action": "proxy", "target_chain_id": "<chain-id>"},
      {"match_type": "ip_cidr", "match_value": "203.0.113.0/24", "action": "block"}
    ]
  }'
```

创建订阅级计划时改为 `"scope": "subscription"` 并提供 `"subscription_id"`；随后在该订阅上设置 `route_plan_id` 才会注入渲染输出（仅 Clash / sing-box 格式，v2ray 链接格式不注入）。`process_name` 规则只在订阅级计划中有实际效果。
