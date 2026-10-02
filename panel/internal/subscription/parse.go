package subscription

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode"
)

const (
	ContentClashYAML   = "clash_yaml"
	ContentShareLinks  = "share_links"
	ContentSingboxJSON = "singbox_json"
)

// MaxProxiesPerSource caps how many proxies we accept from one external body.
const MaxProxiesPerSource = 2000

// DetectAndParse inspects a raw subscription body and normalizes proxies to ProxyEndpoint.
// Detection order: sing-box JSON → base64-wrapped share links → plain share links → Clash YAML.
func DetectAndParse(raw []byte) ([]ProxyEndpoint, string, error) {
	raw = bytes.TrimSpace(stripBOM(raw))
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("订阅内容为空")
	}

	// 1) sing-box client JSON
	if looksLikeJSONObject(raw) && bytes.Contains(raw, []byte(`"outbounds"`)) {
		eps, err := parseSingbox(raw)
		if err == nil {
			return truncateProxies(eps), ContentSingboxJSON, nil
		}
		// fall through — might be something else JSON-ish
	}

	// 2) base64-wrapped share links (common airport format)
	if decoded, ok := tryBase64Decode(raw); ok {
		decoded = bytes.TrimSpace(stripBOM(decoded))
		if looksLikeShareLinks(decoded) {
			eps, err := parseShareLinks(decoded)
			if err == nil && len(eps) > 0 {
				return truncateProxies(eps), ContentShareLinks, nil
			}
		}
		// some providers base64-wrap Clash YAML
		if looksLikeClashYAML(decoded) {
			eps, err := parseClashYAML(decoded)
			if err == nil && len(eps) > 0 {
				return truncateProxies(eps), ContentClashYAML, nil
			}
		}
	}

	// 3) plain share links
	if looksLikeShareLinks(raw) {
		eps, err := parseShareLinks(raw)
		if err == nil && len(eps) > 0 {
			return truncateProxies(eps), ContentShareLinks, nil
		}
	}

	// 4) Clash / Mihomo YAML
	if looksLikeClashYAML(raw) || bytes.Contains(raw, []byte("proxies:")) {
		eps, err := parseClashYAML(raw)
		if err != nil {
			return nil, "", err
		}
		return truncateProxies(eps), ContentClashYAML, nil
	}

	// Last resort: try each parser and take the first non-empty success.
	if eps, err := parseSingbox(raw); err == nil && len(eps) > 0 {
		return truncateProxies(eps), ContentSingboxJSON, nil
	}
	if eps, err := parseShareLinks(raw); err == nil && len(eps) > 0 {
		return truncateProxies(eps), ContentShareLinks, nil
	}
	if eps, err := parseClashYAML(raw); err == nil && len(eps) > 0 {
		return truncateProxies(eps), ContentClashYAML, nil
	}

	return nil, "", fmt.Errorf("无法识别外部订阅格式")
}

func truncateProxies(eps []ProxyEndpoint) []ProxyEndpoint {
	eps = filterDialableEndpoints(eps)
	if len(eps) > MaxProxiesPerSource {
		return eps[:MaxProxiesPerSource]
	}
	return eps
}

func stripBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
}

func looksLikeJSONObject(b []byte) bool {
	b = bytes.TrimSpace(b)
	return len(b) > 0 && b[0] == '{'
}

func looksLikeClashYAML(b []byte) bool {
	s := string(b)
	return strings.Contains(s, "proxies:") || strings.Contains(s, "proxy-groups:") ||
		strings.Contains(s, "mixed-port:") || strings.Contains(s, "port:")
}

func looksLikeShareLinks(b []byte) bool {
	s := string(b)
	for _, scheme := range []string{
		"ss://", "vmess://", "vless://", "trojan://",
		"hysteria2://", "hy2://", "tuic://", "anytls://",
		"socks5://", "socks://",
	} {
		if strings.Contains(s, scheme) {
			return true
		}
	}
	return false
}

func tryBase64Decode(raw []byte) ([]byte, bool) {
	// Only attempt when body looks base64-ish (no schemes, mostly alphabet).
	trim := bytes.TrimSpace(raw)
	if len(trim) < 16 {
		return nil, false
	}
	// If it already has share schemes or YAML keys, skip.
	if looksLikeShareLinks(trim) || looksLikeClashYAML(trim) || looksLikeJSONObject(trim) {
		return nil, false
	}
	// Strip whitespace/newlines common in airport base64 blobs.
	compact := make([]byte, 0, len(trim))
	for _, c := range trim {
		if unicode.IsSpace(rune(c)) {
			continue
		}
		compact = append(compact, c)
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		out, err := enc.DecodeString(string(compact))
		if err == nil && len(out) > 0 {
			// Heuristic: decoded should be printable-ish.
			if isMostlyPrintable(out) {
				return out, true
			}
		}
	}
	return nil, false
}

func isMostlyPrintable(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	bad := 0
	for _, c := range b {
		if c == '\n' || c == '\r' || c == '\t' {
			continue
		}
		if c < 32 || c > 126 {
			// allow utf-8 high bytes lightly
			if c >= 0x80 {
				continue
			}
			bad++
		}
	}
	return bad*10 < len(b) // <10% control bytes
}

func applyParsedUserAuth(params map[string]any, username, password string) {
	if username != "" {
		params["username"] = username
	}
	if password != "" {
		params["password"] = password
	}
	if username != "" || password != "" {
		params["auth_mode"] = "password"
	} else {
		params["auth_mode"] = "none"
	}
}

func anyToString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func anyToInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	case string:
		var i int
		_, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &i)
		return i, err == nil
	default:
		return 0, false
	}
}

func anyToBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "1" || s == "true" || s == "yes"
	case int:
		return t != 0
	case float64:
		return t != 0
	default:
		return false
	}
}
