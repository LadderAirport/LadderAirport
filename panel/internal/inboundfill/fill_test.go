package inboundfill_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ladderairport/panel/internal/inboundfill"
)

func TestFillShadowsocksPassword(t *testing.T) {
	p, err := inboundfill.Fill("shadowsocks", map[string]any{"port": 8388})
	if err != nil {
		t.Fatal(err)
	}
	if p["password"] == nil || p["password"] == "" {
		t.Fatal("expected auto password")
	}
	if p["method"] != "aes-256-gcm" {
		t.Fatalf("method = %v", p["method"])
	}
	// preserve provided password
	p2, err := inboundfill.Fill("shadowsocks", map[string]any{"password": "keep-me", "port": 1})
	if err != nil {
		t.Fatal(err)
	}
	if p2["password"] != "keep-me" {
		t.Fatalf("got %v", p2["password"])
	}
}

func TestFillShadowsocks2022Passwords(t *testing.T) {
	methods := map[string]int{
		"2022-blake3-aes-128-gcm":       16,
		"2022-blake3-aes-256-gcm":       32,
		"2022-blake3-chacha20-poly1305": 32,
	}
	for method, wantSize := range methods {
		t.Run(method, func(t *testing.T) {
			params, err := inboundfill.Fill("shadowsocks", map[string]any{
				"method": method, "port": 8388,
			})
			if err != nil {
				t.Fatal(err)
			}
			password := str(params, "password")
			decoded, err := base64.StdEncoding.DecodeString(password)
			if err != nil {
				t.Fatalf("generated password is not standard Base64: %q: %v", password, err)
			}
			if len(decoded) != wantSize {
				t.Fatalf("decoded key size = %d, want %d", len(decoded), wantSize)
			}
			if err := inboundfill.ValidateShadowsocks2022Password(method, password); err != nil {
				t.Fatalf("generated password rejected: %v", err)
			}
		})
	}
}

func TestRepairShadowsocks2022PasswordPreservesValidCredential(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString(make([]byte, 32))
	params := map[string]any{
		"method": "2022-blake3-aes-256-gcm", "password": valid,
	}
	changed, err := inboundfill.RepairShadowsocks2022Password(params)
	if err != nil {
		t.Fatal(err)
	}
	if changed || params["password"] != valid {
		t.Fatalf("valid credential was changed: %+v", params)
	}

	params["password"] = "legacy_url-safe-_"
	changed, err = inboundfill.RepairShadowsocks2022Password(params)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("invalid credential was not repaired")
	}
	if err := inboundfill.ValidateShadowsocks2022Password(
		"2022-blake3-aes-256-gcm", str(params, "password"),
	); err != nil {
		t.Fatalf("repaired credential rejected: %v", err)
	}
}

func TestFillVLESSReality(t *testing.T) {
	p, err := inboundfill.Fill("vless", map[string]any{"port": 443})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(str(p, "uuid")); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	if p["tls_mode"] != "reality" {
		t.Fatalf("tls_mode = %v", p["tls_mode"])
	}
	if str(p, "private_key") == "" || str(p, "short_id") == "" {
		t.Fatalf("missing reality secrets: %+v", p)
	}
	if str(p, "server_name") == "" || str(p, "handshake_server") == "" {
		t.Fatalf("missing reality endpoints: %+v", p)
	}
}

func TestFillTrojanTLSPEM(t *testing.T) {
	p, err := inboundfill.Fill("trojan", map[string]any{"port": 443})
	if err != nil {
		t.Fatal(err)
	}
	if str(p, "password") == "" {
		t.Fatal("password")
	}
	cert := str(p, "tls_cert_pem")
	key := str(p, "tls_key_pem")
	if !strings.Contains(cert, "BEGIN CERTIFICATE") {
		t.Fatalf("cert pem: %q", cert[:min(40, len(cert))])
	}
	if !strings.Contains(key, "BEGIN") {
		t.Fatalf("key pem missing")
	}
}

func TestFillHysteria2(t *testing.T) {
	p, err := inboundfill.Fill("hysteria2", map[string]any{"port": 8443})
	if err != nil {
		t.Fatal(err)
	}
	if str(p, "password") == "" || str(p, "tls_cert_pem") == "" {
		t.Fatalf("%+v", p)
	}
}

func TestFillTUIC(t *testing.T) {
	p, err := inboundfill.Fill("tuic", map[string]any{"port": 8443})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(str(p, "uuid")); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	if str(p, "password") == "" || str(p, "tls_cert_pem") == "" {
		t.Fatalf("%+v", p)
	}
	if p["congestion_control"] != "cubic" {
		t.Fatalf("congestion_control = %v", p["congestion_control"])
	}
}

func TestFillAnyTLS(t *testing.T) {
	p, err := inboundfill.Fill("anytls", map[string]any{"port": 443})
	if err != nil {
		t.Fatal(err)
	}
	if str(p, "password") == "" || str(p, "tls_cert_pem") == "" {
		t.Fatalf("%+v", p)
	}
}

func TestFillVMess(t *testing.T) {
	p, err := inboundfill.Fill("vmess", map[string]any{"port": 10086})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(str(p, "uuid")); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	if p["tls_mode"] != "none" {
		t.Fatalf("tls_mode = %v", p["tls_mode"])
	}
	// TLS mode should generate certs
	p2, err := inboundfill.Fill("vmess", map[string]any{"port": 10086, "tls_mode": "tls"})
	if err != nil {
		t.Fatal(err)
	}
	if str(p2, "tls_cert_pem") == "" {
		t.Fatalf("expected tls material: %+v", p2)
	}
}

func TestFillUserAuthProtocols(t *testing.T) {
	for _, proto := range []string{"http", "socks5", "auto"} {
		p, err := inboundfill.Fill(proto, map[string]any{"port": 1080})
		if err != nil {
			t.Fatalf("%s: %v", proto, err)
		}
		if p["auth_mode"] != "password" {
			t.Fatalf("%s auth_mode = %v", proto, p["auth_mode"])
		}
		if str(p, "username") == "" || str(p, "password") == "" {
			t.Fatalf("%s missing credentials: %+v", proto, p)
		}
		if p["listen"] != "0.0.0.0" {
			t.Fatalf("%s listen = %v", proto, p["listen"])
		}

		p2, err := inboundfill.Fill(proto, map[string]any{
			"port": 1080, "auth_mode": "none",
		})
		if err != nil {
			t.Fatalf("%s none: %v", proto, err)
		}
		if str(p2, "username") != "" || str(p2, "password") != "" {
			t.Fatalf("%s none should not generate credentials: %+v", proto, p2)
		}

		p3, err := inboundfill.Fill(proto, map[string]any{
			"port": 1080, "username": "keep", "password": "me",
		})
		if err != nil {
			t.Fatalf("%s preserve: %v", proto, err)
		}
		if p3["username"] != "keep" || p3["password"] != "me" {
			t.Fatalf("%s preserved = %+v", proto, p3)
		}
	}
	if _, err := inboundfill.Fill("auto", map[string]any{"port": 1, "auth_mode": "oops"}); err == nil {
		t.Fatal("expected invalid auth_mode error")
	}
}

func str(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}
