package nodedns

import (
	"strings"
	"testing"
)

func TestDefaultEmitUsesFirstRowAsFinal(t *testing.T) {
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
		name, server string
	}{
		{NameAli, "223.5.5.5"},
		{NameDNSPod, "1.12.12.12"},
		{NameCloudflare, "1.1.1.1"},
		{NameGoogle, "8.8.8.8"},
	}
	for i, item := range want {
		if servers[i]["tag"] != item.name || servers[i]["type"] != "https" || servers[i]["server"] != item.server {
			t.Fatalf("server %d = %#v", i, servers[i])
		}
		if servers[i]["path"] != defaultPath || servers[i]["bind_interface"] != "eth0" {
			t.Fatalf("dial = %#v", servers[i])
		}
		tls, _ := servers[i]["tls"].(map[string]any)
		if tls["server_name"] != item.server || tls["enabled"] != true {
			t.Fatalf("tls %d = %#v", i, tls)
		}
		if _, ok := servers[i]["domain_resolver"]; ok {
			t.Fatalf("IP URL must not need a bootstrap resolver: %#v", servers[i])
		}
	}
	if emitted.DNS["final"] != NameAli || emitted.DNS["strategy"] != StrategyPreferIPv4 {
		t.Fatalf("dns = %#v", emitted.DNS)
	}
	if _, ok := emitted.DNS["rules"]; ok {
		t.Fatalf("dns rules must not split queries: %#v", emitted.DNS["rules"])
	}
	if emitted.RuleSet != nil {
		t.Fatalf("rule set = %#v", emitted.RuleSet)
	}
	if emitted.ChainResolver["server"] != NameAli || emitted.ChainResolver["strategy"] != StrategyIPv4Only {
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
	if len(cfg.Servers) != 4 || cfg.Servers[0].URL != "https://223.5.5.5/dns-query" || !cfg.Sniff || cfg.CacheCapacity != 2048 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseLegacyMigratesToNameAndURL(t *testing.T) {
	raw := `{
		"strategy":"prefer_ipv4","sniff":true,"sniff_override_destination":true,"sniff_timeout":"300ms",
		"independent_cache":true,"cache_capacity":2048,"reverse_mapping":true,
		"servers":[
			{"tag":"doh-cf","type":"https","server":"1.1.1.1","server_port":443,"path":"/dns-query","server_name":"cloudflare-dns.com"},
			{"tag":"old","type":"udp","server":"9.9.9.9","server_port":53,"enabled":false},
			{"tag":"doh-ali","type":"https","server":"223.5.5.5","server_port":443,"path":"/dns-query","server_name":"dns.alidns.com"}
		]
	}`
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 2 || cfg.Servers[0].Name != NameCloudflare || cfg.Servers[0].URL != "https://1.1.1.1/dns-query" {
		t.Fatalf("migrated = %+v", cfg.Servers)
	}
	if cfg.Servers[1].Name != NameAli || cfg.Servers[1].URL != "https://223.5.5.5/dns-query" {
		t.Fatalf("second = %+v", cfg.Servers[1])
	}
	emitted, err := cfg.Emit(EmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if emitted.DNS["final"] != NameCloudflare || emitted.ChainResolver["server"] != NameCloudflare {
		t.Fatalf("final = %#v chain = %#v", emitted.DNS["final"], emitted.ChainResolver)
	}
	tls := emitted.DNS["servers"].([]map[string]any)[0]["tls"].(map[string]any)
	if tls["server_name"] != "1.1.1.1" {
		t.Fatalf("server_name follows the URL host, got %#v", tls)
	}
}

func TestHostnameUsesFirstIPThenSystem(t *testing.T) {
	cfg := Config{Servers: []Server{
		{Name: "cf", URL: "https://cloudflare-dns.com/dns-query"},
		{Name: "ali", URL: "https://223.5.5.5/dns-query"},
	}}
	cfg.ClientSubnet = "203.0.113.8"
	emitted, err := cfg.Emit(EmitOptions{BindInterface: "eth0"})
	if err != nil {
		t.Fatal(err)
	}
	if emitted.DNS["client_subnet"] != "203.0.113.8/32" {
		t.Fatalf("subnet dns=%v", emitted.DNS["client_subnet"])
	}
	servers := emitted.DNS["servers"].([]map[string]any)
	if emitted.DNS["final"] != "cf" || len(servers) != 2 {
		t.Fatalf("dns = %#v", emitted.DNS)
	}
	resolver, _ := servers[0]["domain_resolver"].(map[string]any)
	if resolver["server"] != "ali" || servers[0]["bind_interface"] != "eth0" {
		t.Fatalf("hostname dial = %#v", servers[0])
	}
	if _, ok := servers[1]["domain_resolver"]; ok {
		t.Fatalf("IP row must not use a resolver: %#v", servers[1])
	}

	cfg = Config{Servers: []Server{{Name: "cf", URL: "https://one.one.one.one/dns-query"}}}
	emitted, err = cfg.Emit(EmitOptions{BindInterface: "wlan0"})
	if err != nil {
		t.Fatal(err)
	}
	servers = emitted.DNS["servers"].([]map[string]any)
	if len(servers) != 2 || servers[1]["type"] != "local" || servers[1]["tag"] != SystemResolverTag {
		t.Fatalf("system bootstrap = %#v", servers)
	}
	if servers[1]["bind_interface"] != "wlan0" {
		t.Fatalf("local bind = %#v", servers[1])
	}
	resolver, _ = servers[0]["domain_resolver"].(map[string]any)
	if resolver["server"] != SystemResolverTag || emitted.ChainResolver["server"] != "cf" {
		t.Fatalf("resolver = %#v chain = %#v", resolver, emitted.ChainResolver)
	}
}

func TestURLForms(t *testing.T) {
	cfg := Config{Servers: []Server{
		{Name: "quad9", URL: "9.9.9.9"},
		{Name: "dot", URL: "tls://dns.google:8853"},
		{Name: "h3", URL: "h3://8.8.8.8/dns-query"},
	}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[0].URL != "udp://9.9.9.9" || cfg.Servers[1].URL != "tls://dns.google:8853" || cfg.Servers[2].URL != "h3://8.8.8.8/dns-query" {
		t.Fatalf("canonical = %+v", cfg.Servers)
	}
	plain := Config{Servers: []Server{
		{Name: "v6", URL: "2001:db8::1"},
		{Name: "doh", URL: "https://223.5.5.5"},
		{Name: "tcp", URL: "tcp://1.1.1.1:53"},
		{Name: "quic", URL: "quic://dns.google"},
	}}
	if err := plain.Normalize(); err != nil {
		t.Fatal(err)
	}
	if plain.Servers[0].URL != "udp://[2001:db8::1]" || plain.Servers[1].URL != "https://223.5.5.5/dns-query" || plain.Servers[2].URL != "tcp://1.1.1.1" || plain.Servers[3].URL != "quic://dns.google" {
		t.Fatalf("more forms = %+v", plain.Servers)
	}
	emitted, err := cfg.Emit(EmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	servers := emitted.DNS["servers"].([]map[string]any)
	if servers[0]["type"] != "udp" || servers[0]["server_port"] != 53 {
		t.Fatalf("udp = %#v", servers[0])
	}
	if _, ok := servers[0]["tls"]; ok {
		t.Fatalf("udp must not emit tls: %#v", servers[0])
	}
	dot := servers[1]
	if dot["type"] != "tls" || dot["server"] != "dns.google" || dot["server_port"] != 8853 {
		t.Fatalf("tls = %#v", dot)
	}
	resolver := dot["domain_resolver"].(map[string]any)
	if resolver["server"] != "quad9" {
		t.Fatalf("tls bootstrap = %#v", resolver)
	}
	if servers[2]["type"] != "h3" || servers[2]["path"] != "/dns-query" {
		t.Fatalf("h3 = %#v", servers[2])
	}
}

func TestFailoverTriesEarlierRowsThenFinal(t *testing.T) {
	if !SupportsFailover("1.14.3") || !SupportsFailover("v1.14.0") || SupportsFailover("1.13.9") || SupportsFailover("1.12.22") {
		t.Fatal("failover gate")
	}
	if !DropsInboundSniffFields("1.13.0") || DropsInboundSniffFields("1.12.22") {
		t.Fatal("inbound sniff gate")
	}
	cfg := Config{Servers: []Server{
		{Name: "one", URL: "udp://1.1.1.1"},
		{Name: "two", URL: "udp://1.0.0.1"},
		{Name: "three", URL: "https://dns.google/dns-query"},
	}}
	emitted, err := cfg.Emit(EmitOptions{Failover: true, BindInterface: "eth0"})
	if err != nil {
		t.Fatal(err)
	}
	if emitted.DNS["final"] != "three" || emitted.DNS["timeout"] != failoverQueryTimeout {
		t.Fatalf("dns = %#v", emitted.DNS)
	}
	if _, ok := emitted.DNS["independent_cache"]; ok {
		t.Fatal("1.14 emit must drop deprecated independent_cache")
	}
	rules, _ := emitted.DNS["rules"].([]map[string]any)
	if len(rules) != 6 {
		t.Fatalf("rules = %#v", rules)
	}
	if rules[0]["action"] != "evaluate" || rules[0]["server"] != "one" || rules[0]["disable_cache"] != true {
		t.Fatalf("first evaluate = %#v", rules[0])
	}
	if rules[1]["response_rcode"] != "NXDOMAIN" || rules[1]["action"] != "respond" {
		t.Fatalf("nxdomain = %#v", rules[1])
	}
	if rules[2]["ip_accept_any"] != true || rules[3]["server"] != "two" {
		t.Fatalf("rules = %#v", rules)
	}
	if emitted.ChainResolver["server"] != "one" || emitted.ChainResolver["timeout"] != failoverQueryTimeout {
		t.Fatalf("chain resolver stays on the first row: %#v", emitted.ChainResolver)
	}
	servers := emitted.DNS["servers"].([]map[string]any)
	google := servers[2]
	resolver := google["domain_resolver"].(map[string]any)
	if resolver["server"] != "one" || google["bind_interface"] != "eth0" {
		t.Fatalf("hostname bootstrap = %#v", google)
	}

	single, err := (Config{Servers: []Server{{Name: "only", URL: "udp://9.9.9.9"}}}).Emit(EmitOptions{Failover: true})
	if err != nil {
		t.Fatal(err)
	}
	if single.DNS["final"] != "only" {
		t.Fatalf("single = %#v", single.DNS)
	}
	if _, ok := single.DNS["rules"]; ok {
		t.Fatal("one server needs no failover rules")
	}
}

func TestNormalizeRejectsBadRows(t *testing.T) {
	cfg := Config{Servers: []Server{{Name: "bad", URL: "udp://dns.google"}}}
	err := cfg.Normalize()
	if err == nil || !strings.Contains(err.Error(), "第 1 行") {
		t.Fatalf("err = %v", err)
	}
	cfg = Config{Servers: []Server{{Name: "a", URL: "https://1.1.1.1/dns-query"}, {Name: "a", URL: "https://8.8.8.8/dns-query"}}}
	if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "第 2 行") {
		t.Fatalf("duplicate err = %v", err)
	}
	cfg = Default()
	cfg.ClientSubnet = "not-a-prefix"
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected client_subnet error")
	}
	cfg = Config{Servers: []Server{{Name: "plain", URL: "example.com"}}}
	if err := cfg.Normalize(); err == nil {
		t.Fatal("bare hostname must be rejected")
	}
	cfg = Config{Servers: []Server{{Name: "dns-system", URL: "https://1.1.1.1/dns-query"}}}
	if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "dns-system") {
		t.Fatalf("reserved name err = %v", err)
	}
}
