package api_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ladderairport/panel/internal/store"
	"gopkg.in/yaml.v3"
)

func fetchSub(t *testing.T, client *http.Client, url string) []byte {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", url, resp.StatusCode, raw)
	}
	return raw
}

func TestSubscriptionComposesChainClientSide(t *testing.T) {
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()
	chain := setupChainRefs(t, st)
	sub := &store.Subscription{
		Name: "chain-sub", Token: "chain-token-0001", Enabled: true,
		IncludeAllInbounds: true, IncludeStandalone: true, IncludeAllChains: true,
	}
	if err := st.CreateSubscription(sub); err != nil {
		t.Fatal(err)
	}

	// Disabled chains are not rendered.
	if strings.Contains(string(fetchSub(t, client, ts.URL+"/sub/"+sub.Token+"?flag=clash")), "rp-chain") {
		t.Fatal("disabled chain rendered")
	}
	response, payload := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/proxy-chains/"+chain.ID+"/enable", nil)
	if response.StatusCode != http.StatusOK || payload["enabled"] != true {
		t.Fatalf("enable = %d %v", response.StatusCode, payload)
	}

	var clash struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []struct {
			Name    string   `yaml:"name"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(fetchSub(t, client, ts.URL+"/sub/"+sub.Token+"?flag=clash"), &clash); err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, p := range clash.Proxies {
		byName[p["name"].(string)] = p
	}
	// Both hops share host:port with the standalone endpoints; they must
	// survive dedupe because chains are appended after merge.
	entry, exit := byName["rp-chain-跳1"], byName["rp-chain"]
	if entry == nil || exit == nil || len(clash.Proxies) != 4 {
		t.Fatalf("proxies = %v", clash.Proxies)
	}
	if entry["server"] != "192.0.2.11" || entry["dialer-proxy"] != nil {
		t.Fatalf("entry hop = %v", entry)
	}
	if exit["server"] != "192.0.2.12" || exit["dialer-proxy"] != "rp-chain-跳1" {
		t.Fatalf("exit hop = %v", exit)
	}
	foundChainGroup := false
	for _, g := range clash.Groups {
		for _, member := range g.Proxies {
			if member == "rp-chain-跳1" {
				t.Fatalf("intermediate hop must not be selectable: group %s", g.Name)
			}
		}
		if g.Name == "链式代理" {
			foundChainGroup = len(g.Proxies) == 1 && g.Proxies[0] == "rp-chain"
		}
	}
	if !foundChainGroup {
		t.Fatalf("groups = %+v", clash.Groups)
	}

	var singbox struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(fetchSub(t, client, ts.URL+"/sub/"+sub.Token+"?flag=singbox"), &singbox); err != nil {
		t.Fatal(err)
	}
	var selector, sbExit map[string]any
	for _, ob := range singbox.Outbounds {
		switch ob["tag"] {
		case "proxy":
			selector = ob
		case "rp-chain":
			sbExit = ob
		}
	}
	if sbExit == nil || sbExit["detour"] != "rp-chain-跳1" {
		t.Fatalf("sing-box exit = %v", sbExit)
	}
	for _, tag := range selector["outbounds"].([]any) {
		if tag == "rp-chain-跳1" {
			t.Fatalf("selector exposes intermediate hop: %v", selector)
		}
	}

	links, err := base64.StdEncoding.DecodeString(string(fetchSub(t, client, ts.URL+"/sub/"+sub.Token+"?flag=v2ray")))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(links)), "\n"); len(lines) != 2 || strings.Contains(string(links), "rp-chain") {
		t.Fatalf("share links must only contain standalone endpoints: %s", links)
	}
}

func TestProxyChainUpdateKeepsEnabledAndRequiresBindings(t *testing.T) {
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()
	chain := setupChainRefs(t, st)
	if err := st.SetProxyChainEnabled(chain.ID, true); err != nil {
		t.Fatal(err)
	}
	hops := []map[string]any{
		{"node_id": chain.Hops[0].NodeID, "inbound_id": chain.Hops[0].InboundID},
		{"node_id": chain.Hops[1].NodeID, "inbound_id": chain.Hops[1].InboundID, "dial_address": "10.0.0.12"},
	}
	response, payload := doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/proxy-chains/"+chain.ID,
		map[string]any{"name": "renamed", "enabled": false, "hops": hops})
	if response.StatusCode != http.StatusOK || payload["name"] != "renamed" || payload["enabled"] != true {
		t.Fatalf("update = %d %v", response.StatusCode, payload)
	}

	// Swapping in an inbound that is not bound to the node is rejected.
	hops[1]["inbound_id"] = chain.Hops[0].InboundID
	response, payload = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/proxy-chains/"+chain.ID,
		map[string]any{"name": "renamed", "hops": hops})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unbound hop update = %d %v", response.StatusCode, payload)
	}

	// Disabling an inbound used by an enabled chain is blocked.
	response, payload = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/inbounds/"+chain.Hops[0].InboundID,
		map[string]any{"enabled": false})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("disable hop inbound = %d %v", response.StatusCode, payload)
	}
}
