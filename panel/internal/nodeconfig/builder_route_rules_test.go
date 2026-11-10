package nodeconfig

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ladderairport/panel/internal/store"
)

func openRouteRuleStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func addNodeWithInbound(t *testing.T, st *store.Store, name, address string, port int) (store.Node, store.InboundConfig) {
	t.Helper()
	node, inbound := addNodeAndInbound(t, st, name, address, port)
	if err := st.SetNodeInbounds(node.ID, []string{inbound.ID}); err != nil {
		t.Fatal(err)
	}
	return node, inbound
}

// addNodeAndInbound creates the node and inbound without a standalone
// attachment.
func addNodeAndInbound(t *testing.T, st *store.Store, name, address string, port int) (store.Node, store.InboundConfig) {
	t.Helper()
	node := &store.Node{Name: name, Address: address, GRPCPort: 50051, Status: "online"}
	if err := st.CreateNode(node); err != nil {
		t.Fatal(err)
	}
	inbound := &store.InboundConfig{
		Name: "ss-" + name, Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{"port": port, "method": "aes-128-gcm", "password": "pw-" + name},
	}
	if err := st.CreateInbound(inbound); err != nil {
		t.Fatal(err)
	}
	return *node, *inbound
}

func TestGlobalRouteRulesComposeChainWithDetour(t *testing.T) {
	st := openRouteRuleStore(t)
	nodeA, inA := addNodeWithInbound(t, st, "a", "192.0.2.1", 11001)
	nodeB, inB := addNodeWithInbound(t, st, "b", "192.0.2.2", 11002)
	nodeC, _ := addNodeWithInbound(t, st, "c", "192.0.2.3", 11003)

	chain := &store.ProxyChain{
		Name: "res-chain",
		Hops: []store.ProxyChainHop{
			{NodeID: nodeA.ID, InboundID: inA.ID},
			{NodeID: nodeB.ID, InboundID: inB.ID, DialAddress: "10.0.0.2"},
		},
	}
	if err := st.CreateProxyChain(chain); err != nil {
		t.Fatal(err)
	}
	chain.Enabled = true
	plan := &store.RoutePlan{
		Name: "g", Scope: "global", Enabled: true,
		Rules: []store.RoutePlanRule{
			{MatchType: "domain_suffix", MatchValue: "corp.example", Action: "proxy", TargetChainID: chain.ID, Enabled: true},
			{MatchType: "domain", MatchValue: "plain.example", Action: "direct", Enabled: true},
			{MatchType: "ip_cidr", MatchValue: "198.51.100.0/24", Action: "block", Enabled: true},
			{MatchType: "process_name", MatchValue: "ssh", Action: "direct", Enabled: true},
			{MatchType: "domain", MatchValue: "off.example", Action: "direct", Enabled: false},
			{MatchType: "domain_suffix", MatchValue: "again.example", Action: "proxy", TargetChainID: chain.ID, Enabled: true},
		},
	}
	if err := st.CreateRoutePlan(plan); err != nil {
		t.Fatal(err)
	}
	disabled := &store.RoutePlan{
		Name: "off", Scope: "global", Enabled: false,
		Rules: []store.RoutePlanRule{
			{MatchType: "domain", MatchValue: "nope.example", Action: "direct", Enabled: true},
		},
	}
	if err := st.CreateRoutePlan(disabled); err != nil {
		t.Fatal(err)
	}

	builder := &Builder{Store: st}
	chains := []store.ProxyChain{*chain}
	exitTag := ChainOutboundTag(chain.ID, 1)

	// Entry hop continues from the next hop: one outbound, dialed directly.
	rules, outbounds, err := builder.globalRouteRules(nodeA.ID, chains, "eth1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 4 || rules[0].Outbound != exitTag || rules[3].Outbound != exitTag {
		t.Fatalf("nodeA rules = %+v", rules)
	}
	if rules[1].Outbound != "direct" || !rules[2].Reject {
		t.Fatalf("nodeA rules = %+v", rules)
	}
	if len(outbounds) != 1 {
		t.Fatalf("nodeA outbounds = %+v (the chain must be composed once)", outbounds)
	}
	if outbounds[0]["server"] != "10.0.0.2" || outbounds[0]["bind_interface"] != "eth1" {
		t.Fatalf("nodeA next hop = %+v", outbounds[0])
	}
	if _, ok := outbounds[0]["detour"]; ok {
		t.Fatalf("first composed hop must dial directly: %+v", outbounds[0])
	}

	// Exit hop: proxy degrades to direct, nothing composed.
	rules, outbounds, err = builder.globalRouteRules(nodeB.ID, chains, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 4 || rules[0].Outbound != "direct" || len(outbounds) != 0 {
		t.Fatalf("nodeB rules = %+v outbounds = %+v", rules, outbounds)
	}

	// Off-chain node dials the whole chain: entry direct, exit via detour.
	rules, outbounds, err = builder.globalRouteRules(nodeC.ID, chains, "eth1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 4 || rules[0].Outbound != exitTag {
		t.Fatalf("nodeC rules = %+v", rules)
	}
	if len(outbounds) != 2 {
		t.Fatalf("nodeC outbounds = %+v", outbounds)
	}
	entry, exit := outbounds[0], outbounds[1]
	if entry["tag"] != ChainOutboundTag(chain.ID, 0) || entry["server"] != "192.0.2.1" || entry["bind_interface"] != "eth1" {
		t.Fatalf("nodeC entry = %+v", entry)
	}
	if exit["tag"] != exitTag || exit["detour"] != entry["tag"] || exit["server"] != "10.0.0.2" {
		t.Fatalf("nodeC exit = %+v", exit)
	}
	if _, ok := exit["bind_interface"]; ok {
		t.Fatalf("detoured hop must not carry dial fields: %+v", exit)
	}

	// Disabled chain: the proxy rule is skipped.
	chains[0].Enabled = false
	rules, outbounds, err = builder.globalRouteRules(nodeC.ID, chains, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || len(outbounds) != 0 {
		t.Fatalf("disabled chain rules = %+v outbounds = %+v", rules, outbounds)
	}
}

func TestBuildDoesNotDeployChainHops(t *testing.T) {
	st := openRouteRuleStore(t)
	nodeA, inA := addNodeWithInbound(t, st, "a", "192.0.2.1", 11001)
	nodeB, inB := addNodeWithInbound(t, st, "b", "192.0.2.2", 11002)
	chain := &store.ProxyChain{
		Name: "plain",
		Hops: []store.ProxyChainHop{
			{NodeID: nodeA.ID, InboundID: inA.ID},
			{NodeID: nodeB.ID, InboundID: inB.ID},
		},
	}
	if err := st.CreateProxyChain(chain); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProxyChainEnabled(chain.ID, true); err != nil {
		t.Fatal(err)
	}
	result, err := (&Builder{Store: st}).Build(nodeA.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Inbounds  []map[string]any `json:"inbounds"`
		Outbounds []map[string]any `json:"outbounds"`
		Route     map[string]any   `json:"route"`
	}
	if err := json.Unmarshal([]byte(result.JSON), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inbounds) != 1 || len(cfg.Outbounds) != 1 || cfg.Outbounds[0]["tag"] != "direct" {
		t.Fatalf("chain without a global plan must not change the node: %s", result.JSON)
	}
	if _, ok := cfg.Route["rules"]; ok {
		t.Fatalf("no route rules expected: %v", cfg.Route)
	}
}

func TestBuildIncludesGlobalRouteRules(t *testing.T) {
	st := openRouteRuleStore(t)
	node, _ := addNodeWithInbound(t, st, "solo", "192.0.2.10", 12001)
	plan := &store.RoutePlan{
		Name: "g2", Scope: "global", Enabled: true,
		Rules: []store.RoutePlanRule{
			{MatchType: "domain_keyword", MatchValue: "ads", Action: "block", Enabled: true},
			{MatchType: "domain_suffix", MatchValue: "lan.example", Action: "direct", Enabled: true},
		},
	}
	if err := st.CreateRoutePlan(plan); err != nil {
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
	route := cfg["route"].(map[string]any)
	rules, ok := route["rules"].([]any)
	if !ok || len(rules) != 2 {
		t.Fatalf("rules = %v", route["rules"])
	}
	first := rules[0].(map[string]any)
	if first["action"] != "reject" {
		t.Fatalf("first rule = %v", first)
	}
	kw := first["domain_keyword"].([]any)
	if len(kw) != 1 || kw[0] != "ads" {
		t.Fatalf("first rule = %v", first)
	}
	second := rules[1].(map[string]any)
	if second["action"] != "route" || second["outbound"] != "direct" {
		t.Fatalf("second rule = %v", second)
	}
	if route["final"] != "direct" {
		t.Fatalf("final = %v", route["final"])
	}
}
