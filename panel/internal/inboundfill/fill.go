// Package inboundfill auto-generates secrets for inbound params so operators
// only need to pick protocol, port, and a few non-secret options.
package inboundfill

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	"crypto/ecdh"

	"github.com/google/uuid"
)

// Fill mutates params (or creates a map) by filling empty secret fields for protocol.
// Existing non-empty values are preserved. Returns the params map (never nil).
func Fill(protocol string, params map[string]any) (map[string]any, error) {
	if params == nil {
		params = map[string]any{}
	}
	// Normalize empty strings as missing.
	for k, v := range params {
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			delete(params, k)
		}
	}

	switch protocol {
	case "shadowsocks":
		return fillShadowsocks(params)
	case "trojan":
		return fillTrojan(params)
	case "vless":
		return fillVLESS(params)
	case "hysteria2":
		return fillHysteria2(params)
	case "tuic":
		return fillTUIC(params)
	case "anytls":
		return fillAnyTLS(params)
	case "vmess":
		return fillVMess(params)
	case "http", "socks5", "auto":
		return fillUserAuth(params)
	default:
		return params, nil
	}
}

func fillShadowsocks(params map[string]any) (map[string]any, error) {
	if empty(params, "method") {
		params["method"] = "aes-256-gcm"
	}
	if _, ok := Shadowsocks2022KeySize(str(params, "method")); ok {
		if _, err := RepairShadowsocks2022Password(params); err != nil {
			return nil, err
		}
	} else if err := ensurePassword(params); err != nil {
		return nil, err
	}
	if empty(params, "listen") {
		params["listen"] = "0.0.0.0"
	}
	return params, nil
}

// Shadowsocks2022KeySize returns the PSK size required by a Shadowsocks 2022
// method. These methods require a standard (padded) Base64-encoded key.
func Shadowsocks2022KeySize(method string) (int, bool) {
	switch strings.TrimSpace(method) {
	case "2022-blake3-aes-128-gcm":
		return 16, true
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		return 32, true
	default:
		return 0, false
	}
}

// ValidateShadowsocks2022Password mirrors sing-shadowsocks validation before a
// config reaches the Agent. Longer decoded keys are accepted because the
// upstream implementation derives the method-sized key from them.
func ValidateShadowsocks2022Password(method, password string) error {
	keySize, ok := Shadowsocks2022KeySize(method)
	if !ok {
		return nil
	}
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("%s 必须使用 Base64 PSK", method)
	}
	decoded, err := base64.StdEncoding.DecodeString(password)
	if err != nil {
		return fmt.Errorf("%s 密码必须是标准 Base64：%w", method, err)
	}
	if len(decoded) < keySize {
		return fmt.Errorf("%s 密码解码后为 %d 字节，至少需要 %d 字节", method, len(decoded), keySize)
	}
	return nil
}

// RepairShadowsocks2022Password replaces only passwords that sing-shadowsocks
// would reject. It preserves valid existing credentials to avoid disrupting
// clients during startup migration.
func RepairShadowsocks2022Password(params map[string]any) (bool, error) {
	method := str(params, "method")
	keySize, ok := Shadowsocks2022KeySize(method)
	if !ok {
		return false, nil
	}
	password := str(params, "password")
	if ValidateShadowsocks2022Password(method, password) == nil {
		return false, nil
	}
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return false, fmt.Errorf("生成 %s PSK 失败：%w", method, err)
	}
	params["password"] = base64.StdEncoding.EncodeToString(key)
	return true, nil
}

func fillTrojan(params map[string]any) (map[string]any, error) {
	if err := ensurePassword(params); err != nil {
		return nil, err
	}
	if empty(params, "listen") {
		params["listen"] = "0.0.0.0"
	}
	if err := ensureTLSMaterial(params); err != nil {
		return nil, err
	}
	return params, nil
}

func fillHysteria2(params map[string]any) (map[string]any, error) {
	if err := ensurePassword(params); err != nil {
		return nil, err
	}
	if empty(params, "listen") {
		params["listen"] = "0.0.0.0"
	}
	if err := ensureTLSMaterial(params); err != nil {
		return nil, err
	}
	return params, nil
}

func fillVLESS(params map[string]any) (map[string]any, error) {
	if empty(params, "listen") {
		params["listen"] = "0.0.0.0"
	}
	if empty(params, "uuid") {
		params["uuid"] = uuid.NewString()
	}
	if empty(params, "tls_mode") {
		params["tls_mode"] = "reality"
	}
	mode := str(params, "tls_mode")
	switch mode {
	case "tls":
		if err := ensureTLSMaterial(params); err != nil {
			return nil, err
		}
	case "reality":
		if err := ensureReality(params); err != nil {
			return nil, err
		}
	case "none":
		// no secrets
	default:
		return nil, fmt.Errorf("tls_mode %q 无效", mode)
	}
	return params, nil
}

func fillTUIC(params map[string]any) (map[string]any, error) {
	if empty(params, "listen") {
		params["listen"] = "0.0.0.0"
	}
	if empty(params, "uuid") {
		params["uuid"] = uuid.NewString()
	}
	if err := ensurePassword(params); err != nil {
		return nil, err
	}
	if empty(params, "congestion_control") {
		params["congestion_control"] = "cubic"
	}
	if err := ensureTLSMaterial(params); err != nil {
		return nil, err
	}
	return params, nil
}

func fillAnyTLS(params map[string]any) (map[string]any, error) {
	if empty(params, "listen") {
		params["listen"] = "0.0.0.0"
	}
	if err := ensurePassword(params); err != nil {
		return nil, err
	}
	if err := ensureTLSMaterial(params); err != nil {
		return nil, err
	}
	return params, nil
}

func fillVMess(params map[string]any) (map[string]any, error) {
	if empty(params, "listen") {
		params["listen"] = "0.0.0.0"
	}
	if empty(params, "uuid") {
		params["uuid"] = uuid.NewString()
	}
	if empty(params, "alter_id") {
		params["alter_id"] = 0
	}
	if empty(params, "tls_mode") {
		params["tls_mode"] = "none"
	}
	mode := str(params, "tls_mode")
	switch mode {
	case "tls":
		if err := ensureTLSMaterial(params); err != nil {
			return nil, err
		}
	case "none":
		// no TLS material
	default:
		return nil, fmt.Errorf("tls_mode %q 无效", mode)
	}
	return params, nil
}

func fillUserAuth(params map[string]any) (map[string]any, error) {
	if empty(params, "listen") {
		params["listen"] = "0.0.0.0"
	}
	if empty(params, "auth_mode") {
		params["auth_mode"] = "password"
	}
	switch str(params, "auth_mode") {
	case "none":
		return params, nil
	case "password":
		if err := ensureUsername(params); err != nil {
			return nil, err
		}
		if err := ensurePassword(params); err != nil {
			return nil, err
		}
		return params, nil
	default:
		return nil, fmt.Errorf("auth_mode %q 无效", str(params, "auth_mode"))
	}
}

func ensureUsername(params map[string]any) error {
	if !empty(params, "username") {
		return nil
	}
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("生成用户名失败：%w", err)
	}
	params["username"] = "user-" + hex.EncodeToString(b)
	return nil
}

func ensurePassword(params map[string]any) error {
	if !empty(params, "password") {
		return nil
	}
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("生成密码失败：%w", err)
	}
	params["password"] = base64.RawURLEncoding.EncodeToString(b)
	return nil
}

// ensureTLSMaterial fills tls_cert_pem / tls_key_pem when neither PEM nor path is set.
func ensureTLSMaterial(params map[string]any) error {
	hasPEM := !empty(params, "tls_cert_pem") && !empty(params, "tls_key_pem")
	hasPath := !empty(params, "tls_cert_path") && !empty(params, "tls_key_path")
	if hasPEM || hasPath {
		return nil
	}
	cert, key, err := generateSelfSigned("ladder-airport")
	if err != nil {
		return err
	}
	params["tls_cert_pem"] = cert
	params["tls_key_pem"] = key
	return nil
}

func ensureReality(params map[string]any) error {
	if empty(params, "private_key") {
		priv, err := generateRealityPrivateKey()
		if err != nil {
			return err
		}
		params["private_key"] = priv
	}
	if empty(params, "short_id") {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return fmt.Errorf("生成 short_id 失败：%w", err)
		}
		params["short_id"] = hex.EncodeToString(b)
	}
	if empty(params, "server_name") {
		params["server_name"] = "www.microsoft.com"
	}
	if empty(params, "handshake_server") {
		params["handshake_server"] = str(params, "server_name")
		if params["handshake_server"] == "" {
			params["handshake_server"] = "www.microsoft.com"
		}
	}
	if empty(params, "handshake_server_port") {
		params["handshake_server_port"] = 443
	}
	return nil
}

func generateRealityPrivateKey() (string, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("生成 Reality 密钥对失败：%w", err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.Bytes()), nil
}

func generateSelfSigned(cn string) (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("生成 TLS 私钥失败：%w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"LadderAirport"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{cn, "localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", fmt.Errorf("生成 TLS 证书失败：%w", err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}))
	return certPEM, keyPEM, nil
}

func empty(params map[string]any, key string) bool {
	v, ok := params[key]
	if !ok || v == nil {
		return true
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) == ""
	default:
		return fmt.Sprint(t) == ""
	}
}

func str(params map[string]any, key string) string {
	v, ok := params[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return strings.TrimSpace(s)
}
