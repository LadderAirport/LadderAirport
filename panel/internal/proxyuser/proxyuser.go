// Package proxyuser implements per-user protocol admission: credentials are
// derived from one random secret per user, written into sing-box inbound
// "users" lists on nodes, and substituted into the user's rendered
// subscription. Users never log in; their subscription link is all they get.
package proxyuser

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ladderairport/panel/internal/store"
)

// InternalUserName is the sing-box user the panel keeps on admission-only
// inbounds for node-side chain outbounds (global route plans). Its secret is
// per inbound and never rendered into subscriptions.
const InternalUserName = "__panel"

// Identity is one admitted principal. Name becomes the sing-box user name
// (and the HTTP/SOCKS username), so it must be stable and unique per inbound.
type Identity struct {
	Name   string
	Secret []byte
}

// NewSecret returns a fresh random secret encoded for storage.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成用户密钥失败：%w", err)
	}
	return hex.EncodeToString(b), nil
}

// ForUser builds the identity of a stored proxy user.
func ForUser(u store.ProxyUser) (Identity, error) {
	secret, err := decodeSecret(u.Secret)
	if err != nil {
		return Identity{}, fmt.Errorf("用户 %q：%w", u.Name, err)
	}
	return Identity{Name: u.ID, Secret: secret}, nil
}

// Internal builds the panel-internal identity of an admission-only inbound.
func Internal(a store.InboundAdmission) (Identity, error) {
	secret, err := decodeSecret(a.InternalSecret)
	if err != nil {
		return Identity{}, fmt.Errorf("入站 %s 的内部凭据：%w", a.InboundID, err)
	}
	return Identity{Name: InternalUserName, Secret: secret}, nil
}

func decodeSecret(s string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) < 16 {
		return nil, fmt.Errorf("密钥无效")
	}
	return b, nil
}

func (id Identity) derive(label string, n int) []byte {
	mac := hmac.New(sha256.New, id.Secret)
	mac.Write([]byte("ladder-proxyuser/" + label))
	out := mac.Sum(nil)
	return out[:n]
}

// UUID is the user's VLESS / VMess / TUIC UUID (RFC 4122 v4 layout).
func (id Identity) UUID() string {
	b := id.derive("uuid", 16)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	u, _ := uuid.FromBytes(b)
	return u.String()
}

// Password is the user's Trojan / Hysteria2 / TUIC / AnyTLS / HTTP / SOCKS password.
func (id Identity) Password() string {
	return base64.RawURLEncoding.EncodeToString(id.derive("password", 24))
}

// Username is the HTTP / SOCKS username.
func (id Identity) Username() string {
	return id.Name
}

// SSKey is the user's Shadowsocks 2022 identity key for method.
func (id Identity) SSKey(method string) (string, error) {
	n, ok := ss2022MultiUserKeyLen[method]
	if !ok {
		return "", fmt.Errorf("加密方式 %q 不支持多用户", method)
	}
	return base64.StdEncoding.EncodeToString(id.derive("ss2022/"+method, n)), nil
}

// Only the AES Shadowsocks 2022 methods support identity headers (multi-user).
var ss2022MultiUserKeyLen = map[string]int{
	"2022-blake3-aes-128-gcm": 16,
	"2022-blake3-aes-256-gcm": 32,
}

// Supports reports whether an inbound can carry per-user credentials, with a
// user-facing reason when it cannot.
func Supports(in store.InboundConfig) (bool, string) {
	switch in.Protocol {
	case "vless", "vmess", "trojan", "hysteria2", "tuic", "anytls":
		return true, ""
	case "http", "socks5", "auto":
		if paramString(in.Params, "auth_mode") == "none" {
			return false, "未开启认证（auth_mode 为 none）的 HTTP / SOCKS 入站无法区分用户"
		}
		return true, ""
	case "shadowsocks":
		method := paramString(in.Params, "method")
		if _, ok := ss2022MultiUserKeyLen[method]; !ok {
			return false, fmt.Sprintf("Shadowsocks 只有 2022-blake3-aes-128-gcm / 2022-blake3-aes-256-gcm 支持多用户，当前为 %q", method)
		}
		return true, ""
	default:
		return false, fmt.Sprintf("协议 %q 不支持多用户", in.Protocol)
	}
}

// UsesUsers reports whether per-user credentials are in effect for an
// inbound. Shadowsocks switches to multi-user mode (client password becomes
// "server-key:user-key") only when admission-only is on, so existing shared
// SS subscriptions keep working until the operator opts in.
func UsesUsers(in store.InboundConfig, admissionOnly bool) bool {
	if ok, _ := Supports(in); !ok {
		return false
	}
	if in.Protocol == "shadowsocks" {
		return admissionOnly
	}
	return true
}

// ServerUser builds the sing-box inbound user entry for id on in.
func ServerUser(in store.InboundConfig, id Identity) (map[string]any, error) {
	switch in.Protocol {
	case "vless":
		u := map[string]any{"name": id.Name, "uuid": id.UUID()}
		if flow := paramString(in.Params, "flow"); flow != "" {
			u["flow"] = flow
		}
		return u, nil
	case "vmess":
		alterID := 0
		if v, ok := in.Params["alter_id"]; ok {
			fmt.Sscan(fmt.Sprint(v), &alterID)
		}
		return map[string]any{"name": id.Name, "uuid": id.UUID(), "alterId": alterID}, nil
	case "trojan", "hysteria2", "anytls":
		return map[string]any{"name": id.Name, "password": id.Password()}, nil
	case "tuic":
		return map[string]any{"name": id.Name, "uuid": id.UUID(), "password": id.Password()}, nil
	case "http", "socks5", "auto":
		return map[string]any{"username": id.Username(), "password": id.Password()}, nil
	case "shadowsocks":
		key, err := id.SSKey(paramString(in.Params, "method"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"name": id.Name, "password": key}, nil
	default:
		return nil, fmt.Errorf("协议 %q 不支持多用户", in.Protocol)
	}
}

// ClientParams returns a copy of params with the shared credentials replaced
// by id's, for rendering a client outbound against in.
func ClientParams(in store.InboundConfig, params map[string]any, id Identity) (map[string]any, error) {
	out := make(map[string]any, len(params)+2)
	for k, v := range params {
		out[k] = v
	}
	switch in.Protocol {
	case "vless", "vmess":
		out["uuid"] = id.UUID()
	case "trojan", "hysteria2", "anytls":
		out["password"] = id.Password()
	case "tuic":
		out["uuid"] = id.UUID()
		out["password"] = id.Password()
	case "http", "socks5", "auto":
		out["username"] = id.Username()
		out["password"] = id.Password()
	case "shadowsocks":
		key, err := id.SSKey(paramString(params, "method"))
		if err != nil {
			return nil, err
		}
		out["password"] = paramString(params, "password") + ":" + key
	default:
		return nil, fmt.Errorf("协议 %q 不支持多用户", in.Protocol)
	}
	return out, nil
}

func paramString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
