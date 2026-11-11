package nodeconfig

import (
	"encoding/json"
	"testing"

	"github.com/ladderairport/panel/internal/proxyuser"
	"github.com/ladderairport/panel/internal/store"
)

func addVLESSNode(t *testing.T, st *store.Store, name, address string, port int) (store.Node, store.InboundConfig) {
	t.Helper()
	node := &store.Node{Name: name, Address: address, GRPCPort: 50051, Status: "online"}
	if err := st.CreateNode(node); err != nil {
		t.Fatal(err)
	}
	inbound := &store.InboundConfig{
		Name: "vless-" + name, Protocol: "vless", Enabled: true,
		Params: map[string]any{"port": port, "uuid": "11111111-1111-4111-8111-111111111111"},
	}
	if err := st.CreateInbound(inbound); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeInbounds(node.ID, []string{inbound.ID}); err != nil {
		t.Fatal(err)
	}
	return *node, *inbound
}

func addUserWithSub(t *testing.T, st *store.Store, name string, inboundIDs ...string) store.ProxyUser {
	t.Helper()
	secret, err := proxyuser.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	u := &store.ProxyUser{Name: name, Enabled: true, Secret: secret}
	if err := st.CreateProxyUser(u); err != nil {
		t.Fatal(err)
	}
	sub := &store.Subscription{
		Name: "sub-" + name, Token: "tok-" + name, UserID: u.ID, Enabled: true,
		IncludeStandalone: true, InboundIDs: inboundIDs,
	}
	if err := st.CreateSubscription(sub); err != nil {
		t.Fatal(err)
	}
	return *u
}

func inboundUserNames(t *testing.T, raw string) []string {
	t.Helper()
	var cfg struct {
		Inbounds []struct {
			Users []map[string]any `json:"users"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, u := range cfg.Inbounds[0].Users {
		names = append(names, u["name"].(string))
	}
	return names
}

func TestBuildAddsAdmittedUsers(t *testing.T) {
	st := openRouteRuleStore(t)
	node, in := addVLESSNode(t, st, "a", "192.0.2.1", 13001)
	_, other := addVLESSNode(t, st, "b", "192.0.2.2", 13002)
	alice := addUserWithSub(t, st, "alice", in.ID)
	addUserWithSub(t, st, "bob", other.ID) // not admitted on node a

	builder := &Builder{Store: st}
	result, err := builder.Build(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := inboundUserNames(t, result.JSON)
	if len(names) != 2 || names[0] != "default" || names[1] != alice.ID {
		t.Fatalf("shared mode users = %v", names)
	}

	// An admission-only node drops the shared credential, keeps the internal one.
	secret, _ := proxyuser.NewSecret()
	if _, err := st.SetNodeAdmission(node.ID, true, secret); err != nil {
		t.Fatal(err)
	}
	result2, err := builder.Build(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	names = inboundUserNames(t, result2.JSON)
	if len(names) != 2 || names[0] != proxyuser.InternalUserName || names[1] != alice.ID {
		t.Fatalf("admission-only users = %v", names)
	}
	if result2.Hash == result.Hash {
		t.Fatal("hash must change so the node is re-pushed")
	}

	// Disabling the user removes it from the node.
	alice.Enabled = false
	if err := st.UpdateProxyUser(&alice); err != nil {
		t.Fatal(err)
	}
	result3, err := builder.Build(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if names = inboundUserNames(t, result3.JSON); len(names) != 1 || names[0] != proxyuser.InternalUserName {
		t.Fatalf("disabled user still admitted: %v", names)
	}
}

func TestNodeSideChainUsesInternalCredential(t *testing.T) {
	st := openRouteRuleStore(t)
	nodeA, inA := addVLESSNode(t, st, "a", "192.0.2.1", 13101)
	nodeB, inB := addVLESSNode(t, st, "b", "192.0.2.2", 13102)
	chain := &store.ProxyChain{
		Name: "c", Hops: []store.ProxyChainHop{{NodeID: nodeA.ID, InboundID: inA.ID}, {NodeID: nodeB.ID, InboundID: inB.ID}},
	}
	if err := st.CreateProxyChain(chain); err != nil {
		t.Fatal(err)
	}
	chain.Enabled = true
	secret, _ := proxyuser.NewSecret()
	adm, err := st.SetNodeAdmission(nodeB.ID, true, secret)
	if err != nil {
		t.Fatal(err)
	}
	internal, _ := proxyuser.Internal(*adm)
	outbounds, _, err := (&Builder{Store: st}).composeChainFrom(*chain, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(outbounds) != 1 || outbounds[0]["uuid"] != internal.UUID() {
		t.Fatalf("hop into admission-only inbound must use the internal credential: %v", outbounds)
	}
}

// The same inbound template is admission-only on one node and shared on
// another; unsupported inbounds stay shared on the admission-only node.
func TestNodeAdmissionIsPerNode(t *testing.T) {
	st := openRouteRuleStore(t)
	locked, in := addVLESSNode(t, st, "locked", "192.0.2.1", 13201)
	open := &store.Node{Name: "open", Address: "192.0.2.2", GRPCPort: 50051, Status: "online"}
	if err := st.CreateNode(open); err != nil {
		t.Fatal(err)
	}
	legacy := &store.InboundConfig{
		Name: "legacy-ss", Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{"port": 13202, "method": "aes-128-gcm", "password": "legacy"},
	}
	if err := st.CreateInbound(legacy); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeInbounds(locked.ID, []string{in.ID, legacy.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeInbounds(open.ID, []string{in.ID}); err != nil {
		t.Fatal(err)
	}
	secret, _ := proxyuser.NewSecret()
	if _, err := st.SetNodeAdmission(locked.ID, true, secret); err != nil {
		t.Fatal(err)
	}
	builder := &Builder{Store: st}
	inboundsOf := func(nodeID string) []map[string]any {
		t.Helper()
		result, err := builder.Build(nodeID)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Inbounds []map[string]any `json:"inbounds"`
		}
		if err := json.Unmarshal([]byte(result.JSON), &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg.Inbounds
	}
	firstUser := func(inbound map[string]any) string {
		users, _ := inbound["users"].([]any)
		if len(users) == 0 {
			return ""
		}
		return users[0].(map[string]any)["name"].(string)
	}
	for _, inbound := range inboundsOf(locked.ID) {
		switch inbound["type"] {
		case "vless":
			if firstUser(inbound) != proxyuser.InternalUserName {
				t.Fatalf("locked vless users = %v", inbound["users"])
			}
		case "shadowsocks":
			if inbound["password"] != "legacy" || inbound["users"] != nil {
				t.Fatalf("legacy SS must stay shared on locked node: %v", inbound)
			}
		}
	}
	if got := firstUser(inboundsOf(open.ID)[0]); got != "default" {
		t.Fatalf("open node must keep the shared credential, first user = %q", got)
	}
}
