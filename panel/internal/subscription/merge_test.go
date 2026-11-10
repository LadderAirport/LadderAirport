package subscription

import (
	"encoding/base64"
	"testing"
)

func trojanEP(name, server string, port int, password, sni string) ProxyEndpoint {
	p := map[string]any{"password": password}
	if sni != "" {
		p["server_name"] = sni
	}
	return ProxyEndpoint{Name: name, Server: server, Port: port, Protocol: "trojan", Params: p}
}

func names(eps []ProxyEndpoint) []string {
	out := make([]string, 0, len(eps))
	for _, ep := range eps {
		out = append(out, ep.Name)
	}
	return out
}

func TestMergeKeepsLocalInboundsSharingPort(t *testing.T) {
	local := []ProxyEndpoint{
		{Name: "hy2", Server: "1.2.3.4", Port: 443, Protocol: "hysteria2", Params: map[string]any{"password": "p"}},
		{Name: "reality", Server: "1.2.3.4", Port: 443, Protocol: "vless", Params: map[string]any{
			"uuid": "u", "tls_mode": "reality", "public_key": "pk", "short_id": "ab", "server_name": "www.example.com",
		}},
		// Even an identical local copy is kept: local inventory is never deduped.
		{Name: "hy2-copy", Server: "1.2.3.4", Port: 443, Protocol: "hysteria2", Params: map[string]any{"password": "p"}},
	}
	out, dropped := MergeEndpointsWithReport(local, nil)
	if len(out) != 3 || len(dropped) != 0 {
		t.Fatalf("out=%v dropped=%v", names(out), dropped)
	}
}

func TestMergeKeepsCDNNodesDifferingBySNI(t *testing.T) {
	ext := []ProxyEndpoint{
		trojanEP("HK", "cdn.example.com", 443, "pw", "hk.example.net"),
		trojanEP("JP", "cdn.example.com", 443, "pw", "jp.example.net"),
		trojanEP("US", "104.16.1.1", 443, "pw", "us.example.net"),
	}
	out, dropped := MergeEndpointsWithReport(nil, ext)
	if len(out) != 3 || len(dropped) != 0 {
		t.Fatalf("out=%v dropped=%v", names(out), dropped)
	}
}

func TestMergeKeepsSameHostPortDifferentProtocolOrCredential(t *testing.T) {
	ext := []ProxyEndpoint{
		trojanEP("a", "1.2.3.4", 443, "pw1", ""),
		trojanEP("b", "1.2.3.4", 443, "pw2", ""),
		{Name: "c", Server: "1.2.3.4", Port: 443, Protocol: "hysteria2", Params: map[string]any{"password": "pw1"}},
	}
	out, dropped := MergeEndpointsWithReport(nil, ext)
	if len(out) != 3 || len(dropped) != 0 {
		t.Fatalf("out=%v dropped=%v", names(out), dropped)
	}
}

func TestMergeDropsIdenticalExternalAcrossSources(t *testing.T) {
	skip := true
	ext := []ProxyEndpoint{
		trojanEP("机场A-HK", "Node.Example.com", 443, "pw", "hk.example.net"),
		trojanEP("机场A-JP", "jp.example.com", 443, "pw", "jp.example.net"),
		// Same config from another source: different name, host case, SNI case
		// and insecure flag are all ignored.
		trojanEP("机场B-香港", "node.example.com", 443, "pw", "HK.example.net"),
	}
	ext[2].TLSSkipVerify = &skip
	out, dropped := MergeEndpointsWithReport(nil, ext)
	if got := names(out); len(got) != 2 || got[0] != "机场A-HK" || got[1] != "机场A-JP" {
		t.Fatalf("out=%v", got)
	}
	if len(dropped) != 1 || dropped[0].Name != "机场B-香港" || dropped[0].DuplicateOf != "机场A-HK" {
		t.Fatalf("dropped=%+v", dropped)
	}
	if w := DedupeWarnings(dropped); len(w) != 1 {
		t.Fatalf("warnings=%v", w)
	}
}

func TestMergePrefersLocalOverIdenticalExternal(t *testing.T) {
	local := []ProxyEndpoint{trojanEP("mine", "203.0.113.9", 443, "pw", "my.example.com")}
	ext := []ProxyEndpoint{
		trojanEP("theirs", "203.0.113.9", 443, "pw", "my.example.com"),
		trojanEP("other-port", "203.0.113.9", 8443, "pw", "my.example.com"),
	}
	out, dropped := MergeEndpointsWithReport(local, ext)
	if got := names(out); len(got) != 2 || got[0] != "mine" || got[1] != "other-port" {
		t.Fatalf("out=%v", got)
	}
	if len(dropped) != 1 || dropped[0].DuplicateOf != "mine" {
		t.Fatalf("dropped=%+v", dropped)
	}
}

func TestMergeLocalRealityMatchesExternalPublicKey(t *testing.T) {
	// Local inbounds carry the server-side private key; an external share link
	// carries the derived public key. Both must normalize to one identity.
	priv := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	pub, err := realityPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"uuid": "u", "tls_mode": "reality", "short_id": "ab", "server_name": "www.example.com"}
	lp := map[string]any{"private_key": priv}
	ep := map[string]any{"public_key": pub}
	for k, v := range base {
		lp[k] = v
		ep[k] = v
	}
	local := []ProxyEndpoint{{Name: "mine", Server: "1.2.3.4", Port: 443, Protocol: "vless", Params: lp}}
	ext := []ProxyEndpoint{{Name: "theirs", Server: "1.2.3.4", Port: 443, Protocol: "vless", Params: ep}}
	out, _ := MergeEndpointsWithReport(local, ext)
	if len(out) != 1 {
		t.Fatalf("out=%v", names(out))
	}
}

func TestMergeUnrenderableExternalNotCollapsed(t *testing.T) {
	ext := []ProxyEndpoint{
		{Name: "a", Server: "1.2.3.4", Port: 443, Protocol: "weird", Params: map[string]any{"k": "1"}},
		{Name: "b", Server: "1.2.3.4", Port: 443, Protocol: "weird", Params: map[string]any{"k": "2"}},
	}
	out, _ := MergeEndpointsWithReport(nil, ext)
	if len(out) != 2 {
		t.Fatalf("out=%v", names(out))
	}
}

func TestFilterPlaceholderServers(t *testing.T) {
	eps := []ProxyEndpoint{
		{Name: "notice", Server: "0.0.0.0", Port: 443, Protocol: "trojan"},
		{Name: "v6", Server: "::", Port: 443, Protocol: "trojan"},
		{Name: "loop", Server: "127.0.0.1", Port: 443, Protocol: "trojan"},
		{Name: "ok", Server: "1.2.3.4", Port: 443, Protocol: "trojan"},
		{Name: "badport", Server: "1.2.3.4", Port: 0, Protocol: "trojan"},
	}
	out := filterDialableEndpoints(eps)
	if len(out) != 1 || out[0].Name != "ok" {
		t.Fatalf("%+v", out)
	}
}

func TestMergeDropsPlaceholderExternal(t *testing.T) {
	local := []ProxyEndpoint{{Name: "mine", Server: "9.9.9.9", Port: 443, Protocol: "trojan"}}
	ext := []ProxyEndpoint{
		{Name: "notice", Server: "0.0.0.0", Port: 443, Protocol: "trojan"},
		{Name: "good", Server: "8.8.8.8", Port: 443, Protocol: "trojan"},
	}
	out := MergeEndpoints(local, ext)
	if len(out) != 2 {
		t.Fatalf("got %d: %+v", len(out), out)
	}
	for _, ep := range out {
		if ep.Server == "0.0.0.0" {
			t.Fatalf("placeholder leaked: %+v", ep)
		}
	}
}
