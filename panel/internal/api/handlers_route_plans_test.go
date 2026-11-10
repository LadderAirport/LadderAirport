package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ladderairport/panel/internal/store"
)

func setupChainRefs(t *testing.T, st *store.Store) *store.ProxyChain {
	t.Helper()
	nodeA := &store.Node{Name: "rp-a", Address: "192.0.2.11", GRPCPort: 50051}
	nodeB := &store.Node{Name: "rp-b", Address: "192.0.2.12", GRPCPort: 50051}
	if err := st.CreateNode(nodeA); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(nodeB); err != nil {
		t.Fatal(err)
	}
	inA := &store.InboundConfig{Name: "rp-ia", Protocol: "shadowsocks", Enabled: true, Params: map[string]any{
		"port": 13001, "method": "aes-128-gcm", "password": "pw-a",
	}}
	inB := &store.InboundConfig{Name: "rp-ib", Protocol: "shadowsocks", Enabled: true, Params: map[string]any{
		"port": 13002, "method": "aes-128-gcm", "password": "pw-b",
	}}
	if err := st.CreateInbound(inA); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInbound(inB); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeInbounds(nodeA.ID, []string{inA.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeInbounds(nodeB.ID, []string{inB.ID}); err != nil {
		t.Fatal(err)
	}
	chain := &store.ProxyChain{
		Name: "rp-chain",
		Hops: []store.ProxyChainHop{
			{NodeID: nodeA.ID, InboundID: inA.ID},
			{NodeID: nodeB.ID, InboundID: inB.ID},
		},
	}
	if err := st.CreateProxyChain(chain); err != nil {
		t.Fatal(err)
	}
	return chain
}

func TestRoutePlanAPICRUD(t *testing.T) {
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()
	chain := setupChainRefs(t, st)
	if err := st.SetProxyChainEnabled(chain.ID, true); err != nil {
		t.Fatal(err)
	}

	createBody := map[string]any{
		"name":  "全局分流",
		"scope": "global",
		"rules": []map[string]any{
			{"match_type": "domain_suffix", "match_value": "corp.example", "action": "proxy", "target_chain_id": chain.ID},
			{"match_type": "ip_cidr", "match_value": "203.0.113.0/24", "action": "block", "enabled": false},
		},
	}
	response, payload := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/route-plans", createBody)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, payload = %v", response.StatusCode, payload)
	}
	planID, _ := payload["id"].(string)
	if planID == "" || payload["name"] != "全局分流" || payload["scope"] != "global" ||
		payload["enabled"] != true || payload["subscription_id"] != "" {
		t.Fatalf("payload = %v", payload)
	}
	rules, ok := payload["rules"].([]any)
	if !ok || len(rules) != 2 {
		t.Fatalf("rules = %v", payload["rules"])
	}
	first := rules[0].(map[string]any)
	if first["position"] != float64(0) || first["match_type"] != "domain_suffix" ||
		first["action"] != "proxy" || first["target_chain_id"] != chain.ID || first["enabled"] != true {
		t.Fatalf("rule[0] = %v", first)
	}
	second := rules[1].(map[string]any)
	if second["position"] != float64(1) || second["enabled"] != false || second["target_chain_id"] != "" {
		t.Fatalf("rule[1] = %v", second)
	}
	if _, ok := payload["created_at_unix"].(float64); !ok {
		t.Fatalf("payload = %v", payload)
	}

	// List.
	response, payload = doJSON(t, client, http.MethodGet, ts.URL+"/api/v1/route-plans", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", response.StatusCode)
	}
	if list, _ := payload["_array"].([]any); len(list) != 1 {
		t.Fatalf("list = %v", payload)
	}

	// Get.
	response, payload = doJSON(t, client, http.MethodGet, ts.URL+"/api/v1/route-plans/"+planID, nil)
	if response.StatusCode != http.StatusOK || payload["id"] != planID {
		t.Fatalf("get status = %d, payload = %v", response.StatusCode, payload)
	}

	// Update scalars + full rule replacement (positions resequenced).
	response, payload = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/route-plans/"+planID, map[string]any{
		"name":       "改名",
		"sort_order": 3,
		"rules": []map[string]any{
			{"match_type": "domain_keyword", "match_value": "kw", "action": "block"},
			{"match_type": "domain", "match_value": "direct.example", "action": "direct"},
		},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d, payload = %v", response.StatusCode, payload)
	}
	if payload["name"] != "改名" || payload["sort_order"] != float64(3) {
		t.Fatalf("payload = %v", payload)
	}
	rules, _ = payload["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("rules = %v", payload["rules"])
	}
	for i, item := range rules {
		rule := item.(map[string]any)
		if rule["position"] != float64(i) {
			t.Fatalf("rule %d position = %v", i, rule["position"])
		}
	}
	if rules[0].(map[string]any)["match_value"] != "kw" {
		t.Fatalf("rules = %v", rules)
	}

	// Delete.
	response, payload = doJSON(t, client, http.MethodDelete, ts.URL+"/api/v1/route-plans/"+planID, nil)
	if response.StatusCode != http.StatusOK || payload["ok"] != true {
		t.Fatalf("delete status = %d, payload = %v", response.StatusCode, payload)
	}
	response, _ = doJSON(t, client, http.MethodGet, ts.URL+"/api/v1/route-plans/"+planID, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("get-after-delete status = %d", response.StatusCode)
	}
}

func TestRoutePlanAPIValidation(t *testing.T) {
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()
	disabledChain := setupChainRefs(t, st)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing name", map[string]any{"scope": "global"}},
		{"bad scope", map[string]any{"name": "x", "scope": "weird"}},
		{"global with subscription_id", map[string]any{
			"name": "x", "scope": "global", "subscription_id": "whatever"}},
		{"subscription without subscription_id", map[string]any{
			"name": "x", "scope": "subscription"}},
		{"subscription with unknown id", map[string]any{
			"name": "x", "scope": "subscription", "subscription_id": "missing"}},
		{"invalid cidr", map[string]any{
			"name": "x", "rules": []map[string]any{
				{"match_type": "ip_cidr", "match_value": "not-a-cidr", "action": "direct"}}}},
		{"empty match_value", map[string]any{
			"name": "x", "rules": []map[string]any{
				{"match_type": "domain", "match_value": "  ", "action": "direct"}}}},
		{"bad match_type", map[string]any{
			"name": "x", "rules": []map[string]any{
				{"match_type": "geoip", "match_value": "cn", "action": "direct"}}}},
		{"bad action", map[string]any{
			"name": "x", "rules": []map[string]any{
				{"match_type": "domain", "match_value": "a.example", "action": "reject"}}}},
		{"proxy without chain", map[string]any{
			"name": "x", "rules": []map[string]any{
				{"match_type": "domain", "match_value": "a.example", "action": "proxy"}}}},
		{"proxy dangling chain", map[string]any{
			"name": "x", "rules": []map[string]any{
				{"match_type": "domain", "match_value": "a.example", "action": "proxy", "target_chain_id": "missing"}}}},
		{"proxy disabled chain", map[string]any{
			"name": "x", "rules": []map[string]any{
				{"match_type": "domain", "match_value": "a.example", "action": "proxy", "target_chain_id": disabledChain.ID}}}},
	}
	for _, tc := range cases {
		response, payload := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/route-plans", tc.body)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, payload = %v", tc.name, response.StatusCode, payload)
		}
		if payload["error"] == nil || payload["error"] == "" {
			t.Fatalf("%s: missing error message: %v", tc.name, payload)
		}
	}
}

func TestRoutePlanSubscriptionBinding(t *testing.T) {
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()

	sub := &store.Subscription{Name: "bind-sub", Token: "bind-token", Enabled: true}
	if err := st.CreateSubscription(sub); err != nil {
		t.Fatal(err)
	}
	response, payload := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/route-plans", map[string]any{
		"name": "个人计划", "scope": "subscription", "subscription_id": sub.ID,
		"rules": []map[string]any{
			{"match_type": "process_name", "match_value": "telegram", "action": "direct"},
		},
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, payload = %v", response.StatusCode, payload)
	}
	planID := payload["id"].(string)
	if payload["subscription_id"] != sub.ID {
		t.Fatalf("payload = %v", payload)
	}

	// Bind the plan to the subscription.
	response, payload = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/subscriptions/"+sub.ID,
		map[string]any{"route_plan_id": planID})
	if response.StatusCode != http.StatusOK || payload["route_plan_id"] != planID {
		t.Fatalf("bind status = %d, payload = %v", response.StatusCode, payload)
	}

	// Global plans cannot be bound.
	globalPlan := &store.RoutePlan{Name: "g", Scope: "global", Enabled: true}
	if err := st.CreateRoutePlan(globalPlan); err != nil {
		t.Fatal(err)
	}
	response, _ = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/subscriptions/"+sub.ID,
		map[string]any{"route_plan_id": globalPlan.ID})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bind-global status = %d", response.StatusCode)
	}
	// Dangling plan IDs are rejected.
	response, _ = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/subscriptions/"+sub.ID,
		map[string]any{"route_plan_id": "missing"})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bind-missing status = %d", response.StatusCode)
	}
	// Unbind.
	response, payload = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/subscriptions/"+sub.ID,
		map[string]any{"route_plan_id": ""})
	if response.StatusCode != http.StatusOK || payload["route_plan_id"] != "" {
		t.Fatalf("unbind status = %d, payload = %v", response.StatusCode, payload)
	}

	// Deleting the bound subscription cascades the plan.
	if err := st.DeleteSubscription(sub.ID); err != nil {
		t.Fatal(err)
	}
	response, _ = doJSON(t, client, http.MethodGet, ts.URL+"/api/v1/route-plans/"+planID, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("cascade status = %d", response.StatusCode)
	}
}

func prepareRenderableSubscription(t *testing.T, st *store.Store) *store.Subscription {
	t.Helper()
	node := &store.Node{Name: "tok-node", Address: "192.0.2.20", GRPCPort: 50051}
	if err := st.CreateNode(node); err != nil {
		t.Fatal(err)
	}
	inbound := &store.InboundConfig{Name: "tok-ss", Protocol: "shadowsocks", Enabled: true, Params: map[string]any{
		"port": 14001, "method": "aes-128-gcm", "password": "pw-tok",
	}}
	if err := st.CreateInbound(inbound); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeInbounds(node.ID, []string{inbound.ID}); err != nil {
		t.Fatal(err)
	}
	sub := &store.Subscription{
		Name: "tok-sub", Token: "old-token-0001", Enabled: true,
		IncludeAllInbounds: true, IncludeStandalone: true,
	}
	if err := st.CreateSubscription(sub); err != nil {
		t.Fatal(err)
	}
	return sub
}

func subStatus(t *testing.T, client *http.Client, url string) int {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestSubscriptionPreviewIncludesPlanRules(t *testing.T) {
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()
	sub := prepareRenderableSubscription(t, st)
	plan := &store.RoutePlan{
		Name: "preview-plan", Scope: "subscription", SubscriptionID: sub.ID, Enabled: true,
		Rules: []store.RoutePlanRule{
			{MatchType: "process_name", MatchValue: "telegram", Action: "proxy", Enabled: true},
			{MatchType: "domain_suffix", MatchValue: "internal.example", Action: "block", Enabled: true},
		},
	}
	if err := st.CreateRoutePlan(plan); err != nil {
		t.Fatal(err)
	}
	sub.RoutePlanID = plan.ID
	if err := st.UpdateSubscription(sub); err != nil {
		t.Fatal(err)
	}

	preview, err := client.Get(ts.URL + "/api/v1/subscriptions/" + sub.ID + "/preview?flag=clash")
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Body.Close()
	raw, err := io.ReadAll(preview.Body)
	if err != nil {
		t.Fatal(err)
	}
	if preview.StatusCode != http.StatusOK {
		t.Fatalf("clash preview status = %d: %s", preview.StatusCode, raw)
	}
	text := string(raw)
	if !strings.Contains(text, "PROCESS-NAME,telegram,") ||
		!strings.Contains(text, "DOMAIN-SUFFIX,internal.example,REJECT") {
		t.Fatalf("clash preview missing plan rules:\n%s", text)
	}

	preview2, err := client.Get(ts.URL + "/api/v1/subscriptions/" + sub.ID + "/preview?flag=singbox")
	if err != nil {
		t.Fatal(err)
	}
	defer preview2.Body.Close()
	raw, err = io.ReadAll(preview2.Body)
	if err != nil {
		t.Fatal(err)
	}
	if preview2.StatusCode != http.StatusOK {
		t.Fatalf("singbox preview status = %d: %s", preview2.StatusCode, raw)
	}
	text = string(raw)
	if !strings.Contains(text, `"process_name"`) || !strings.Contains(text, `"reject"`) {
		t.Fatalf("singbox preview missing plan rules:\n%s", text)
	}
}

func TestSubscriptionTokenRotateAndDisable(t *testing.T) {
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()
	sub := prepareRenderableSubscription(t, st)

	if status := subStatus(t, client, ts.URL+"/sub/old-token-0001"); status != http.StatusOK {
		t.Fatalf("initial /sub status = %d", status)
	}
	if subStatus(t, client, ts.URL+"/sub/no-such-token") != http.StatusNotFound {
		t.Fatal("unknown token must be 404")
	}

	// Rotate: old link dies immediately, new link works.
	response, payload := doJSON(t, client, http.MethodPost,
		ts.URL+"/api/v1/subscriptions/"+sub.ID+"/token/rotate", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("rotate status = %d, payload = %v", response.StatusCode, payload)
	}
	newToken, _ := payload["token"].(string)
	if newToken == "" || newToken == "old-token-0001" {
		t.Fatalf("token = %q", newToken)
	}
	if status := subStatus(t, client, ts.URL+"/sub/old-token-0001"); status != http.StatusNotFound {
		t.Fatalf("old token status = %d", status)
	}
	if status := subStatus(t, client, ts.URL+"/sub/"+newToken); status != http.StatusOK {
		t.Fatalf("new token status = %d", status)
	}

	// Disable: public link answers 404 like an unknown token.
	response, payload = doJSON(t, client, http.MethodPost,
		ts.URL+"/api/v1/subscriptions/"+sub.ID+"/disable", nil)
	if response.StatusCode != http.StatusOK || payload["ok"] != true {
		t.Fatalf("disable status = %d, payload = %v", response.StatusCode, payload)
	}
	if status := subStatus(t, client, ts.URL+"/sub/"+newToken); status != http.StatusNotFound {
		t.Fatalf("disabled status = %d", status)
	}
	// The disabled flag is exposed on the subscription object.
	response, payload = doJSON(t, client, http.MethodGet, ts.URL+"/api/v1/subscriptions", nil)
	list, _ := payload["_array"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["disabled"] != true {
		t.Fatalf("list = %v", payload)
	}

	// Enable again.
	response, payload = doJSON(t, client, http.MethodPost,
		ts.URL+"/api/v1/subscriptions/"+sub.ID+"/enable", nil)
	if response.StatusCode != http.StatusOK || payload["ok"] != true {
		t.Fatalf("enable status = %d, payload = %v", response.StatusCode, payload)
	}
	if status := subStatus(t, client, ts.URL+"/sub/"+newToken); status != http.StatusOK {
		t.Fatalf("re-enabled status = %d", status)
	}
}
