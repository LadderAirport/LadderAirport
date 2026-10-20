package nodeconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ladderairport/panel/internal/store"
)

func TestBuildOmitsDNSUntilSingbox112(t *testing.T) {
	st := openRouteRuleStore(t)
	node, _ := addNodeWithInbound(t, st, "old", "192.0.2.20", 12001)
	result, err := (&Builder{Store: st}).Build(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.JSON, `"dns"`) || strings.Contains(result.JSON, "geosite-cn") {
		t.Fatalf("empty sing-box version must keep the legacy config:\n%s", result.JSON)
	}
	loaded, err := st.GetNode(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.SingboxVersion = "1.11.0"
	if err := st.UpdateNode(loaded); err != nil {
		t.Fatal(err)
	}
	result, err = (&Builder{Store: st}).Build(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.JSON, `"doh-cf"`) {
		t.Fatalf("1.11 must not receive the new dns block:\n%s", result.JSON)
	}
}

func TestBuildEmitsFailoverOn114(t *testing.T) {
	st := openRouteRuleStore(t)
	node, _ := addNodeWithInbound(t, st, "new", "192.0.2.22", 12021)
	loaded, err := st.GetNode(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.SingboxVersion = "1.14.3"
	if err := st.UpdateNode(loaded); err != nil {
		t.Fatal(err)
	}
	result, err := (&Builder{Store: st}).Build(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(result.JSON), &cfg); err != nil {
		t.Fatal(err)
	}
	dns := cfg["dns"].(map[string]any)
	if dns["final"] != "doh-google" {
		t.Fatalf("final = %v", dns["final"])
	}
	rules, _ := dns["rules"].([]any)
	if len(rules) != 9 || rules[0].(map[string]any)["server"] != "doh-ali" {
		t.Fatalf("rules = %v", rules)
	}
	if _, ok := dns["independent_cache"]; ok {
		t.Fatal("independent_cache is deprecated on 1.14")
	}
	inbound := cfg["inbounds"].([]any)[0].(map[string]any)
	if _, ok := inbound["sniff"]; ok {
		t.Fatalf("1.13+ must not emit inbound sniff: %v", inbound)
	}
	route := cfg["route"].(map[string]any)
	routeRules := route["rules"].([]any)
	if routeRules[0].(map[string]any)["action"] != "sniff" {
		t.Fatalf("route sniff = %v", routeRules[0])
	}
}

func TestBuildEmitsPerNodeDNS(t *testing.T) {
	st := openRouteRuleStore(t)
	node, inA := addNodeAndInbound(t, st, "exit", "192.0.2.21", 12011)
	next, inB := addNodeAndInbound(t, st, "next", "next.example", 12012)
	chain := &store.ProxyChain{
		Name: "dns-chain",
		Hops: []store.ProxyChainHop{
			{NodeID: node.ID, InboundID: inA.ID},
			{NodeID: next.ID, InboundID: inB.ID},
		},
	}
	if err := st.CreateProxyChain(chain); err != nil {
		t.Fatal(err)
	}
	chain.Enabled = true

	loaded, err := st.GetNode(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.SingboxVersion = "1.12.22"
	loaded.EgressInterface = "eth0"
	loaded.DNS.ClientSubnet = "203.0.113.0/24"
	loaded.DNS.Servers[2].URL = "https://cloudflare-dns.com/dns-query"
	if err := st.UpdateNode(loaded); err != nil {
		t.Fatal(err)
	}
	result, err := (&Builder{Store: st}).BuildWithChains(node.ID, []store.ProxyChain{*chain})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(result.JSON), &cfg); err != nil {
		t.Fatal(err)
	}
	dns := cfg["dns"].(map[string]any)
	if dns["final"] != "doh-ali" || dns["client_subnet"] != "203.0.113.0/24" {
		t.Fatalf("dns = %v", dns)
	}
	if _, ok := dns["rules"]; ok {
		t.Fatalf("dns rules = %v", dns["rules"])
	}
	servers := dns["servers"].([]any)
	cf := servers[2].(map[string]any)
	tls, _ := cf["tls"].(map[string]any)
	if cf["server"] != "cloudflare-dns.com" || cf["bind_interface"] != "eth0" || tls["server_name"] != "cloudflare-dns.com" {
		t.Fatalf("cloudflare server = %v", cf)
	}
	resolver := cf["domain_resolver"].(map[string]any)
	if resolver["server"] != "doh-ali" {
		t.Fatalf("bootstrap = %v", resolver)
	}
	outbounds := cfg["outbounds"].([]any)
	direct := outbounds[0].(map[string]any)
	if _, ok := direct["domain_resolver"]; ok {
		t.Fatal("direct resolved the user destination through dns.rules, not domain_resolver")
	}
	if direct["bind_interface"] != "eth0" {
		t.Fatalf("direct bind = %v", direct)
	}
	var chainOutbound map[string]any
	for _, item := range outbounds[1:] {
		ob := item.(map[string]any)
		if ob["type"] != "direct" {
			chainOutbound = ob
		}
	}
	if chainOutbound == nil || chainOutbound["domain_resolver"] == nil || chainOutbound["bind_interface"] != "eth0" {
		t.Fatalf("chain outbound = %v", chainOutbound)
	}
	if chainOutbound["server"] != "next.example" {
		t.Fatalf("next hop = %v", chainOutbound["server"])
	}
	inbound := cfg["inbounds"].([]any)[0].(map[string]any)
	if inbound["sniff"] != true {
		t.Fatalf("inbound = %v", inbound)
	}
	if strings.Contains(result.JSON, "geosite-cn") {
		t.Fatalf("node DNS must not download geosite-cn:\n%s", result.JSON)
	}
	route := cfg["route"].(map[string]any)
	rules := route["rules"].([]any)
	if rules[0].(map[string]any)["action"] != "sniff" {
		t.Fatalf("rules = %v", rules)
	}

	loaded.ClientType = "android"
	loaded.EgressInterface = "wlan0"
	if err := st.UpdateNode(loaded); err != nil {
		t.Fatal(err)
	}
	result, err = (&Builder{Store: st}).BuildWithChains(node.ID, []store.ProxyChain{*chain})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.JSON, "bind_interface") {
		t.Fatalf("android node config must not bind an interface:\n%s", result.JSON)
	}
}
