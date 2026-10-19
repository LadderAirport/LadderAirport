package nodedns

import "testing"

func TestDefaultEmitPartitionsQueries(t *testing.T) {
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
	if emitted.DNS["final"] != TagCloudflare || emitted.DNS["strategy"] != StrategyPreferIPv4 {
		t.Fatalf("dns = %#v", emitted.DNS)
	}
	rules, _ := emitted.DNS["rules"].([]map[string]any)
	if len(rules) != 3 || rules[0]["server"] != TagDNSPod || rules[1]["server"] != TagAli || rules[2]["server"] != TagGoogle {
		t.Fatalf("rules = %#v", rules)
	}
	if emitted.RuleSet["tag"] != RuleSetGeositeCN || emitted.RuleSet["download_detour"] != "direct" {
		t.Fatalf("rule set = %#v", emitted.RuleSet)
	}
	if emitted.ChainResolver["server"] != TagCloudflare || emitted.ChainResolver["strategy"] != StrategyIPv4Only {
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
	if resolver["server"] != TagGoogle {
		t.Fatalf("bootstrap = %#v, want %s (Cloudflare itself is a hostname)", resolver, TagGoogle)
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
	cfg.Servers = cfg.Servers[:3]
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected missing tag error")
	}
}
