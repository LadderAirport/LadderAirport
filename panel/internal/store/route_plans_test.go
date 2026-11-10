package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func openRoutePlanTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRoutePlanCRUD(t *testing.T) {
	st := openRoutePlanTestStore(t)

	plan := &RoutePlan{
		Name: "Global Split", Scope: "global", Enabled: true,
		Rules: []RoutePlanRule{
			{MatchType: "domain_suffix", MatchValue: "example.com", Action: "direct", Enabled: true},
			{MatchType: "ip_cidr", MatchValue: "10.0.0.0/8", Action: "block", Enabled: false},
		},
	}
	if err := st.CreateRoutePlan(plan); err != nil {
		t.Fatal(err)
	}
	if plan.ID == "" {
		t.Fatal("expected generated plan ID")
	}

	got, err := st.GetRoutePlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Global Split" || got.Scope != "global" || !got.Enabled || got.SubscriptionID != "" {
		t.Fatalf("plan = %+v", got)
	}
	if len(got.Rules) != 2 {
		t.Fatalf("rules = %+v", got.Rules)
	}
	for i, rule := range got.Rules {
		if rule.Position != i {
			t.Fatalf("rule %d position = %d", i, rule.Position)
		}
	}
	if got.Rules[0].MatchValue != "example.com" || got.Rules[1].Enabled {
		t.Fatalf("rules = %+v", got.Rules)
	}

	// Duplicate name (case-insensitive) must fail.
	dup := &RoutePlan{Name: "global split", Scope: "global"}
	if err := st.CreateRoutePlan(dup); err == nil {
		t.Fatal("expected unique-name violation")
	}

	// Update scalar fields only; rules stay untouched.
	got.Name = "Renamed"
	got.Enabled = false
	got.SortOrder = 7
	if err := st.UpdateRoutePlan(got); err != nil {
		t.Fatal(err)
	}
	reloaded, err := st.GetRoutePlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Name != "Renamed" || reloaded.Enabled || reloaded.SortOrder != 7 || len(reloaded.Rules) != 2 {
		t.Fatalf("reloaded = %+v", reloaded)
	}

	// Disabled plan is excluded from the enabled-global listing.
	plans, err := st.ListEnabledGlobalRoutePlans()
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 0 {
		t.Fatalf("enabled global plans = %+v", plans)
	}
	if err := st.UpdateRoutePlan(&RoutePlan{
		ID: plan.ID, Name: "Renamed", Scope: "global", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	plans, err = st.ListEnabledGlobalRoutePlans()
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].Rules) != 1 {
		// Only the enabled rule survives the enabled-only filter.
		t.Fatalf("enabled global plans = %+v", plans)
	}
	if plans[0].Rules[0].MatchValue != "example.com" {
		t.Fatalf("rules = %+v", plans[0].Rules)
	}

	// Delete removes rules via cascade.
	if err := st.DeleteRoutePlan(plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetRoutePlan(plan.ID); err == nil {
		t.Fatal("expected plan to be gone")
	}
	var ruleCount int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM route_plan_rules WHERE plan_id = ?`, plan.ID).Scan(&ruleCount); err != nil {
		t.Fatal(err)
	}
	if ruleCount != 0 {
		t.Fatalf("orphan rules = %d", ruleCount)
	}
	if err := st.DeleteRoutePlan(plan.ID); err == nil {
		t.Fatal("expected not-found on second delete")
	}
}

func TestReplaceRoutePlanRulesResequences(t *testing.T) {
	st := openRoutePlanTestStore(t)
	plan := &RoutePlan{
		Name:  "reseq",
		Scope: "global",
		Rules: []RoutePlanRule{
			{MatchType: "domain", MatchValue: "a.example", Action: "direct"},
		},
	}
	if err := st.CreateRoutePlan(plan); err != nil {
		t.Fatal(err)
	}
	replacement := []RoutePlanRule{
		{MatchType: "domain_keyword", MatchValue: "kw", Action: "block", Position: 9},
		{MatchType: "domain", MatchValue: "b.example", Action: "direct", Enabled: false, Position: 3},
		{MatchType: "ip_cidr", MatchValue: "192.168.0.0/16", Action: "direct", Position: 1},
	}
	if err := st.ReplaceRoutePlanRules(plan.ID, replacement); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRoutePlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 3 {
		t.Fatalf("rules = %+v", got.Rules)
	}
	for i, rule := range got.Rules {
		if rule.Position != i {
			t.Fatalf("rule %d position = %d (want resequenced)", i, rule.Position)
		}
	}
	if got.Rules[0].MatchValue != "kw" || got.Rules[1].MatchValue != "b.example" ||
		got.Rules[2].MatchValue != "192.168.0.0/16" || got.Rules[1].Enabled {
		t.Fatalf("rules = %+v", got.Rules)
	}
	if err := st.ReplaceRoutePlanRules("missing-plan", replacement); err == nil {
		t.Fatal("expected not-found for unknown plan")
	}
}

func TestRoutePlanSubscriptionCascade(t *testing.T) {
	st := openRoutePlanTestStore(t)
	sub := &Subscription{Name: "sub", Token: "tok-cascade", Enabled: true}
	if err := st.CreateSubscription(sub); err != nil {
		t.Fatal(err)
	}
	plan := &RoutePlan{
		Name:           "personal",
		Scope:          "subscription",
		SubscriptionID: sub.ID,
		Enabled:        true,
		Rules: []RoutePlanRule{
			{MatchType: "process_name", MatchValue: "telegram", Action: "direct", Enabled: true},
		},
	}
	if err := st.CreateRoutePlan(plan); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRoutePlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SubscriptionID != sub.ID {
		t.Fatalf("subscription_id = %q", got.SubscriptionID)
	}
	if err := st.DeleteSubscription(sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetRoutePlan(plan.ID); err == nil {
		t.Fatal("expected cascade delete of subscription-bound plan")
	}
}

func TestRoutePlanRuleChainRestrict(t *testing.T) {
	st := openRoutePlanTestStore(t)
	nodeA := &Node{Name: "a", Address: "192.0.2.1", GRPCPort: 50051}
	nodeB := &Node{Name: "b", Address: "192.0.2.2", GRPCPort: 50051}
	if err := st.CreateNode(nodeA); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(nodeB); err != nil {
		t.Fatal(err)
	}
	inboundA := &InboundConfig{Name: "ia", Protocol: "shadowsocks", Enabled: true, Params: map[string]any{
		"port": 11001, "method": "aes-128-gcm", "password": "pw-a",
	}}
	inboundB := &InboundConfig{Name: "ib", Protocol: "shadowsocks", Enabled: true, Params: map[string]any{
		"port": 11002, "method": "aes-128-gcm", "password": "pw-b",
	}}
	if err := st.CreateInbound(inboundA); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInbound(inboundB); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeInbounds(nodeA.ID, []string{inboundA.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeInbounds(nodeB.ID, []string{inboundB.ID}); err != nil {
		t.Fatal(err)
	}
	chain := &ProxyChain{
		Name: "chain-x",
		Hops: []ProxyChainHop{
			{NodeID: nodeA.ID, InboundID: inboundA.ID},
			{NodeID: nodeB.ID, InboundID: inboundB.ID},
		},
	}
	if err := st.CreateProxyChain(chain); err != nil {
		t.Fatal(err)
	}
	plan := &RoutePlan{
		Name:  "via-chain",
		Scope: "global",
		Rules: []RoutePlanRule{
			{MatchType: "domain_suffix", MatchValue: "corp.internal", Action: "proxy", TargetChainID: chain.ID},
		},
	}
	if err := st.CreateRoutePlan(plan); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRoutePlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rules[0].TargetChainID != chain.ID {
		t.Fatalf("target_chain_id = %q", got.Rules[0].TargetChainID)
	}
	if err := st.DeleteProxyChain(chain.ID); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Fatalf("expected FK restrict error, got %v", err)
	}
	// Removing the rule unblocks chain deletion.
	if err := st.ReplaceRoutePlanRules(plan.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProxyChain(chain.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionRoutePlanAndDisabledColumns(t *testing.T) {
	st := openRoutePlanTestStore(t)
	sub := &Subscription{Name: "s", Token: "tok-cols", Enabled: true}
	if err := st.CreateSubscription(sub); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSubscriptionByToken("tok-cols")
	if err != nil {
		t.Fatal(err)
	}
	if got.Disabled || got.RoutePlanID != "" {
		t.Fatalf("defaults = %+v", got)
	}
	got.Disabled = true
	got.RoutePlanID = "plan-1"
	if err := st.UpdateSubscription(got); err != nil {
		t.Fatal(err)
	}
	reloaded, err := st.GetSubscription(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Disabled || reloaded.RoutePlanID != "plan-1" {
		t.Fatalf("reloaded = %+v", reloaded)
	}
}
