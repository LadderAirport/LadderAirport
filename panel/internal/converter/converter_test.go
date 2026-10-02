package converter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ladderairport/panel/internal/store"
)

func TestConvertShadowsocksGolden(t *testing.T) {
	in := store.InboundConfig{
		ID:       "ss-001",
		Name:     "ss-main",
		Protocol: "shadowsocks",
		Enabled:  true,
		Params: map[string]any{
			"listen":   "0.0.0.0",
			"port":     float64(8388),
			"method":   "aes-256-gcm",
			"password": "ss-secret",
			"network":  "tcp",
		},
	}
	assertGolden(t, []store.InboundConfig{in}, "ss.json")
}

func TestConvertTrojanGolden(t *testing.T) {
	in := store.InboundConfig{
		ID:       "tr-001",
		Name:     "trojan-main",
		Protocol: "trojan",
		Enabled:  true,
		Params: map[string]any{
			"listen":        "0.0.0.0",
			"port":          float64(443),
			"password":      "trojan-pass",
			"tls_cert_path": "/etc/certs/fullchain.pem",
			"tls_key_path":  "/etc/certs/privkey.pem",
		},
	}
	assertGolden(t, []store.InboundConfig{in}, "trojan.json")
}

func TestConvertVLESSRealityGolden(t *testing.T) {
	in := store.InboundConfig{
		ID:       "vl-001",
		Name:     "vless-main",
		Protocol: "vless",
		Enabled:  true,
		Params: map[string]any{
			"listen":           "::",
			"port":             float64(443),
			"uuid":             "bf000d23-0752-40b4-affe-68f7707a9661",
			"flow":             "xtls-rprx-vision",
			"tls_mode":         "reality",
			"private_key":      "UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc",
			"short_id":         "0123456789abcdef",
			"server_name":      "www.example.com",
			"handshake_server": "www.example.com",
		},
	}
	assertGolden(t, []store.InboundConfig{in}, "vless.json")
}

func TestConvertHysteria2Golden(t *testing.T) {
	in := store.InboundConfig{
		ID:       "hy-001",
		Name:     "hy2-main",
		Protocol: "hysteria2",
		Enabled:  true,
		Params: map[string]any{
			"listen":        "0.0.0.0",
			"port":          float64(8443),
			"password":      "hy2-pass",
			"tls_cert_path": "/etc/certs/fullchain.pem",
			"tls_key_path":  "/etc/certs/privkey.pem",
			"up_mbps":       float64(100),
			"down_mbps":     float64(200),
		},
	}
	assertGolden(t, []store.InboundConfig{in}, "hysteria2.json")
}

func TestConvertTUICGolden(t *testing.T) {
	in := store.InboundConfig{
		ID:       "tuic-001",
		Name:     "tuic-main",
		Protocol: "tuic",
		Enabled:  true,
		Params: map[string]any{
			"listen":             "0.0.0.0",
			"port":               float64(8443),
			"uuid":               "2dd61d93-75d8-4da4-ac0e-6aece7eac365",
			"password":           "tuic-pass",
			"congestion_control": "bbr",
			"server_name":        "tuic.example.com",
			"tls_cert_path":      "/etc/certs/fullchain.pem",
			"tls_key_path":       "/etc/certs/privkey.pem",
		},
	}
	assertGolden(t, []store.InboundConfig{in}, "tuic.json")
}

func TestConvertAnyTLSGolden(t *testing.T) {
	in := store.InboundConfig{
		ID:       "any-001",
		Name:     "anytls-main",
		Protocol: "anytls",
		Enabled:  true,
		Params: map[string]any{
			"listen":        "0.0.0.0",
			"port":          float64(443),
			"password":      "anytls-pass",
			"server_name":   "anytls.example.com",
			"tls_cert_path": "/etc/certs/fullchain.pem",
			"tls_key_path":  "/etc/certs/privkey.pem",
		},
	}
	assertGolden(t, []store.InboundConfig{in}, "anytls.json")
}

func TestConvertVMessGolden(t *testing.T) {
	in := store.InboundConfig{
		ID:       "vm-001",
		Name:     "vmess-main",
		Protocol: "vmess",
		Enabled:  true,
		Params: map[string]any{
			"listen":        "0.0.0.0",
			"port":          float64(10086),
			"uuid":          "bf000d23-0752-40b4-affe-68f7707a9661",
			"alter_id":      float64(0),
			"tls_mode":      "tls",
			"server_name":   "vmess.example.com",
			"tls_cert_path": "/etc/certs/fullchain.pem",
			"tls_key_path":  "/etc/certs/privkey.pem",
		},
	}
	assertGolden(t, []store.InboundConfig{in}, "vmess.json")
}

func TestConvertHTTPGolden(t *testing.T) {
	in := store.InboundConfig{
		ID: "http-001", Name: "http-main", Protocol: "http", Enabled: true,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": float64(8080),
			"auth_mode": "password", "username": "alice", "password": "http-pass",
		},
	}
	assertGolden(t, []store.InboundConfig{in}, "http.json")
}

func TestConvertSOCKS5Golden(t *testing.T) {
	in := store.InboundConfig{
		ID: "sk-001", Name: "socks-main", Protocol: "socks5", Enabled: true,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": float64(1080),
			"auth_mode": "password", "username": "bob", "password": "socks-pass",
		},
	}
	assertGolden(t, []store.InboundConfig{in}, "socks5.json")
}

func TestConvertAutoGolden(t *testing.T) {
	in := store.InboundConfig{
		ID: "auto-001", Name: "auto-main", Protocol: "auto", Enabled: true,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": float64(7890),
			"auth_mode": "password", "username": "carol", "password": "auto-pass",
		},
	}
	assertGolden(t, []store.InboundConfig{in}, "auto.json")
}

func TestConvertAutoNoAuth(t *testing.T) {
	in := store.InboundConfig{
		ID: "auto-002", Name: "auto-open", Protocol: "auto", Enabled: true,
		Params: map[string]any{
			"listen": "127.0.0.1", "port": float64(7891), "auth_mode": "none",
		},
	}
	raw, err := Convert([]store.InboundConfig{in}, ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	inb := cfg["inbounds"].([]any)[0].(map[string]any)
	if inb["type"] != "mixed" {
		t.Fatalf("type = %v", inb["type"])
	}
	if _, ok := inb["users"]; ok {
		t.Fatalf("none auth should omit users: %#v", inb)
	}
}

func TestConvertInvalidAuthMode(t *testing.T) {
	in := store.InboundConfig{
		ID: "http-bad", Name: "http-bad", Protocol: "http", Enabled: true,
		Params: map[string]any{"port": float64(8080), "auth_mode": "oops"},
	}
	if _, err := Convert([]store.InboundConfig{in}, ConvertOptions{}); err == nil {
		t.Fatal("expected invalid auth_mode error")
	}
}

func TestConvertEmptyError(t *testing.T) {
	if _, err := Convert(nil, ConvertOptions{}); err == nil {
		t.Fatal("expected error for empty list")
	}
	if _, err := Convert([]store.InboundConfig{}, ConvertOptions{}); err == nil {
		t.Fatal("expected error for empty list")
	}
	// only disabled
	if _, err := Convert([]store.InboundConfig{{
		Name: "x", Protocol: "shadowsocks", Enabled: false,
		Params: map[string]any{"port": 1, "method": "aes-256-gcm", "password": "p"},
	}}, ConvertOptions{}); err == nil {
		t.Fatal("expected error when all disabled")
	}
}

func TestConvertAllowEmptyAndChainRoute(t *testing.T) {
	empty, err := Convert(nil, ConvertOptions{AllowEmpty: true})
	if err != nil {
		t.Fatalf("Convert empty: %v", err)
	}
	var emptyConfig struct {
		Inbounds []any `json:"inbounds"`
	}
	if err := json.Unmarshal(empty, &emptyConfig); err != nil {
		t.Fatal(err)
	}
	if len(emptyConfig.Inbounds) != 0 {
		t.Fatalf("empty inbounds = %d, want 0", len(emptyConfig.Inbounds))
	}

	in := store.InboundConfig{
		ID: "chain-entry", Name: "entry", Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": float64(8388),
			"method": "aes-256-gcm", "password": "secret", "network": "tcp",
		},
	}
	raw, err := Convert([]store.InboundConfig{in}, ConvertOptions{
		ChainRoutes: []ChainRoute{{
			InboundID: in.ID,
			Outbound: map[string]any{
				"type": "shadowsocks", "tag": "chain-next",
				"server": "next.example", "server_port": 8389,
				"method": "aes-256-gcm", "password": "next-secret",
			},
		}},
	})
	if err != nil {
		t.Fatalf("Convert chain route: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	outbounds := cfg["outbounds"].([]any)
	if len(outbounds) != 2 || outbounds[1].(map[string]any)["tag"] != "chain-next" {
		t.Fatalf("outbounds = %#v", outbounds)
	}
	route := cfg["route"].(map[string]any)
	rules := route["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["outbound"] != "chain-next" {
		t.Fatalf("route rules = %#v", rules)
	}
}

func TestConvertPortConflict(t *testing.T) {
	a := store.InboundConfig{
		ID: "a", Name: "a", Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": float64(8388),
			"method": "aes-256-gcm", "password": "p1",
		},
	}
	b := store.InboundConfig{
		ID: "b", Name: "b", Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": float64(8388),
			"method": "aes-256-gcm", "password": "p2",
		},
	}
	_, err := Convert([]store.InboundConfig{a, b}, ConvertOptions{})
	if err == nil {
		t.Fatal("expected port conflict error")
	}
}

func TestConvertInvalidUUID(t *testing.T) {
	in := store.InboundConfig{
		ID: "vl-bad", Name: "vless-bad", Protocol: "vless", Enabled: true,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": float64(443),
			"uuid": "not-a-uuid", "tls_mode": "none",
		},
	}
	_, err := Convert([]store.InboundConfig{in}, ConvertOptions{})
	if err == nil {
		t.Fatal("expected invalid UUID error")
	}
}

func TestConvertSkipsDisabled(t *testing.T) {
	enabled := store.InboundConfig{
		ID: "ss-001", Name: "ss-main", Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": float64(8388),
			"method": "aes-256-gcm", "password": "ss-secret", "network": "tcp",
		},
	}
	disabled := store.InboundConfig{
		ID: "ss-002", Name: "ss-off", Protocol: "shadowsocks", Enabled: false,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": float64(8389),
			"method": "aes-256-gcm", "password": "other",
		},
	}
	assertGolden(t, []store.InboundConfig{disabled, enabled}, "ss.json")
}

func TestConvertMissingRequired(t *testing.T) {
	in := store.InboundConfig{
		ID: "ss", Name: "ss", Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{"port": float64(8388), "method": "aes-256-gcm"},
	}
	if _, err := Convert([]store.InboundConfig{in}, ConvertOptions{}); err == nil {
		t.Fatal("expected missing password error")
	}
}

func TestConvertRejectsInvalidShadowsocks2022PSK(t *testing.T) {
	in := store.InboundConfig{
		ID: "ss-2022", Name: "ss-2022", Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": float64(8388),
			"method": "2022-blake3-aes-256-gcm", "password": "legacy_url-safe-_",
		},
	}
	_, err := Convert([]store.InboundConfig{in}, ConvertOptions{})
	if err == nil || !strings.Contains(err.Error(), "标准 Base64") {
		t.Fatalf("Convert error = %v, want standard Base64 validation", err)
	}
}

func assertGolden(t *testing.T, inbounds []store.InboundConfig, goldenName string) {
	t.Helper()
	got, err := Convert(inbounds, ConvertOptions{})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	path := filepath.Join("testdata", goldenName)
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	var gotObj, wantObj any
	if err := json.Unmarshal(got, &gotObj); err != nil {
		t.Fatalf("unmarshal got: %v\n%s", err, got)
	}
	if err := json.Unmarshal(wantBytes, &wantObj); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if !reflect.DeepEqual(gotObj, wantObj) {
		gotPretty, _ := json.MarshalIndent(gotObj, "", "  ")
		wantPretty, _ := json.MarshalIndent(wantObj, "", "  ")
		t.Fatalf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", goldenName, gotPretty, wantPretty)
	}
}

func TestConvertBindInterface(t *testing.T) {
	in := store.InboundConfig{
		ID: "ss-001", Name: "ss-main", Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": float64(8388),
			"method": "aes-256-gcm", "password": "ss-secret", "network": "tcp",
		},
	}
	got, err := Convert([]store.InboundConfig{in}, ConvertOptions{BindInterface: "eth1"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	outs, _ := cfg["outbounds"].([]any)
	if len(outs) != 1 {
		t.Fatalf("outbounds len = %d", len(outs))
	}
	ob, _ := outs[0].(map[string]any)
	if ob["bind_interface"] != "eth1" {
		t.Fatalf("bind_interface = %v", ob["bind_interface"])
	}

	// whitespace-only must omit the key
	got2, err := Convert([]store.InboundConfig{in}, ConvertOptions{BindInterface: "  "})
	if err != nil {
		t.Fatalf("Convert blank: %v", err)
	}
	var cfg2 map[string]any
	if err := json.Unmarshal(got2, &cfg2); err != nil {
		t.Fatalf("unmarshal blank: %v", err)
	}
	outs2, _ := cfg2["outbounds"].([]any)
	ob2, _ := outs2[0].(map[string]any)
	if _, ok := ob2["bind_interface"]; ok {
		t.Fatalf("expected no bind_interface for blank, got %v", ob2["bind_interface"])
	}
}
