package nodedns

import "testing"

func TestDefaultEmitUsesFirstEnabledAsFinal(t *testing.T) {
	if !SupportsSchema("1.12.22") || !SupportsSchema("v1.13.0") {
		t.Fatal("1.12+ should support the schema")
	}
	if SupportsSchema("") || SupportsSchema("1.11.9") || SupportsSchema("dev") {
		t.Fatal("empty or pre-1.12 versions must not emit the new block")
	}
	cfg := Default()
	emitted, err := cfg.Emit(EmitOptions{BindInterface: "eth0"})
	if err != nil {
		t.Fatal(err)
	}
	servers, _ := emitted.DNS["servers"].([]map[string]any)
	if len(servers) != 4 {
		t.Fatalf("servers = %#v", emitted.DNS["servers"])
	}
	want := []struct {
		tag, server, name string
	}{
		{TagAli, "223.5.5.5", "dns.alidns.com"},
		{TagDNSPod, "1.12.12.12", "doh.pub"},
		{TagCloudflare, "1.1.1.1", "cloudflare-dns.com"},
		{TagGoogle, "8.8.8.8", "dns.google"},
	}
	for i, item := range want {
		if servers[i]["tag"] != item.tag || servers[i]["server"] != item.server {
			t.Fatalf("server %d = %#v", i, servers[i])
		}
		if servers[i]["bind_interface"] != "eth0" {
			t.Fatalf("bind = %#v", servers[i]["bind_interface"])
		}
		tls, _ := servers[i]["tls"].(map[string]any)
		if tls["server_name"] != item.name || tls["enabled"] != true {
			t.Fatalf("tls %d = %#v", i, tls)
		}
		if _, ok := servers[i]["domain_resolver"]; ok {
			t.Fatalf("IP server must not need a bootstrap resolver: %#v", servers[i])
		}
	}
	if emitted.DNS["final"] != TagAli || emitted.DNS["strategy"] != StrategyPreferIPv4 {
		t.Fatalf("dns = %#v", emitted.DNS)
	}
	if _, ok := emitted.DNS["rules"]; ok {
		t.Fatalf("dns rules must not split queries: %#v", emitted.DNS["rules"])
	}
	if emitted.RuleSet != nil {
		t.Fatalf("rule set = %#v", emitted.RuleSet)
	}
	if emitted.ChainResolver["server"] != TagAli || emitted.ChainResolver["strategy"] != StrategyIPv4Only {
		t.Fatalf("chain resolver = %#v", emitted.ChainResolver)
	}
	skipped, err := cfg.Emit(EmitOptions{BindInterface: "eth0", SkipBind: true})
	if err != nil {
		t.Fatal(err)
	}
	skippedServers := skipped.DNS["servers"].([]map[string]any)
	if _, ok := skippedServers[0]["bind_interface"]; ok {
		t.Fatal("SkipBind should omit bind_interface")
	}
}

func TestParseEmptyIsDefault(t *testing.T) {
	cfg, err := Parse("  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 4 || !cfg.Sniff || !cfg.SniffOverride || cfg.CacheCapacity != 2048 || !cfg.IndependentCache || !cfg.ReverseMapping {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestNormalizeHostnameUsesIPBootstrap(t *testing.T) {
	cfg := Default()
	cfg.Servers[2].Server = "cloudflare-dns.com"
	cfg.ClientSubnet = "203.0.113.8"
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.ClientSubnet != "203.0.113.8/32" {
		t.Fatalf("subnet = %q", cfg.ClientSubnet)
	}
	emitted, err := cfg.Emit(EmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	servers := emitted.DNS["servers"].([]map[string]any)
	resolver, _ := servers[2]["domain_resolver"].(map[string]any)
	if resolver["server"] != TagAli {
		t.Fatalf("bootstrap = %#v, want %s (first enabled IP)", resolver, TagAli)
	}
	if emitted.DNS["client_subnet"] != "203.0.113.8/32" {
		t.Fatalf("emitted subnet = %#v", emitted.DNS["client_subnet"])
	}
}

func TestNormalizeRejectsUnusableConfig(t *testing.T) {
	cfg := Default()
	for i := range cfg.Servers {
		cfg.Servers[i].Server = "dns.example"
	}
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected bootstrap error when every server is a hostname")
	}
	cfg = Default()
	cfg.ClientSubnet = "not-a-prefix"
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected client_subnet error")
	}
	cfg = Default()
	off := false
	for i := range cfg.Servers {
		cfg.Servers[i].Enabled = &off
	}
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected an enabled final server")
	}
	cfg = Default()
	cfg.Servers[0].Tag = "bad tag"
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected tag error")
	}
}

func TestLegacyJSONKeepsOrderAndEnablesRows(t *testing.T) {
	raw := `{
		"strategy":"prefer_ipv4","sniff":true,"sniff_override_destination":true,"sniff_timeout":"300ms",
		"independent_cache":true,"cache_capacity":2048,"reverse_mapping":true,
		"servers":[
			{"tag":"doh-cf","type":"https","server":"1.1.1.1","server_port":443,"path":"/dns-query","server_name":"cloudflare-dns.com"},
			{"tag":"doh-ali","type":"https","server":"223.5.5.5","server_port":443,"path":"/dns-query","server_name":"dns.alidns.com"}
		]
	}`
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[0].Tag != TagCloudflare || !serverEnabled(cfg.Servers[0]) || cfg.Servers[1].Tag != TagAli {
		t.Fatalf("migrated = %+v", cfg.Servers)
	}
	emitted, err := cfg.Emit(EmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if emitted.DNS["final"] != TagCloudflare || emitted.ChainResolver["server"] != TagCloudflare {
		t.Fatalf("final = %#v chain = %#v", emitted.DNS["final"], emitted.ChainResolver)
	}
}

func TestReorderAndDisableSelectFinal(t *testing.T) {
	cfg := Default()
	cfg.Servers[0], cfg.Servers[2] = cfg.Servers[2], cfg.Servers[0]
	off := false
	cfg.Servers[0].Enabled = &off
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	emitted, err := cfg.Emit(EmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	servers := emitted.DNS["servers"].([]map[string]any)
	if len(servers) != 3 || emitted.DNS["final"] != TagDNSPod || emitted.ChainResolver["server"] != TagDNSPod {
		t.Fatalf("servers=%#v final=%v chain=%v", servers, emitted.DNS["final"], emitted.ChainResolver)
	}
	udp := enabledServer("plain", "udp", "9.9.9.9", 53, "", "")
	cfg.Servers = append(cfg.Servers, udp)
	emitted, err = cfg.Emit(EmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	servers = emitted.DNS["servers"].([]map[string]any)
	last := servers[len(servers)-1]
	if last["type"] != "udp" || last["server_port"] != 53 {
		t.Fatalf("udp = %#v", last)
	}
	if _, ok := last["tls"]; ok {
		t.Fatalf("udp must not emit tls: %#v", last)
	}
}
