package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// parseShareLinks parses line-separated share URIs (optional #name fragment).
func parseShareLinks(raw []byte) ([]ProxyEndpoint, error) {
	lines := strings.Split(string(raw), "\n")
	var out []ProxyEndpoint
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Some providers put multiple links separated by whitespace on one line.
		for _, part := range strings.Fields(line) {
			ep, err := parseOneShareURI(part)
			if err != nil {
				continue
			}
			out = append(out, ep)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("分享链接中没有受支持的代理")
	}
	return out, nil
}

func parseOneShareURI(raw string) (ProxyEndpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ProxyEndpoint{}, fmt.Errorf("链接为空")
	}
	scheme, _, ok := strings.Cut(raw, "://")
	if !ok {
		return ProxyEndpoint{}, fmt.Errorf("链接缺少协议方案")
	}
	switch strings.ToLower(scheme) {
	case "ss":
		return parseSSURI(raw)
	case "vmess":
		return parseVMessURI(raw)
	case "vless":
		return parseVLESSURI(raw)
	case "trojan":
		return parseTrojanURI(raw)
	case "hysteria2", "hy2":
		return parseHysteria2URI(raw)
	case "tuic":
		return parseTUICURI(raw)
	case "anytls":
		return parseAnyTLSURI(raw)
	case "http":
		return parseHTTPOrSocksURI(raw, "http")
	case "socks", "socks5":
		return parseHTTPOrSocksURI(raw, "socks5")
	default:
		return ProxyEndpoint{}, fmt.Errorf("不支持链接方案 %q", scheme)
	}
}

func parseSSURI(raw string) (ProxyEndpoint, error) {
	// SIP002: ss://base64(method:password)@host:port#name
	// Legacy: ss://base64(method:password@host:port)
	u, err := url.Parse(raw)
	if err != nil {
		return ProxyEndpoint{}, err
	}
	name := fragmentName(u)
	var method, password, host string
	var port int

	if u.User != nil {
		// SIP002 userinfo may be base64(method:password) or method:password
		userinfo := u.User.String()
		if decoded, ok := decodeBase64Loose(userinfo); ok && strings.Contains(string(decoded), ":") {
			userinfo = string(decoded)
		}
		method, password, _ = strings.Cut(userinfo, ":")
		if p, err := url.QueryUnescape(password); err == nil {
			password = p
		}
		host = u.Hostname()
		port, _ = strconv.Atoi(u.Port())
	} else {
		// legacy full body base64 after ss://
		body := strings.TrimPrefix(raw, "ss://")
		body = strings.TrimPrefix(body, "SS://")
		if i := strings.Index(body, "#"); i >= 0 {
			if name == "" {
				name, _ = url.QueryUnescape(body[i+1:])
			}
			body = body[:i]
		}
		if i := strings.Index(body, "?"); i >= 0 {
			body = body[:i]
		}
		decoded, ok := decodeBase64Loose(body)
		if !ok {
			return ProxyEndpoint{}, fmt.Errorf("Shadowsocks Base64 无效")
		}
		// method:password@host:port
		s := string(decoded)
		at := strings.LastIndex(s, "@")
		if at < 0 {
			return ProxyEndpoint{}, fmt.Errorf("Shadowsocks 旧格式内容无效")
		}
		method, password, _ = strings.Cut(s[:at], ":")
		hostport := s[at+1:]
		h, p, err := net.SplitHostPort(hostport)
		if err != nil {
			return ProxyEndpoint{}, err
		}
		host = h
		port, _ = strconv.Atoi(p)
	}
	if method == "" || password == "" || host == "" || port < 1 {
		return ProxyEndpoint{}, fmt.Errorf("Shadowsocks 链接不完整")
	}
	if name == "" {
		name = fmt.Sprintf("ss-%s-%d", host, port)
	}
	return ProxyEndpoint{
		Name:     sanitizeName(name),
		Server:   host,
		Port:     port,
		Protocol: "shadowsocks",
		Params: map[string]any{
			"method":   method,
			"password": password,
		},
	}, nil
}

func parseVMessURI(raw string) (ProxyEndpoint, error) {
	// vmess://base64(json)
	body := strings.TrimPrefix(raw, "vmess://")
	body = strings.TrimPrefix(body, "VMESS://")
	if i := strings.Index(body, "#"); i >= 0 {
		body = body[:i]
	}
	decoded, ok := decodeBase64Loose(body)
	if !ok {
		return ProxyEndpoint{}, fmt.Errorf("VMess Base64 无效")
	}
	var m map[string]any
	if err := json.Unmarshal(decoded, &m); err != nil {
		return ProxyEndpoint{}, fmt.Errorf("解析 VMess JSON 失败：%w", err)
	}
	host := anyToString(m["add"])
	port, ok := anyToInt(m["port"])
	if !ok || host == "" || port < 1 {
		return ProxyEndpoint{}, fmt.Errorf("VMess 缺少服务器地址或端口")
	}
	uid := anyToString(m["id"])
	if uid == "" {
		return ProxyEndpoint{}, fmt.Errorf("VMess 缺少 ID")
	}
	name := firstNonEmpty(anyToString(m["ps"]), fmt.Sprintf("vmess-%s-%d", host, port))
	params := map[string]any{"uuid": uid}
	if aid, ok := anyToInt(m["aid"]); ok {
		params["alter_id"] = aid
	}
	tls := strings.ToLower(anyToString(m["tls"]))
	if tls == "tls" || tls == "1" || tls == "true" {
		params["tls_mode"] = "tls"
	} else {
		params["tls_mode"] = "none"
	}
	if sn := firstNonEmpty(anyToString(m["sni"]), anyToString(m["host"])); sn != "" {
		params["server_name"] = sn
	}
	return ProxyEndpoint{
		Name:     sanitizeName(name),
		Server:   host,
		Port:     port,
		Protocol: "vmess",
		Params:   params,
	}, nil
}

func parseVLESSURI(raw string) (ProxyEndpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return ProxyEndpoint{}, err
	}
	uid := ""
	if u.User != nil {
		uid = u.User.Username()
	}
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if uid == "" || host == "" || port < 1 {
		return ProxyEndpoint{}, fmt.Errorf("VLESS 链接不完整")
	}
	q := u.Query()
	params := map[string]any{"uuid": uid}
	if flow := q.Get("flow"); flow != "" {
		params["flow"] = flow
	}
	security := strings.ToLower(q.Get("security"))
	switch security {
	case "reality":
		params["tls_mode"] = "reality"
		if pbk := q.Get("pbk"); pbk != "" {
			params["public_key"] = pbk
		}
		if sid := q.Get("sid"); sid != "" {
			params["short_id"] = sid
		}
	case "tls":
		params["tls_mode"] = "tls"
	default:
		params["tls_mode"] = "none"
	}
	if sn := firstNonEmpty(q.Get("sni"), q.Get("servername")); sn != "" {
		params["server_name"] = sn
	}
	name := fragmentName(u)
	if name == "" {
		name = fmt.Sprintf("vless-%s-%d", host, port)
	}
	return ProxyEndpoint{
		Name:          sanitizeName(name),
		Server:        host,
		Port:          port,
		Protocol:      "vless",
		Params:        params,
		TLSSkipVerify: allowInsecureFromQuery(q),
	}, nil
}

func parseTrojanURI(raw string) (ProxyEndpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return ProxyEndpoint{}, err
	}
	password := ""
	if u.User != nil {
		password = u.User.Username()
		if p, ok := u.User.Password(); ok && p != "" {
			password = password + ":" + p // rare
		}
	}
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if password == "" || host == "" || port < 1 {
		return ProxyEndpoint{}, fmt.Errorf("Trojan 链接不完整")
	}
	params := map[string]any{"password": password}
	if sn := firstNonEmpty(u.Query().Get("sni"), u.Query().Get("peer")); sn != "" {
		params["server_name"] = sn
	}
	name := fragmentName(u)
	if name == "" {
		name = fmt.Sprintf("trojan-%s-%d", host, port)
	}
	return ProxyEndpoint{
		Name:          sanitizeName(name),
		Server:        host,
		Port:          port,
		Protocol:      "trojan",
		Params:        params,
		TLSSkipVerify: allowInsecureFromQuery(u.Query()),
	}, nil
}

func parseHysteria2URI(raw string) (ProxyEndpoint, error) {
	// hysteria2://password@host:port?sni=...#name
	u, err := url.Parse(raw)
	if err != nil {
		return ProxyEndpoint{}, err
	}
	password := ""
	if u.User != nil {
		password = u.User.Username()
	}
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if password == "" || host == "" || port < 1 {
		return ProxyEndpoint{}, fmt.Errorf("Hysteria2 链接不完整")
	}
	params := map[string]any{"password": password}
	if sn := u.Query().Get("sni"); sn != "" {
		params["server_name"] = sn
	}
	name := fragmentName(u)
	if name == "" {
		name = fmt.Sprintf("hy2-%s-%d", host, port)
	}
	return ProxyEndpoint{
		Name:          sanitizeName(name),
		Server:        host,
		Port:          port,
		Protocol:      "hysteria2",
		Params:        params,
		TLSSkipVerify: allowInsecureFromQuery(u.Query()),
	}, nil
}

func parseTUICURI(raw string) (ProxyEndpoint, error) {
	// tuic://uuid:password@host:port?sni=...&congestion_control=...
	u, err := url.Parse(raw)
	if err != nil {
		return ProxyEndpoint{}, err
	}
	uid, password := "", ""
	if u.User != nil {
		uid = u.User.Username()
		password, _ = u.User.Password()
	}
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if uid == "" || password == "" || host == "" || port < 1 {
		return ProxyEndpoint{}, fmt.Errorf("TUIC 链接不完整")
	}
	params := map[string]any{"uuid": uid, "password": password}
	q := u.Query()
	if sn := q.Get("sni"); sn != "" {
		params["server_name"] = sn
	}
	if cc := firstNonEmpty(q.Get("congestion_control"), q.Get("congestion-controller")); cc != "" {
		params["congestion_control"] = cc
	}
	name := fragmentName(u)
	if name == "" {
		name = fmt.Sprintf("tuic-%s-%d", host, port)
	}
	return ProxyEndpoint{
		Name:          sanitizeName(name),
		Server:        host,
		Port:          port,
		Protocol:      "tuic",
		Params:        params,
		TLSSkipVerify: allowInsecureFromQuery(q),
	}, nil
}

func parseAnyTLSURI(raw string) (ProxyEndpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return ProxyEndpoint{}, err
	}
	password := ""
	if u.User != nil {
		password = u.User.Username()
	}
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if password == "" || host == "" || port < 1 {
		return ProxyEndpoint{}, fmt.Errorf("AnyTLS 链接不完整")
	}
	params := map[string]any{"password": password}
	if sn := u.Query().Get("sni"); sn != "" {
		params["server_name"] = sn
	}
	name := fragmentName(u)
	if name == "" {
		name = fmt.Sprintf("anytls-%s-%d", host, port)
	}
	return ProxyEndpoint{
		Name:          sanitizeName(name),
		Server:        host,
		Port:          port,
		Protocol:      "anytls",
		Params:        params,
		TLSSkipVerify: allowInsecureFromQuery(u.Query()),
	}, nil
}

func parseHTTPOrSocksURI(raw, protocol string) (ProxyEndpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return ProxyEndpoint{}, err
	}
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil || host == "" || port < 1 || port > 65535 {
		return ProxyEndpoint{}, fmt.Errorf("%s 链接缺少主机或端口", protocol)
	}
	params := map[string]any{}
	username, password := "", ""
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	applyParsedUserAuth(params, username, password)
	name := fragmentName(u)
	if name == "" {
		name = fmt.Sprintf("%s-%s-%d", protocol, host, port)
	}
	return ProxyEndpoint{
		Name:     sanitizeName(name),
		Server:   host,
		Port:     port,
		Protocol: protocol,
		Params:   params,
	}, nil
}

func fragmentName(u *url.URL) string {
	if u == nil || u.Fragment == "" {
		return ""
	}
	name, err := url.QueryUnescape(u.Fragment)
	if err != nil {
		return u.Fragment
	}
	return name
}

// allowInsecureFromQuery maps the allowInsecure / allow_insecure share-link
// flag onto TLSSkipVerify. Nil when the source link does not specify it.
func allowInsecureFromQuery(q url.Values) *bool {
	for _, key := range []string{"allowInsecure", "allow_insecure"} {
		if raw, ok := q[key]; ok && len(raw) > 0 {
			skip := anyToBool(raw[0])
			return &skip
		}
	}
	return nil
}

// decodeBase64Loose tries std/raw/url encodings without the printable heuristics of tryBase64Decode.
func decodeBase64Loose(s string) ([]byte, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		out, err := enc.DecodeString(s)
		if err == nil && len(out) > 0 {
			return out, true
		}
	}
	return nil, false
}
