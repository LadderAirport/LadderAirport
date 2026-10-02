package converter

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/ladderairport/panel/internal/inboundfill"
	"github.com/ladderairport/panel/internal/store"
)

// ConvertOptions controls node-level knobs injected into the generated config.
type ConvertOptions struct {
	// BindInterface sets sing-box direct outbound bind_interface.
	// Empty means OS default routing (field omitted).
	BindInterface string
	// AllowEmpty emits a valid listener-free config. This is required when a
	// chain is disabled or the final inbound is detached.
	AllowEmpty bool
	// ChainRoutes routes traffic arriving on InboundID to Outbound.
	ChainRoutes []ChainRoute
	// RouteRules are match-condition rules from enabled global route plans.
	// They are emitted after chain rules and before the final outbound.
	// Outbound must reference a known outbound tag ("direct" or a chain
	// next-hop tag); rules with unknown or empty targets are skipped.
	RouteRules []RouteRule
}

type ChainRoute struct {
	InboundID string
	Outbound  map[string]any
}

// RouteRule is one resolved global route-plan rule. MatchType is one of
// domain|domain_suffix|domain_keyword|ip_cidr (process_name never reaches
// agent configs). Reject emits action:"reject" and ignores Outbound.
type RouteRule struct {
	MatchType  string
	MatchValue string
	Outbound   string
	Reject     bool
}

// Convert builds a full sing-box JSON config from inbound configs.
// Disabled inbounds are skipped. Returns an error if no enabled inbounds remain,
// if listen/port conflicts exist, or if required fields/validation fail.
func Convert(inbounds []store.InboundConfig, opts ConvertOptions) ([]byte, error) {
	enabled := make([]store.InboundConfig, 0, len(inbounds))
	for _, in := range inbounds {
		if in.Enabled {
			enabled = append(enabled, in)
		}
	}
	if len(enabled) == 0 && !opts.AllowEmpty {
		return nil, fmt.Errorf("没有已启用的入站")
	}

	seenPorts := map[string]string{} // "listen:port" -> name/id
	outInbounds := make([]map[string]any, 0, len(enabled))
	inboundTags := make(map[string]string, len(enabled))
	for _, in := range enabled {
		mapped, err := mapInbound(in)
		if err != nil {
			return nil, fmt.Errorf("入站 %q（%s）：%w", in.Name, in.ID, err)
		}
		listen, _ := mapped["listen"].(string)
		port, _ := asInt(mapped["listen_port"])
		key := fmt.Sprintf("%s:%d", listen, port)
		if prev, ok := seenPorts[key]; ok {
			return nil, fmt.Errorf("端口 %s 冲突（%s 与 %s）", key, prev, label(in))
		}
		seenPorts[key] = label(in)
		inboundTags[in.ID] = inboundTag(in)
		outInbounds = append(outInbounds, mapped)
	}

	direct := map[string]any{
		"type": "direct",
		"tag":  "direct",
	}
	if iface := strings.TrimSpace(opts.BindInterface); iface != "" {
		direct["bind_interface"] = iface
	}

	outbounds := []map[string]any{direct}
	rules := make([]map[string]any, 0, len(opts.ChainRoutes))
	outboundTags := map[string]bool{"direct": true}
	for _, route := range opts.ChainRoutes {
		tag, ok := inboundTags[route.InboundID]
		if !ok {
			return nil, fmt.Errorf("代理链路由入站未启用：%s", route.InboundID)
		}
		if route.Outbound == nil {
			return nil, fmt.Errorf("入站 %s 缺少代理链路由出站", route.InboundID)
		}
		outboundTag, _ := route.Outbound["tag"].(string)
		if strings.TrimSpace(outboundTag) == "" {
			return nil, fmt.Errorf("入站 %s 的代理链路由必须提供出站标签", route.InboundID)
		}
		if outboundTags[outboundTag] {
			return nil, fmt.Errorf("出站标签 %q 重复", outboundTag)
		}
		outboundTags[outboundTag] = true
		outbounds = append(outbounds, route.Outbound)
		rules = append(rules, map[string]any{
			"inbound":  []string{tag},
			"action":   "route",
			"outbound": outboundTag,
		})
	}
	for _, rule := range opts.RouteRules {
		condition := routeRuleCondition(rule.MatchType, rule.MatchValue)
		if condition == nil {
			continue
		}
		if rule.Reject {
			condition["action"] = "reject"
			rules = append(rules, condition)
			continue
		}
		outboundTag := strings.TrimSpace(rule.Outbound)
		if outboundTag == "" || !outboundTags[outboundTag] {
			// Never emit a dangling outbound reference: sing-box refuses to
			// start when a route rule points at an unknown outbound.
			continue
		}
		condition["action"] = "route"
		condition["outbound"] = outboundTag
		rules = append(rules, condition)
	}
	routeOptions := map[string]any{"final": "direct"}
	if len(rules) > 0 {
		routeOptions["rules"] = rules
	}
	cfg := map[string]any{
		"log": map[string]any{
			"level": "info",
		},
		"inbounds":  outInbounds,
		"outbounds": outbounds,
		"route":     routeOptions,
	}
	return json.Marshal(cfg)
}

func label(in store.InboundConfig) string {
	if in.Name != "" {
		return in.Name
	}
	return in.ID
}

// routeRuleCondition maps a match type/value to the sing-box 1.12 rule
// condition fields. Returns nil for unsupported types (e.g. process_name,
// which only applies to client-side subscription configs).
func routeRuleCondition(matchType, matchValue string) map[string]any {
	value := strings.TrimSpace(matchValue)
	if value == "" {
		return nil
	}
	switch matchType {
	case "domain":
		return map[string]any{"domain": []string{value}}
	case "domain_suffix":
		return map[string]any{"domain_suffix": []string{value}}
	case "domain_keyword":
		return map[string]any{"domain_keyword": []string{value}}
	case "ip_cidr":
		return map[string]any{"ip_cidr": []string{value}}
	default:
		return nil
	}
}

func mapInbound(in store.InboundConfig) (map[string]any, error) {
	if in.Params == nil {
		return nil, fmt.Errorf("缺少参数")
	}
	switch in.Protocol {
	case "shadowsocks":
		return mapShadowsocks(in)
	case "trojan":
		return mapTrojan(in)
	case "vless":
		return mapVLESS(in)
	case "hysteria2":
		return mapHysteria2(in)
	case "tuic":
		return mapTUIC(in)
	case "anytls":
		return mapAnyTLS(in)
	case "vmess":
		return mapVMess(in)
	case "http":
		return mapUserAuthInbound(in, "http")
	case "socks5":
		return mapUserAuthInbound(in, "socks")
	case "auto":
		return mapUserAuthInbound(in, "mixed")
	default:
		return nil, fmt.Errorf("不支持协议 %q", in.Protocol)
	}
}

func mapShadowsocks(in store.InboundConfig) (map[string]any, error) {
	listen, port, err := requireListenPort(in.Params)
	if err != nil {
		return nil, err
	}
	method, err := requireString(in.Params, "method")
	if err != nil {
		return nil, err
	}
	password, err := requireString(in.Params, "password")
	if err != nil {
		return nil, err
	}
	if err := inboundfill.ValidateShadowsocks2022Password(method, password); err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":        "shadowsocks",
		"tag":         inboundTag(in),
		"listen":      listen,
		"listen_port": port,
		"method":      method,
		"password":    password,
	}
	if network := optionalString(in.Params, "network"); network != "" {
		out["network"] = network
	}
	return out, nil
}

func mapTrojan(in store.InboundConfig) (map[string]any, error) {
	listen, port, err := requireListenPort(in.Params)
	if err != nil {
		return nil, err
	}
	password, err := requireString(in.Params, "password")
	if err != nil {
		return nil, err
	}
	tlsBlock, err := buildTLS(in.Params, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":        "trojan",
		"tag":         inboundTag(in),
		"listen":      listen,
		"listen_port": port,
		"users": []map[string]any{
			{"name": "default", "password": password},
		},
		"tls": tlsBlock,
	}, nil
}

func mapVLESS(in store.InboundConfig) (map[string]any, error) {
	listen, port, err := requireListenPort(in.Params)
	if err != nil {
		return nil, err
	}
	uid, err := requireString(in.Params, "uuid")
	if err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(uid); err != nil {
		return nil, fmt.Errorf("UUID 无效：%w", err)
	}
	user := map[string]any{
		"name": "default",
		"uuid": uid,
	}
	if flow := optionalString(in.Params, "flow"); flow != "" {
		user["flow"] = flow
	}
	out := map[string]any{
		"type":        "vless",
		"tag":         inboundTag(in),
		"listen":      listen,
		"listen_port": port,
		"users":       []map[string]any{user},
	}

	tlsMode := optionalString(in.Params, "tls_mode")
	if tlsMode == "" {
		tlsMode = "none"
	}
	switch tlsMode {
	case "none":
		// no tls block
	case "tls":
		tls, err := buildTLS(in.Params, true)
		if err != nil {
			return nil, err
		}
		if sn := optionalString(in.Params, "server_name"); sn != "" {
			tls["server_name"] = sn
		}
		out["tls"] = tls
	case "reality":
		// Prefer private_key (inbound Reality); accept public_key as alias for ops mistakes.
		priv := optionalString(in.Params, "private_key")
		if priv == "" {
			priv = optionalString(in.Params, "public_key")
		}
		if priv == "" {
			return nil, fmt.Errorf("缺少必填字段 private_key")
		}
		shortID, err := requireString(in.Params, "short_id")
		if err != nil {
			return nil, err
		}
		serverName, err := requireString(in.Params, "server_name")
		if err != nil {
			return nil, err
		}
		hsServer, err := requireString(in.Params, "handshake_server")
		if err != nil {
			return nil, err
		}
		hsPort := 443
		if v, ok := in.Params["handshake_server_port"]; ok && v != nil && fmt.Sprint(v) != "" {
			p, err := asInt(v)
			if err != nil {
				return nil, fmt.Errorf("handshake_server_port 无效：%w", err)
			}
			hsPort = p
		}
		out["tls"] = map[string]any{
			"enabled":     true,
			"server_name": serverName,
			"reality": map[string]any{
				"enabled": true,
				"handshake": map[string]any{
					"server":      hsServer,
					"server_port": hsPort,
				},
				"private_key": priv,
				"short_id":    []string{shortID},
			},
		}
	default:
		return nil, fmt.Errorf("tls_mode %q 无效", tlsMode)
	}
	return out, nil
}

func mapHysteria2(in store.InboundConfig) (map[string]any, error) {
	listen, port, err := requireListenPort(in.Params)
	if err != nil {
		return nil, err
	}
	password, err := requireString(in.Params, "password")
	if err != nil {
		return nil, err
	}
	tlsBlock, err := buildTLS(in.Params, true)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":        "hysteria2",
		"tag":         inboundTag(in),
		"listen":      listen,
		"listen_port": port,
		"users": []map[string]any{
			{"name": "default", "password": password},
		},
		"tls": tlsBlock,
	}
	if v, ok := in.Params["up_mbps"]; ok && v != nil && fmt.Sprint(v) != "" {
		n, err := asInt(v)
		if err != nil {
			return nil, fmt.Errorf("up_mbps 无效：%w", err)
		}
		if n > 0 {
			out["up_mbps"] = n
		}
	}
	if v, ok := in.Params["down_mbps"]; ok && v != nil && fmt.Sprint(v) != "" {
		n, err := asInt(v)
		if err != nil {
			return nil, fmt.Errorf("down_mbps 无效：%w", err)
		}
		if n > 0 {
			out["down_mbps"] = n
		}
	}
	return out, nil
}

func mapTUIC(in store.InboundConfig) (map[string]any, error) {
	listen, port, err := requireListenPort(in.Params)
	if err != nil {
		return nil, err
	}
	uid, err := requireString(in.Params, "uuid")
	if err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(uid); err != nil {
		return nil, fmt.Errorf("UUID 无效：%w", err)
	}
	password, err := requireString(in.Params, "password")
	if err != nil {
		return nil, err
	}
	tlsBlock, err := buildTLS(in.Params, true)
	if err != nil {
		return nil, err
	}
	if sn := optionalString(in.Params, "server_name"); sn != "" {
		tlsBlock["server_name"] = sn
	}
	out := map[string]any{
		"type":        "tuic",
		"tag":         inboundTag(in),
		"listen":      listen,
		"listen_port": port,
		"users": []map[string]any{
			{"name": "default", "uuid": uid, "password": password},
		},
		"tls": tlsBlock,
	}
	if cc := optionalString(in.Params, "congestion_control"); cc != "" {
		switch cc {
		case "cubic", "new_reno", "bbr":
			out["congestion_control"] = cc
		default:
			return nil, fmt.Errorf("congestion_control %q 无效", cc)
		}
	}
	if v, ok := in.Params["zero_rtt_handshake"]; ok && v != nil {
		if b, ok := asBool(v); ok {
			out["zero_rtt_handshake"] = b
		}
	}
	return out, nil
}

func mapAnyTLS(in store.InboundConfig) (map[string]any, error) {
	listen, port, err := requireListenPort(in.Params)
	if err != nil {
		return nil, err
	}
	password, err := requireString(in.Params, "password")
	if err != nil {
		return nil, err
	}
	tlsBlock, err := buildTLS(in.Params, true)
	if err != nil {
		return nil, err
	}
	if sn := optionalString(in.Params, "server_name"); sn != "" {
		tlsBlock["server_name"] = sn
	}
	return map[string]any{
		"type":        "anytls",
		"tag":         inboundTag(in),
		"listen":      listen,
		"listen_port": port,
		"users": []map[string]any{
			{"name": "default", "password": password},
		},
		"tls": tlsBlock,
	}, nil
}

func mapVMess(in store.InboundConfig) (map[string]any, error) {
	listen, port, err := requireListenPort(in.Params)
	if err != nil {
		return nil, err
	}
	uid, err := requireString(in.Params, "uuid")
	if err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(uid); err != nil {
		return nil, fmt.Errorf("UUID 无效：%w", err)
	}
	alterID := 0
	if v, ok := in.Params["alter_id"]; ok && v != nil && fmt.Sprint(v) != "" {
		n, err := asInt(v)
		if err != nil {
			return nil, fmt.Errorf("alter_id 无效：%w", err)
		}
		if n < 0 {
			return nil, fmt.Errorf("alter_id 必须大于或等于 0")
		}
		alterID = n
	}
	user := map[string]any{
		"name":    "default",
		"uuid":    uid,
		"alterId": alterID,
	}
	out := map[string]any{
		"type":        "vmess",
		"tag":         inboundTag(in),
		"listen":      listen,
		"listen_port": port,
		"users":       []map[string]any{user},
	}
	tlsMode := optionalString(in.Params, "tls_mode")
	if tlsMode == "" {
		tlsMode = "none"
	}
	switch tlsMode {
	case "none":
	case "tls":
		tls, err := buildTLS(in.Params, true)
		if err != nil {
			return nil, err
		}
		if sn := optionalString(in.Params, "server_name"); sn != "" {
			tls["server_name"] = sn
		}
		out["tls"] = tls
	default:
		return nil, fmt.Errorf("tls_mode %q 无效", tlsMode)
	}
	return out, nil
}

func mapUserAuthInbound(in store.InboundConfig, singboxType string) (map[string]any, error) {
	listen, port, err := requireListenPort(in.Params)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":        singboxType,
		"tag":         inboundTag(in),
		"listen":      listen,
		"listen_port": port,
	}
	authMode := optionalString(in.Params, "auth_mode")
	if authMode == "" {
		authMode = "password"
	}
	switch authMode {
	case "none":
		return out, nil
	case "password":
		username, err := requireString(in.Params, "username")
		if err != nil {
			return nil, err
		}
		password, err := requireString(in.Params, "password")
		if err != nil {
			return nil, err
		}
		out["users"] = []map[string]any{
			{"username": username, "password": password},
		}
		return out, nil
	default:
		return nil, fmt.Errorf("auth_mode %q 无效", authMode)
	}
}

func asBool(v any) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
	case float64:
		return b != 0, true
	case int:
		return b != 0, true
	}
	return false, false
}

// buildTLS builds a sing-box tls object from PEM (preferred) or file paths.
func buildTLS(params map[string]any, required bool) (map[string]any, error) {
	certPEM := optionalString(params, "tls_cert_pem")
	keyPEM := optionalString(params, "tls_key_pem")
	if certPEM != "" && keyPEM != "" {
		tls := map[string]any{
			"enabled":     true,
			"certificate": []string{certPEM},
			"key":         []string{keyPEM},
		}
		if serverName := optionalString(params, "server_name"); serverName != "" {
			tls["server_name"] = serverName
		}
		return tls, nil
	}
	certPath := optionalString(params, "tls_cert_path")
	keyPath := optionalString(params, "tls_key_path")
	if certPath != "" && keyPath != "" {
		tls := map[string]any{
			"enabled":          true,
			"certificate_path": certPath,
			"key_path":         keyPath,
		}
		if serverName := optionalString(params, "server_name"); serverName != "" {
			tls["server_name"] = serverName
		}
		return tls, nil
	}
	if required {
		return nil, fmt.Errorf("缺少 TLS 材料（tls_cert_pem/tls_key_pem 或 tls_cert_path/tls_key_path）")
	}
	return map[string]any{"enabled": false}, nil
}

func requireListenPort(params map[string]any) (listen string, port int, err error) {
	listen = optionalString(params, "listen")
	if listen == "" {
		listen = "0.0.0.0"
	}
	if _, ok := params["port"]; !ok || params["port"] == nil {
		return "", 0, fmt.Errorf("缺少必填字段 port")
	}
	port, err = asInt(params["port"])
	if err != nil {
		return "", 0, fmt.Errorf("端口无效：%w", err)
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("端口超出有效范围：%d", port)
	}
	return listen, port, nil
}

func requireString(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok || v == nil {
		return "", fmt.Errorf("缺少必填字段 %s", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("字段 %s 必须是字符串", key)
	}
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("缺少必填字段 %s", key)
	}
	return s, nil
}

func optionalString(params map[string]any, key string) string {
	v, ok := params[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprint(v)
	}
	return s
}

func asInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int32:
		return int(n), nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	case float32:
		return int(n), nil
	case json.Number:
		i, err := n.Int64()
		return int(i), err
	case string:
		return strconv.Atoi(strings.TrimSpace(n))
	default:
		return 0, fmt.Errorf("无法将 %T 转换为整数", v)
	}
}

var nonTagChars = regexp.MustCompile(`[^a-z0-9]+`)

func inboundTag(in store.InboundConfig) string {
	base := strings.ToLower(strings.TrimSpace(in.Name))
	base = nonTagChars.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		id := in.ID
		if len(id) > 8 {
			id = id[:8]
		}
		base = id
	}
	if base == "" {
		base = "inbound"
	}
	id := strings.ToLower(strings.TrimSpace(in.ID))
	id = nonTagChars.ReplaceAllString(id, "")
	if len(id) > 8 {
		id = id[:8]
	}
	if id != "" {
		return "in-" + base + "-" + id
	}
	return "in-" + base
}
