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

// RenderV2ray renders proxy endpoints as line-separated share URIs,
// base64-encoded according to V2Ray subscription standard.
func RenderV2ray(endpoints []ProxyEndpoint) ([]byte, error) {
	var lines []string
	for _, ep := range endpoints {
		uri, err := renderOneShareURI(ep)
		if err != nil {
			// Skip endpoints that fail rendering or return error for invalid params
			continue
		}
		if uri != "" {
			lines = append(lines, uri)
		}
	}
	raw := strings.Join(lines, "\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(raw))
	return []byte(encoded), nil
}

func renderOneShareURI(ep ProxyEndpoint) (string, error) {
	switch strings.ToLower(ep.Protocol) {
	case "shadowsocks":
		return renderSSURI(ep)
	case "vmess":
		return renderVMessURI(ep)
	case "vless":
		return renderVLESSURI(ep)
	case "trojan":
		return renderTrojanURI(ep)
	case "hysteria2", "hy2":
		return renderHysteria2URI(ep)
	case "tuic":
		return renderTUICURI(ep)
	case "anytls":
		return renderAnyTLSURI(ep)
	case "http":
		return renderHTTPOrSocksURI("http", ep)
	case "socks5", "auto":
		return renderHTTPOrSocksURI("socks5", ep)
	default:
		return "", fmt.Errorf("不支持协议 %q", ep.Protocol)
	}
}

func formatHostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func renderSSURI(ep ProxyEndpoint) (string, error) {
	method, _ := paramString(ep.Params, "method")
	password, _ := paramString(ep.Params, "password")
	if method == "" || password == "" {
		return "", fmt.Errorf("Shadowsocks 缺少加密方法或密码")
	}
	userInfo := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + password))
	hp := formatHostPort(ep.Server, ep.Port)
	return fmt.Sprintf("ss://%s@%s#%s", userInfo, hp, url.QueryEscape(ep.Name)), nil
}

func renderVMessURI(ep ProxyEndpoint) (string, error) {
	uid, _ := paramString(ep.Params, "uuid")
	if uid == "" {
		return "", fmt.Errorf("VMess 缺少 UUID")
	}
	alterID := 0
	if n, err := paramInt(ep.Params, "alter_id"); err == nil && n >= 0 {
		alterID = n
	}
	tlsMode, _ := paramString(ep.Params, "tls_mode")
	tlsStr := "none"
	if tlsMode == "tls" {
		tlsStr = "tls"
	}
	sni := paramStringMust(ep.Params, "server_name")

	m := map[string]any{
		"v":    "2",
		"ps":   ep.Name,
		"add":  ep.Server,
		"port": ep.Port,
		"id":   uid,
		"aid":  alterID,
		"net":  "tcp",
		"type": "none",
		"host": sni,
		"path": "",
		"tls":  tlsStr,
		"sni":  sni,
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(b)
	return "vmess://" + encoded, nil
}

func renderVLESSURI(ep ProxyEndpoint) (string, error) {
	uid, _ := paramString(ep.Params, "uuid")
	if uid == "" {
		return "", fmt.Errorf("VLESS 缺少 UUID")
	}
	q := url.Values{}
	q.Set("type", "tcp")

	tlsMode, _ := paramString(ep.Params, "tls_mode")
	switch tlsMode {
	case "reality":
		q.Set("security", "reality")
		if pub, err := resolveRealityPublicKey(ep.Params); err == nil && pub != "" {
			q.Set("pbk", pub)
		}
		if sid, ok := paramString(ep.Params, "short_id"); ok && sid != "" {
			q.Set("sid", sid)
		}
	case "tls":
		q.Set("security", "tls")
	default:
		q.Set("security", "none")
	}

	if sni := paramStringMust(ep.Params, "server_name"); sni != "" {
		q.Set("sni", sni)
	}
	if flow, ok := paramString(ep.Params, "flow"); ok && flow != "" {
		q.Set("flow", flow)
	}

	hp := formatHostPort(ep.Server, ep.Port)
	return fmt.Sprintf("vless://%s@%s?%s#%s", uid, hp, q.Encode(), url.QueryEscape(ep.Name)), nil
}

func renderTrojanURI(ep ProxyEndpoint) (string, error) {
	password, _ := paramString(ep.Params, "password")
	if password == "" {
		return "", fmt.Errorf("Trojan 缺少密码")
	}
	q := url.Values{}
	if sni := paramStringMust(ep.Params, "server_name"); sni != "" {
		q.Set("sni", sni)
	}
	hp := formatHostPort(ep.Server, ep.Port)
	u := fmt.Sprintf("trojan://%s@%s", password, hp)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	u += "#" + url.QueryEscape(ep.Name)
	return u, nil
}

func renderHysteria2URI(ep ProxyEndpoint) (string, error) {
	password, _ := paramString(ep.Params, "password")
	if password == "" {
		return "", fmt.Errorf("Hysteria2 缺少密码")
	}
	q := url.Values{}
	if sni := paramStringMust(ep.Params, "server_name"); sni != "" {
		q.Set("sni", sni)
	}
	hp := formatHostPort(ep.Server, ep.Port)
	u := fmt.Sprintf("hysteria2://%s@%s", password, hp)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	u += "#" + url.QueryEscape(ep.Name)
	return u, nil
}

func renderTUICURI(ep ProxyEndpoint) (string, error) {
	uid, _ := paramString(ep.Params, "uuid")
	password, _ := paramString(ep.Params, "password")
	if uid == "" || password == "" {
		return "", fmt.Errorf("TUIC 缺少 UUID 或密码")
	}
	q := url.Values{}
	if sni := paramStringMust(ep.Params, "server_name"); sni != "" {
		q.Set("sni", sni)
	}
	if cc, ok := paramString(ep.Params, "congestion_control"); ok && cc != "" {
		q.Set("congestion_control", cc)
	}
	hp := formatHostPort(ep.Server, ep.Port)
	u := fmt.Sprintf("tuic://%s:%s@%s", uid, password, hp)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	u += "#" + url.QueryEscape(ep.Name)
	return u, nil
}

func renderAnyTLSURI(ep ProxyEndpoint) (string, error) {
	password, _ := paramString(ep.Params, "password")
	if password == "" {
		return "", fmt.Errorf("AnyTLS 缺少密码")
	}
	q := url.Values{}
	if sni := paramStringMust(ep.Params, "server_name"); sni != "" {
		q.Set("sni", sni)
	}
	hp := formatHostPort(ep.Server, ep.Port)
	u := fmt.Sprintf("anytls://%s@%s", password, hp)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	u += "#" + url.QueryEscape(ep.Name)
	return u, nil
}

func renderHTTPOrSocksURI(scheme string, ep ProxyEndpoint) (string, error) {
	hp := formatHostPort(ep.Server, ep.Port)
	user, _ := paramString(ep.Params, "username")
	pass, _ := paramString(ep.Params, "password")
	auth := ""
	if user != "" || pass != "" {
		auth = url.UserPassword(user, pass).String() + "@"
	}
	return fmt.Sprintf("%s://%s%s#%s", scheme, auth, hp, url.QueryEscape(ep.Name)), nil
}
