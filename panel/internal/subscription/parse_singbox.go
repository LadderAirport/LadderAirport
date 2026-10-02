package subscription

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseSingbox reads client outbounds from a sing-box JSON config.
func parseSingbox(raw []byte) ([]ProxyEndpoint, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析 sing-box JSON 失败：%w", err)
	}
	rawOut, ok := doc["outbounds"]
	if !ok {
		return nil, fmt.Errorf("sing-box JSON 缺少 outbounds")
	}
	list, ok := rawOut.([]any)
	if !ok {
		return nil, fmt.Errorf("sing-box JSON 的 outbounds 不是列表")
	}
	var out []ProxyEndpoint
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ep, err := singboxMapToEndpoint(m)
		if err != nil {
			continue
		}
		out = append(out, ep)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sing-box JSON 中没有受支持的出站")
	}
	return out, nil
}

func singboxMapToEndpoint(m map[string]any) (ProxyEndpoint, error) {
	typ := strings.ToLower(anyToString(m["type"]))
	switch typ {
	case "selector", "urltest", "direct", "block", "dns", "tor", "ssh",
		"wireguard", "hysteria", "shadowtls", "naive",
		"redirect", "tproxy", "tun", "mixed", "shadowsocks-legacy":
		return ProxyEndpoint{}, fmt.Errorf("跳过类型 %s", typ)
	}

	server := anyToString(m["server"])
	port, ok := anyToInt(m["server_port"])
	if !ok {
		port, ok = anyToInt(m["serverPort"])
	}
	if !ok || server == "" || port < 1 || port > 65535 {
		return ProxyEndpoint{}, fmt.Errorf("缺少服务器地址或端口")
	}
	name := anyToString(m["tag"])
	if name == "" {
		name = fmt.Sprintf("%s-%s-%d", typ, server, port)
	}
	params := map[string]any{}
	protocol := ""

	switch typ {
	case "shadowsocks":
		protocol = "shadowsocks"
		method := anyToString(m["method"])
		password := anyToString(m["password"])
		if method == "" || password == "" {
			return ProxyEndpoint{}, fmt.Errorf("Shadowsocks 配置不完整")
		}
		params["method"] = method
		params["password"] = password
	case "trojan":
		protocol = "trojan"
		password := anyToString(m["password"])
		if password == "" {
			return ProxyEndpoint{}, fmt.Errorf("Trojan 配置不完整")
		}
		params["password"] = password
		applySingboxTLS(m, params)
	case "vless":
		protocol = "vless"
		uid := anyToString(m["uuid"])
		if uid == "" {
			return ProxyEndpoint{}, fmt.Errorf("VLESS 配置不完整")
		}
		params["uuid"] = uid
		if flow := anyToString(m["flow"]); flow != "" {
			params["flow"] = flow
		}
		applySingboxTLS(m, params)
	case "hysteria2":
		protocol = "hysteria2"
		password := anyToString(m["password"])
		if password == "" {
			return ProxyEndpoint{}, fmt.Errorf("Hysteria2 配置不完整")
		}
		params["password"] = password
		if up, ok := anyToInt(m["up_mbps"]); ok {
			params["up_mbps"] = up
		}
		if down, ok := anyToInt(m["down_mbps"]); ok {
			params["down_mbps"] = down
		}
		applySingboxTLS(m, params)
	case "tuic":
		protocol = "tuic"
		uid := anyToString(m["uuid"])
		password := anyToString(m["password"])
		if uid == "" || password == "" {
			return ProxyEndpoint{}, fmt.Errorf("TUIC 配置不完整")
		}
		params["uuid"] = uid
		params["password"] = password
		if cc := anyToString(m["congestion_control"]); cc != "" {
			params["congestion_control"] = cc
		}
		applySingboxTLS(m, params)
	case "anytls":
		protocol = "anytls"
		password := anyToString(m["password"])
		if password == "" {
			return ProxyEndpoint{}, fmt.Errorf("AnyTLS 配置不完整")
		}
		params["password"] = password
		applySingboxTLS(m, params)
	case "vmess":
		protocol = "vmess"
		uid := anyToString(m["uuid"])
		if uid == "" {
			return ProxyEndpoint{}, fmt.Errorf("VMess 配置不完整")
		}
		params["uuid"] = uid
		if aid, ok := anyToInt(m["alter_id"]); ok {
			params["alter_id"] = aid
		}
		applySingboxTLS(m, params)
	case "http":
		protocol = "http"
		applyParsedUserAuth(params, anyToString(m["username"]), anyToString(m["password"]))
	case "socks":
		protocol = "socks5"
		applyParsedUserAuth(params, anyToString(m["username"]), anyToString(m["password"]))
	default:
		return ProxyEndpoint{}, fmt.Errorf("不支持类型 %q", typ)
	}

	return ProxyEndpoint{
		Name:     sanitizeName(name),
		Server:   server,
		Port:     port,
		Protocol: protocol,
		Params:   params,
	}, nil
}

func applySingboxTLS(m map[string]any, params map[string]any) {
	tls, ok := m["tls"].(map[string]any)
	if !ok {
		// leave tls_mode unset/none for protocols that need it
		if _, has := params["tls_mode"]; !has {
			// only vmess/vless care; others always use TLS in our renderer
			params["tls_mode"] = "none"
		}
		return
	}
	if !anyToBool(tls["enabled"]) {
		params["tls_mode"] = "none"
		return
	}
	if sn := anyToString(tls["server_name"]); sn != "" {
		params["server_name"] = sn
	}
	if reality, ok := tls["reality"].(map[string]any); ok && anyToBool(reality["enabled"]) {
		params["tls_mode"] = "reality"
		if pub := anyToString(reality["public_key"]); pub != "" {
			params["public_key"] = pub
		}
		if sid := anyToString(reality["short_id"]); sid != "" {
			params["short_id"] = sid
		}
		return
	}
	params["tls_mode"] = "tls"
}
