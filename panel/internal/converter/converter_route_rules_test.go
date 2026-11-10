package converter

import (
	"encoding/json"
	"testing"

	"github.com/ladderairport/panel/internal/store"
)

func testSSInbound(id string, port int) store.InboundConfig {
	return store.InboundConfig{
		ID: id, Name: "ss-" + id, Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": port, "method": "aes-128-gcm", "password": "pw-" + id,
		},
	}
}

// decodeConfig unmarshals a generated config and returns its route rules.
func decodeRouteRules(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	route, ok := cfg["route"].(map[string]any)
	if !ok {
		t.Fatalf("route = %v", cfg["route"])
	}
	if route["final"] != "direct" {
		t.Fatalf("final = %v", route["final"])
	}
	rulesRaw, _ := route["rules"].([]any)
	out := []map[string]any{}
	for _, item := range rulesRaw {
		rule, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("rule = %v", item)
		}
		out = append(out, rule)
	}
	return out
}

func TestConvertRouteRulesTargetExtraOutbounds(t *testing.T) {
	inbounds := []store.InboundConfig{testSSInbound("a1", 11001), testSSInbound("b2", 11002)}
	chainOutbound := map[string]any{
		"type": "shadowsocks", "tag": "chain-deadbeef-hop-0",
		"server": "198.51.100.2", "server_port": 443, "method": "aes-128-gcm", "password": "x",
	}
	raw, err := Convert(inbounds, ConvertOptions{
		Outbounds: []map[string]any{chainOutbound},
		RouteRules: []RouteRule{
			{MatchType: "domain", MatchValue: "www.corp.example", Outbound: "chain-deadbeef-hop-0"},
			{MatchType: "domain_suffix", MatchValue: "internal.example", Outbound: "direct"},
			{MatchType: "ip_cidr", MatchValue: "203.0.113.0/24", Reject: true},
			{MatchType: "process_name", MatchValue: "telegram", Outbound: "direct"}, // agent-side: skipped
			{MatchType: "domain", MatchValue: "dangling.example", Outbound: "no-such-outbound"},
			{MatchType: "domain_keyword", MatchValue: "", Outbound: "direct"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rules := decodeRouteRules(t, raw)
	// 3 valid plan rules; process_name/dangling/empty skipped. No per-inbound
	// chain rules: inbounds all egress through route.final.
	if len(rules) != 3 {
		t.Fatalf("rules = %v", rules)
	}
	for _, rule := range rules {
		if _, ok := rule["inbound"]; ok {
			t.Fatalf("no rule may match by inbound: %v", rule)
		}
	}
	if rule := rules[0]; rule["action"] != "route" || rule["outbound"] != "chain-deadbeef-hop-0" {
		t.Fatalf("proxy plan rule = %v", rule)
	}
	if domains, ok := rules[0]["domain"].([]any); !ok || len(domains) != 1 || domains[0] != "www.corp.example" {
		t.Fatalf("proxy plan rule domain = %v", rules[0]["domain"])
	}
	if rule := rules[1]; rule["action"] != "route" || rule["outbound"] != "direct" {
		t.Fatalf("direct plan rule = %v", rule)
	}
	if _, ok := rules[1]["domain_suffix"]; !ok {
		t.Fatalf("direct plan rule condition = %v", rules[1])
	}
	if rule := rules[2]; rule["action"] != "reject" {
		t.Fatalf("block plan rule = %v", rule)
	}
	if _, hasOutbound := rules[2]["outbound"]; hasOutbound {
		t.Fatalf("reject rule must not carry outbound: %v", rules[2])
	}
	if _, ok := rules[2]["ip_cidr"]; !ok {
		t.Fatalf("block plan rule condition = %v", rules[2])
	}
}

func TestConvertNodeDNSBlock(t *testing.T) {
	inbounds := []store.InboundConfig{testSSInbound("a1", 11001)}
	chainEntry := map[string]any{
		"type": "shadowsocks", "tag": "chain-hop-0",
		"server": "entry.example", "server_port": 443, "method": "aes-128-gcm", "password": "x",
	}
	chainExit := map[string]any{
		"type": "shadowsocks", "tag": "chain-hop-1", "detour": "chain-hop-0",
		"server": "exit.example", "server_port": 443, "method": "aes-128-gcm", "password": "y",
	}
	raw, err := Convert(inbounds, ConvertOptions{
		BindInterface: "eth0",
		Outbounds:     []map[string]any{chainEntry, chainExit},
		RouteRules: []RouteRule{
			{MatchType: "domain", MatchValue: "corp.example", Outbound: "chain-hop-1"},
			{MatchType: "ip_cidr", MatchValue: "203.0.113.0/24", Outbound: "direct"},
			{MatchType: "domain_suffix", MatchValue: "example.com", Outbound: "direct"},
		},
		DNS:                 map[string]any{"final": "doh-ali"},
		ChainDomainResolver: map[string]any{"server": "doh-ali", "strategy": "ipv4_only"},
		Sniff:               true,
		SniffOverride:       true,
		SniffTimeout:        "300ms",
		ResolveStrategy:     "prefer_ipv4",
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["dns"].(map[string]any)["final"] != "doh-ali" {
		t.Fatalf("dns = %v", cfg["dns"])
	}
	inbound := cfg["inbounds"].([]any)[0].(map[string]any)
	if inbound["sniff"] != true || inbound["sniff_override_destination"] != true || inbound["sniff_timeout"] != "300ms" {
		t.Fatalf("inbound sniff = %v", inbound)
	}
	direct := cfg["outbounds"].([]any)[0].(map[string]any)
	if _, ok := direct["domain_resolver"]; ok {
		t.Fatalf("direct must not set domain_resolver: %v", direct)
	}
	if direct["bind_interface"] != "eth0" {
		t.Fatalf("direct bind = %v", direct["bind_interface"])
	}
	entry := cfg["outbounds"].([]any)[1].(map[string]any)
	resolver := entry["domain_resolver"].(map[string]any)
	if resolver["server"] != "doh-ali" || resolver["strategy"] != "ipv4_only" {
		t.Fatalf("chain entry resolver = %v", resolver)
	}
	exit := cfg["outbounds"].([]any)[2].(map[string]any)
	if _, ok := exit["domain_resolver"]; ok {
		t.Fatalf("detoured hop must leave resolution to its upstream: %v", exit)
	}
	rules := decodeRouteRules(t, raw)
	if rules[0]["action"] != "sniff" {
		t.Fatalf("first rule = %v", rules[0])
	}
	if rules[1]["outbound"] != "chain-hop-1" {
		t.Fatalf("chain rule = %v", rules[1])
	}
	if _, ok := rules[2]["domain_suffix"]; !ok {
		t.Fatalf("domain rule should precede resolve: %v", rules[2])
	}
	if rules[3]["action"] != "resolve" || rules[3]["strategy"] != "prefer_ipv4" {
		t.Fatalf("resolve = %v", rules[3])
	}
	if _, ok := rules[3]["server"]; ok {
		t.Fatal("resolve must omit server so the query uses dns.final")
	}
	if _, ok := rules[4]["ip_cidr"]; !ok {
		t.Fatalf("ip rule = %v", rules[4])
	}
	route := cfg["route"].(map[string]any)
	if _, ok := route["rule_set"]; ok {
		t.Fatalf("node DNS must not add a rule set: %v", route["rule_set"])
	}
}

func TestConvertRouteRulesNilKeepsLegacyShape(t *testing.T) {
	raw, err := Convert([]store.InboundConfig{testSSInbound("a1", 11001)}, ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	route := cfg["route"].(map[string]any)
	if _, hasRules := route["rules"]; hasRules {
		t.Fatalf("route = %v", route)
	}
}
