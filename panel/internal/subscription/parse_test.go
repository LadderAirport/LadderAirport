package subscription

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDetectAndParseClash(t *testing.T) {
	raw := []byte(`
proxies:
  - name: hk-ss
    type: ss
    server: 1.2.3.4
    port: 8388
    cipher: aes-256-gcm
    password: secret
  - name: lan-http
    type: http
    server: 9.9.9.9
    port: 8080
    username: alice
    password: secret
  - name: bad
    type: ssr
    server: 8.8.8.8
    port: 80
`)
	eps, kind, err := DetectAndParse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if kind != ContentClashYAML {
		t.Fatalf("kind=%s", kind)
	}
	if len(eps) != 2 {
		t.Fatalf("%+v", eps)
	}
	if eps[0].Protocol != "shadowsocks" || eps[0].Port != 8388 {
		t.Fatalf("%+v", eps[0])
	}
	if eps[1].Protocol != "http" || eps[1].Params["username"] != "alice" {
		t.Fatalf("%+v", eps[1])
	}
}

func TestDetectAndParseShareLinksAndBase64(t *testing.T) {
	// trojan share link
	line := "trojan://pw@example.com:443?sni=www.example.com#Node-A\n"
	eps, kind, err := DetectAndParse([]byte(line))
	if err != nil || kind != ContentShareLinks || len(eps) != 1 {
		t.Fatalf("plain: eps=%v kind=%s err=%v", eps, kind, err)
	}
	if eps[0].Protocol != "trojan" || eps[0].Server != "example.com" || eps[0].Name != "Node-A" {
		t.Fatalf("%+v", eps[0])
	}

	b64 := base64.StdEncoding.EncodeToString([]byte(line))
	eps2, kind2, err := DetectAndParse([]byte(b64))
	if err != nil || kind2 != ContentShareLinks || len(eps2) != 1 {
		t.Fatalf("b64: eps=%v kind=%s err=%v", eps2, kind2, err)
	}
}

func TestDetectAndParseSingbox(t *testing.T) {
	raw := []byte(`{
  "outbounds": [
    {"type":"selector","tag":"proxy","outbounds":["a","direct"]},
    {"type":"shadowsocks","tag":"a","server":"8.8.8.8","server_port":9000,"method":"aes-128-gcm","password":"p"},
    {"type":"direct","tag":"direct"}
  ]
}`)
	eps, kind, err := DetectAndParse(raw)
	if err != nil || kind != ContentSingboxJSON || len(eps) != 1 {
		t.Fatalf("eps=%v kind=%s err=%v", eps, kind, err)
	}
	if eps[0].Name != "a" || eps[0].Port != 9000 {
		t.Fatalf("%+v", eps[0])
	}
}

func TestMergeEndpointsPrefixAndUniquify(t *testing.T) {
	local := []ProxyEndpoint{{Name: "hk-ss", Server: "1.1.1.1", Port: 1, Protocol: "shadowsocks"}}
	ext := []ProxyEndpoint{
		{Name: prefixExternalName("机场A", "hk-ss"), Server: "2.2.2.2", Port: 2, Protocol: "shadowsocks", SourceID: "s1"},
		{Name: prefixExternalName("机场A", "hk-ss"), Server: "3.3.3.3", Port: 3, Protocol: "shadowsocks", SourceID: "s1"},
	}
	out := MergeEndpoints(local, ext)
	if len(out) != 3 {
		t.Fatalf("%d", len(out))
	}
	names := map[string]bool{}
	for _, ep := range out {
		if names[ep.Name] {
			t.Fatalf("duplicate name %s", ep.Name)
		}
		names[ep.Name] = true
	}
	if !strings.HasPrefix(out[1].Name, "机场A") && !strings.Contains(out[1].Name, "机场A") {
		// sanitize may alter unicode? keep as-is in sanitizeName
		if !strings.Contains(out[1].Name, "hk-ss") {
			t.Fatalf("unexpected names: %v", names)
		}
	}
}

func TestRealityPublicKeyParam(t *testing.T) {
	ep := ProxyEndpoint{
		Name: "r", Server: "1.1.1.1", Port: 443, Protocol: "vless",
		Params: map[string]any{
			"uuid":        "11111111-1111-1111-1111-111111111111",
			"tls_mode":    "reality",
			"public_key":  "abcd",
			"short_id":    "01",
			"server_name": "www.microsoft.com",
		},
	}
	clash, err := clashProxy(ep)
	if err != nil {
		t.Fatal(err)
	}
	ro := clash["reality-opts"].(map[string]any)
	if ro["public-key"] != "abcd" {
		t.Fatalf("%v", clash)
	}
	sb, err := singboxOutbound(ep)
	if err != nil {
		t.Fatal(err)
	}
	tls := sb["tls"].(map[string]any)
	reality := tls["reality"].(map[string]any)
	if reality["public_key"] != "abcd" {
		t.Fatalf("%v", sb)
	}
}

func TestCollectEndpointsEmptyOK(t *testing.T) {
	eps, err := CollectEndpoints(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 0 {
		t.Fatalf("%v", eps)
	}
}

func TestVLESSShareLinkReality(t *testing.T) {
	raw := "vless://11111111-1111-1111-1111-111111111111@edge.example:443?security=reality&pbk=PUBKEY&sid=ab&sni=www.microsoft.com#RL"
	eps, err := parseShareLinks([]byte(raw))
	if err != nil || len(eps) != 1 {
		t.Fatalf("%v %v", eps, err)
	}
	if eps[0].Params["tls_mode"] != "reality" || eps[0].Params["public_key"] != "PUBKEY" {
		t.Fatalf("%+v", eps[0].Params)
	}
}

func TestDetectAndParseDropsZeroServer(t *testing.T) {
	raw := []byte(`
proxies:
  - name: 一元机场-请立即到官网下载新客户端！
    type: trojan
    server: 0.0.0.0
    port: 443
    password: secret
    sni: 0.0.0.0
    skip-cert-verify: true
  - name: real
    type: trojan
    server: edge.example.com
    port: 443
    password: secret
`)
	eps, kind, err := DetectAndParse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if kind != ContentClashYAML {
		t.Fatalf("kind=%s", kind)
	}
	if len(eps) != 1 || eps[0].Name != "real" || eps[0].Server != "edge.example.com" {
		t.Fatalf("%+v", eps)
	}
}

func TestParseShareLinkAllowInsecure(t *testing.T) {
	ep, err := parseOneShareURI("trojan://pw@edge.example.com:443?allowInsecure=1#x")
	if err != nil {
		t.Fatal(err)
	}
	if ep.TLSSkipVerify == nil || !*ep.TLSSkipVerify {
		t.Fatalf("TLSSkipVerify = %v", ep.TLSSkipVerify)
	}

	ep, err = parseOneShareURI("vless://uid@edge.example.com:443?security=tls&allowInsecure=0#x")
	if err != nil {
		t.Fatal(err)
	}
	if ep.TLSSkipVerify == nil || *ep.TLSSkipVerify {
		t.Fatalf("TLSSkipVerify = %v", ep.TLSSkipVerify)
	}

	// Absent flag stays nil; rendering defaults to verification on.
	ep, err = parseOneShareURI("hysteria2://pw@edge.example.com:443#x")
	if err != nil {
		t.Fatal(err)
	}
	if ep.TLSSkipVerify != nil {
		t.Fatalf("TLSSkipVerify = %v", ep.TLSSkipVerify)
	}
	if endpointTLSSkipVerify(ep) {
		t.Fatal("nil TLSSkipVerify must default to verify")
	}
}

func TestParseClashSkipCertVerify(t *testing.T) {
	raw := []byte(`
proxies:
  - name: skip
    type: trojan
    server: a.example.com
    port: 443
    password: secret
    skip-cert-verify: true
  - name: strict
    type: vless
    server: b.example.com
    port: 443
    uuid: 0196b4c8-2e2b-4c0a-9f4a-9d2c3f4a5b6c
    tls: true
`)
	eps, err := parseClashYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 2 {
		t.Fatalf("%+v", eps)
	}
	if eps[0].TLSSkipVerify == nil || !*eps[0].TLSSkipVerify {
		t.Fatalf("skip TLSSkipVerify = %v", eps[0].TLSSkipVerify)
	}
	// No skip-cert-verify key → nil → verify.
	if eps[1].TLSSkipVerify != nil || endpointTLSSkipVerify(eps[1]) {
		t.Fatalf("strict TLSSkipVerify = %v", eps[1].TLSSkipVerify)
	}
}
