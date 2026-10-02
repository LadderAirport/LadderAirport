package subscription

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ladderairport/panel/internal/store"
)

func sampleEndpoints() []ProxyEndpoint {
	return []ProxyEndpoint{
		{
			Name:     "hk-ss",
			Server:   "1.2.3.4",
			Port:     8388,
			Protocol: "shadowsocks",
			Params: map[string]any{
				"method":   "aes-256-gcm",
				"password": "secret",
			},
			Node:    store.Node{Name: "hk", Address: "1.2.3.4"},
			Inbound: store.InboundConfig{Name: "ss", Protocol: "shadowsocks"},
		},
	}
}

func TestRenderClashHasCNRules(t *testing.T) {
	b, err := RenderClash(sampleEndpoints())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"type: ss",
		"RULE-SET,cn,",
		"RULE-SET,cnip,",
		"RULE-SET,privateip,",
		"url-test",
		"rule-providers:",
		"respect-rules:",
		"proxy-server-nameserver:",
		"本地",
		"节点选择",
		"全球直连",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "proxy-providers:") {
		t.Fatalf("must not emit proxy-providers:\n%s", s)
	}
	if strings.Contains(s, "GEOIP,CN,DIRECT") || strings.Contains(s, "GEOSITE,cn,DIRECT") {
		t.Fatalf("legacy GEOIP/GEOSITE rules should be gone:\n%s", s)
	}
}

func TestRenderSingboxHasCNRuleSets(t *testing.T) {
	b, err := RenderSingbox(sampleEndpoints())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"type": "shadowsocks"`, `geoip-cn`, `geosite-cn`, `"final": "proxy"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}

func TestCollectEndpoints(t *testing.T) {
	nodes := []store.Node{{ID: "n1", Name: "hk", Address: "10.0.0.1"}}
	ins := map[string][]store.InboundConfig{
		"n1": {{
			ID: "i1", Name: "ss", Protocol: "shadowsocks", Enabled: true,
			Params: map[string]any{"port": float64(9000), "method": "aes-128-gcm", "password": "p"},
		}},
	}
	eps, err := CollectEndpoints(nodes, ins, nil)
	if err != nil || len(eps) != 1 {
		t.Fatalf("eps=%v err=%v", eps, err)
	}
	if eps[0].Port != 9000 || eps[0].Server != "10.0.0.1" {
		t.Fatalf("%+v", eps[0])
	}
}

func TestCollectEndpointsUsesFRPSAddressAndPort(t *testing.T) {
	nodes := []store.Node{{ID: "n1", Name: "edge", Address: "10.0.0.1"}}
	attachments := map[string][]store.NodeInboundAttachment{"n1": {{
		InboundConfig: store.InboundConfig{ID: "i1", Name: "ss", Protocol: "shadowsocks", Enabled: true,
			Params: map[string]any{"port": 8388, "method": "aes-256-gcm", "password": "secret"}},
		FRPEnabled: true,
		FRPCConfig: `{"server_addr":"frps.example.com","server_port":7000,"remote_port":20001,"token":"secret"}`,
	}}}
	endpoints, err := CollectEndpointsFromAttachments(nodes, attachments, nil)
	if err != nil || len(endpoints) != 1 {
		t.Fatalf("endpoints = %+v, err = %v", endpoints, err)
	}
	if endpoints[0].Server != "frps.example.com" || endpoints[0].Port != 20001 {
		t.Fatalf("endpoint = %+v", endpoints[0])
	}
	clash, err := clashProxy(endpoints[0])
	if err != nil || clash["udp"] != false {
		t.Fatalf("Clash Shadowsocks UDP = %v, err = %v", clash["udp"], err)
	}
	singbox, err := SingboxOutbound(endpoints[0])
	if err != nil || singbox["network"] != "tcp" {
		t.Fatalf("sing-box Shadowsocks network = %v, err = %v", singbox["network"], err)
	}
}

func TestCollectEndpointsPrefersPublicAddress(t *testing.T) {
	nodes := []store.Node{{
		ID: "n1", Name: "hk",
		Address: "10.0.0.1", PublicAddress: "edge.example.com",
	}}
	ins := map[string][]store.InboundConfig{
		"n1": {{
			ID: "i1", Name: "ss", Protocol: "shadowsocks", Enabled: true,
			Params: map[string]any{"port": float64(9000), "method": "aes-128-gcm", "password": "p"},
		}},
	}
	eps, err := CollectEndpoints(nodes, ins, nil)
	if err != nil || len(eps) != 1 {
		t.Fatalf("eps=%v err=%v", eps, err)
	}
	if eps[0].Server != "edge.example.com" {
		t.Fatalf("server = %q, want public_address", eps[0].Server)
	}
}

func TestCollectEndpointsAppliesPortMappings(t *testing.T) {
	nodes := []store.Node{{
		ID: "n1", Name: "nat",
		Address: "10.0.0.8", PublicAddress: "203.0.113.9",
		PortMappings: []store.PortMapping{
			{ListenPort: 8443, PublicPort: 443},
			{ListenPort: 9000, PublicPort: 19000},
		},
	}}
	ins := map[string][]store.InboundConfig{
		"n1": {
			{
				ID: "i1", Name: "hy2", Protocol: "hysteria2", Enabled: true,
				Params: map[string]any{"port": float64(8443), "password": "p"},
			},
			{
				ID: "i2", Name: "ss", Protocol: "shadowsocks", Enabled: true,
				Params: map[string]any{"port": float64(9000), "method": "aes-128-gcm", "password": "p"},
			},
			{
				ID: "i3", Name: "ss2", Protocol: "shadowsocks", Enabled: true,
				// No mapping → keep listen port.
				Params: map[string]any{"port": float64(10086), "method": "aes-128-gcm", "password": "p"},
			},
		},
	}
	eps, err := CollectEndpoints(nodes, ins, nil)
	if err != nil || len(eps) != 3 {
		t.Fatalf("eps=%v err=%v", eps, err)
	}
	want := map[string]int{"hy2": 443, "ss": 19000, "ss2": 10086}
	for _, ep := range eps {
		// Name is sanitized "nat-<inbound>"
		key := ep.Inbound.Name
		if ep.Port != want[key] {
			t.Fatalf("%s port = %d, want %d (server=%s)", key, ep.Port, want[key], ep.Server)
		}
		if ep.Server != "203.0.113.9" {
			t.Fatalf("%s server = %q", key, ep.Server)
		}
	}
}

func TestClientServerHost(t *testing.T) {
	if got := clientServerHost(store.Node{Address: "10.0.0.1"}); got != "10.0.0.1" {
		t.Fatalf("fallback = %q", got)
	}
	if got := clientServerHost(store.Node{Address: "10.0.0.1", PublicAddress: " pub.example "}); got != "pub.example" {
		t.Fatalf("prefer public = %q", got)
	}
}

func TestRenderNewProtocols(t *testing.T) {
	eps := []ProxyEndpoint{
		{
			Name: "n-tuic", Server: "1.1.1.1", Port: 8443, Protocol: "tuic",
			Params: map[string]any{
				"uuid": "2dd61d93-75d8-4da4-ac0e-6aece7eac365", "password": "p",
				"congestion_control": "bbr", "server_name": "t.example.com",
			},
		},
		{
			Name: "n-anytls", Server: "1.1.1.1", Port: 443, Protocol: "anytls",
			Params: map[string]any{"password": "p", "server_name": "a.example.com"},
		},
		{
			Name: "n-vmess", Server: "1.1.1.1", Port: 10086, Protocol: "vmess",
			Params: map[string]any{
				"uuid":     "bf000d23-0752-40b4-affe-68f7707a9661",
				"alter_id": float64(0), "tls_mode": "tls", "server_name": "v.example.com",
			},
		},
		{
			Name: "n-http", Server: "1.1.1.1", Port: 8080, Protocol: "http",
			Params: map[string]any{"username": "alice", "password": "p"},
		},
		{
			Name: "n-socks", Server: "1.1.1.1", Port: 1080, Protocol: "socks5",
			Params: map[string]any{"username": "bob", "password": "p"},
		},
		{
			Name: "n-auto", Server: "1.1.1.1", Port: 7890, Protocol: "auto",
			Params: map[string]any{"username": "carol", "password": "p"},
		},
	}
	clash, err := RenderClash(eps)
	if err != nil {
		t.Fatal(err)
	}
	cs := string(clash)
	for _, want := range []string{
		"type: tuic", "type: anytls", "type: vmess", "congestion-controller: bbr",
		"type: http", "type: socks5", "username: alice", "username: bob", "username: carol",
	} {
		if !strings.Contains(cs, want) {
			t.Fatalf("clash missing %q in:\n%s", want, cs)
		}
	}
	sb, err := RenderSingbox(eps)
	if err != nil {
		t.Fatal(err)
	}
	ss := string(sb)
	for _, want := range []string{
		`"type": "tuic"`, `"type": "anytls"`, `"type": "vmess"`, `"congestion_control": "bbr"`,
		`"type": "http"`, `"type": "socks"`, `"username": "alice"`, `"username": "carol"`,
	} {
		if !strings.Contains(ss, want) {
			t.Fatalf("singbox missing %q in:\n%s", want, ss)
		}
	}
}

func TestRenderHTTPSocksShareAndFRP(t *testing.T) {
	httpEP := ProxyEndpoint{
		Name: "lan-http", Server: "10.0.0.1", Port: 8080, Protocol: "http",
		Params: map[string]any{"username": "alice", "password": "secret"},
	}
	autoFRP := ProxyEndpoint{
		Name: "lan-auto", Server: "frps.example.com", Port: 20001, Protocol: "auto",
		Params: map[string]any{"username": "carol", "password": "secret", "frp_enabled": true},
	}
	clash, err := clashProxy(autoFRP)
	if err != nil || clash["type"] != "socks5" || clash["udp"] != false {
		t.Fatalf("auto FRP clash = %#v err=%v", clash, err)
	}
	sb, err := SingboxOutbound(autoFRP)
	if err != nil || sb["type"] != "socks" || sb["network"] != "tcp" {
		t.Fatalf("auto FRP singbox = %#v err=%v", sb, err)
	}
	uri, err := renderOneShareURI(httpEP)
	if err != nil || !strings.HasPrefix(uri, "http://") || !strings.Contains(uri, "alice") {
		t.Fatalf("http uri = %q err=%v", uri, err)
	}
	uri2, err := renderOneShareURI(autoFRP)
	if err != nil || !strings.HasPrefix(uri2, "socks5://") {
		t.Fatalf("auto uri = %q err=%v", uri2, err)
	}
}

func TestCollectEndpointsPerInboundNAT(t *testing.T) {
	nodes := []store.Node{{
		ID: "n1", Name: "nat",
		Address: "10.0.0.8", PublicAddress: "node-default.example.com",
		PortMappings: []store.PortMapping{{ListenPort: 9000, PublicPort: 19000}},
	}}
	atts := map[string][]store.NodeInboundAttachment{
		"n1": {
			{
				InboundConfig: store.InboundConfig{
					ID: "i1", Name: "hy2", Protocol: "hysteria2", Enabled: true,
					Params: map[string]any{"port": float64(8443), "password": "p"},
				},
				PublicAddress: "edge.example.com",
				PublicPort:    443,
			},
			{
				InboundConfig: store.InboundConfig{
					ID: "i2", Name: "ss", Protocol: "shadowsocks", Enabled: true,
					Params: map[string]any{"port": float64(9000), "method": "aes-128-gcm", "password": "p"},
				},
				// no attachment override → node port_mappings + node public_address
			},
			{
				InboundConfig: store.InboundConfig{
					ID: "i3", Name: "ss2", Protocol: "shadowsocks", Enabled: true,
					Params: map[string]any{"port": float64(10086), "method": "aes-128-gcm", "password": "p"},
				},
				PublicPort: 10086, // same as listen, still fine
			},
		},
	}
	eps, err := CollectEndpointsFromAttachments(nodes, atts, nil)
	if err != nil || len(eps) != 3 {
		t.Fatalf("eps=%v err=%v", eps, err)
	}
	wantHost := map[string]string{"hy2": "edge.example.com", "ss": "node-default.example.com", "ss2": "node-default.example.com"}
	wantPort := map[string]int{"hy2": 443, "ss": 19000, "ss2": 10086}
	for _, ep := range eps {
		key := ep.Inbound.Name
		if ep.Server != wantHost[key] {
			t.Fatalf("%s server=%q want %q", key, ep.Server, wantHost[key])
		}
		if ep.Port != wantPort[key] {
			t.Fatalf("%s port=%d want %d", key, ep.Port, wantPort[key])
		}
	}
}

func TestRenderClashSourceGroups(t *testing.T) {
	eps := []ProxyEndpoint{
		{Name: "hk-ss", Server: "1.1.1.1", Port: 443, Protocol: "shadowsocks",
			Params: map[string]any{"method": "aes-256-gcm", "password": "p"}},
		{Name: "机场A-a", Server: "2.2.2.2", Port: 443, Protocol: "trojan",
			Params: map[string]any{"password": "p"}, SourceID: "s1", SourceName: "机场A"},
		{Name: "机场A-b", Server: "3.3.3.3", Port: 443, Protocol: "trojan",
			Params: map[string]any{"password": "p"}, SourceID: "s1", SourceName: "机场A"},
		{Name: "机场B-x", Server: "4.4.4.4", Port: 443, Protocol: "trojan",
			Params: map[string]any{"password": "p"}, SourceID: "s2", SourceName: "机场B"},
	}
	b, err := RenderClash(eps)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"机场A", "机场B", "本地", "url-test", "hk-ss", "机场A-a"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "proxy-providers:") {
		t.Fatal("proxy-providers must not appear")
	}
}

func TestRenderV2ray(t *testing.T) {
	eps := sampleEndpoints()
	b, err := RenderV2ray(eps)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if s == "" {
		t.Fatal("empty v2ray output")
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("v2ray base64 decode failed: %v", err)
	}
	decStr := string(decoded)
	if !strings.HasPrefix(decStr, "ss://") {
		t.Fatalf("expected ss:// prefix in decoded string, got %q", decStr)
	}
}

func TestIsDomainHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"example.com", true},
		{"sub.domain.co.uk", true},
		{"1.2.3.4", false},
		{"2001:db8::1", false},
		{"[2001:db8::1]:443", false},
		{"0.0.0.0", false},
		{"localhost", false},
	}
	for _, tt := range tests {
		if got := IsDomainHost(tt.host); got != tt.want {
			t.Errorf("IsDomainHost(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}
