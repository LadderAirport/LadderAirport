package proxyuser

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ladderairport/panel/internal/store"
	"github.com/ladderairport/panel/internal/subscription"
)

func testUser(t *testing.T, id string) store.ProxyUser {
	t.Helper()
	secret, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	return store.ProxyUser{ID: id, Name: "name-" + id, Enabled: true, Secret: secret}
}

func identity(t *testing.T, u store.ProxyUser) Identity {
	t.Helper()
	id, err := ForUser(u)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDerivedCredentialsStableAndDistinct(t *testing.T) {
	a := identity(t, testUser(t, "a"))
	b := identity(t, testUser(t, "b"))
	again, _ := ForUser(store.ProxyUser{ID: "a", Secret: hex.EncodeToString(a.Secret)})
	if a.UUID() != again.UUID() || a.Password() != again.Password() {
		t.Fatal("derivation must be deterministic")
	}
	if a.UUID() == b.UUID() || a.Password() == b.Password() {
		t.Fatal("different secrets must derive different credentials")
	}
	u, err := uuid.Parse(a.UUID())
	if err != nil || u.Version() != 4 || u.Variant() != uuid.RFC4122 {
		t.Fatalf("uuid %q: version=%v variant=%v err=%v", a.UUID(), u.Version(), u.Variant(), err)
	}
	for method, n := range map[string]int{"2022-blake3-aes-128-gcm": 16, "2022-blake3-aes-256-gcm": 32} {
		key, err := a.SSKey(method)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := base64.StdEncoding.DecodeString(key)
		if err != nil || len(raw) != n {
			t.Fatalf("%s key len=%d err=%v", method, len(raw), err)
		}
	}
	if _, err := a.SSKey("aes-128-gcm"); err == nil {
		t.Fatal("legacy SS must not derive a multi-user key")
	}
}

func TestSupportsMatrix(t *testing.T) {
	cases := []struct {
		in   store.InboundConfig
		want bool
	}{
		{store.InboundConfig{Protocol: "vless"}, true},
		{store.InboundConfig{Protocol: "vmess"}, true},
		{store.InboundConfig{Protocol: "trojan"}, true},
		{store.InboundConfig{Protocol: "hysteria2"}, true},
		{store.InboundConfig{Protocol: "tuic"}, true},
		{store.InboundConfig{Protocol: "anytls"}, true},
		{store.InboundConfig{Protocol: "http", Params: map[string]any{"auth_mode": "password"}}, true},
		{store.InboundConfig{Protocol: "socks5", Params: map[string]any{}}, true}, // default auth_mode = password
		{store.InboundConfig{Protocol: "auto", Params: map[string]any{"auth_mode": "none"}}, false},
		{store.InboundConfig{Protocol: "shadowsocks", Params: map[string]any{"method": "2022-blake3-aes-256-gcm"}}, true},
		{store.InboundConfig{Protocol: "shadowsocks", Params: map[string]any{"method": "2022-blake3-chacha20-poly1305"}}, false},
		{store.InboundConfig{Protocol: "shadowsocks", Params: map[string]any{"method": "aes-128-gcm"}}, false},
	}
	for _, c := range cases {
		if got, reason := Supports(c.in); got != c.want {
			t.Errorf("%s %v: got %v (%s)", c.in.Protocol, c.in.Params, got, reason)
		}
	}
	ss := store.InboundConfig{Protocol: "shadowsocks", Params: map[string]any{"method": "2022-blake3-aes-128-gcm"}}
	if UsesUsers(ss, false) || !UsesUsers(ss, true) {
		t.Fatal("SS must switch to multi-user only when admission-only")
	}
}

func TestBuildPlanFollowsSubscriptionSelection(t *testing.T) {
	alice, bob, carol, dave := testUser(t, "alice"), testUser(t, "bob"), testUser(t, "carol"), testUser(t, "dave")
	dave.Enabled = false
	chains := []store.ProxyChain{
		{ID: "c1", Enabled: true, Hops: []store.ProxyChainHop{{InboundID: "hop1"}, {InboundID: "hop2"}}},
		{ID: "c2", Enabled: false, Hops: []store.ProxyChainHop{{InboundID: "hop3"}}},
	}
	subs := []store.Subscription{
		{UserID: "alice", Enabled: true, IncludeStandalone: true, InboundIDs: []string{"in1"}},
		{UserID: "bob", Enabled: true, IncludeStandalone: true, IncludeAllInbounds: true},
		{UserID: "carol", Enabled: true, ChainIDs: []string{"c1", "c2"}},
		{UserID: "carol", Enabled: true, Disabled: true, IncludeStandalone: true, InboundIDs: []string{"in1"}},
		{UserID: "dave", Enabled: true, IncludeStandalone: true, IncludeAllInbounds: true},
		{UserID: "", Enabled: true, IncludeStandalone: true, IncludeAllInbounds: true},
	}
	plan, err := BuildPlan([]store.ProxyUser{alice, bob, carol, dave}, subs, chains)
	if err != nil {
		t.Fatal(err)
	}
	names := func(inboundID string) string {
		var out []string
		for _, id := range plan.UsersFor(inboundID) {
			out = append(out, id.Name)
		}
		return strings.Join(out, ",")
	}
	for inbound, want := range map[string]string{
		"in1":  "alice,bob",
		"in2":  "bob",
		"hop1": "bob,carol",
		"hop2": "bob,carol",
		"hop3": "bob", // c2 is disabled
	} {
		if got := names(inbound); got != want {
			t.Errorf("%s: got %q want %q", inbound, got, want)
		}
	}
}

func TestServerUsersAdmissionOnly(t *testing.T) {
	alice := identity(t, testUser(t, "alice"))
	secret, _ := NewSecret()
	vless := store.InboundConfig{Name: "v", Protocol: "vless", Params: map[string]any{"uuid": "x", "flow": "xtls-rprx-vision"}}

	res, ok, err := ServerUsers(vless, store.NodeAdmission{}, nil)
	if err != nil || ok {
		t.Fatalf("no users, not admission-only: ok=%v err=%v", ok, err)
	}
	res, ok, err = ServerUsers(vless, store.NodeAdmission{}, []Identity{alice})
	if err != nil || !ok || res.ReplaceDefault || len(res.Users) != 1 {
		t.Fatalf("shared mode: %+v ok=%v err=%v", res, ok, err)
	}
	if res.Users[0]["uuid"] != alice.UUID() || res.Users[0]["flow"] != "xtls-rprx-vision" || res.Users[0]["name"] != "alice" {
		t.Fatalf("user entry: %v", res.Users[0])
	}

	node := store.NodeAdmission{NodeID: "n", AdmissionOnly: true, InternalSecret: secret}
	res, ok, err = ServerUsers(vless, node, []Identity{alice})
	if err != nil || !ok || !res.ReplaceDefault || len(res.Users) != 2 || res.Users[0]["name"] != InternalUserName {
		t.Fatalf("admission-only: %+v ok=%v err=%v", res, ok, err)
	}
	// Admission-only with nobody admitted still keeps the internal user, so
	// the inbound never ends up accepting the shared credential.
	res, ok, err = ServerUsers(vless, node, nil)
	if err != nil || !ok || len(res.Users) != 1 {
		t.Fatalf("admission-only empty: %+v ok=%v err=%v", res, ok, err)
	}

	// Inbounds that cannot distinguish users stay shared, even on an
	// admission-only node.
	legacySS := store.InboundConfig{Name: "ss", Protocol: "shadowsocks", Params: map[string]any{"method": "aes-128-gcm"}}
	openHTTP := store.InboundConfig{Name: "h", Protocol: "http", Params: map[string]any{"auth_mode": "none"}}
	for _, in := range []store.InboundConfig{legacySS, openHTTP} {
		for _, n := range []store.NodeAdmission{{}, node} {
			if _, ok, err := ServerUsers(in, n, []Identity{alice}); ok || err != nil {
				t.Fatalf("%s on %+v must stay untouched: ok=%v err=%v", in.Protocol, n, ok, err)
			}
		}
	}
}

func TestForSubscription(t *testing.T) {
	alice := identity(t, testUser(t, "alice"))
	secret, _ := NewSecret()
	onNode := func(ep subscription.ProxyEndpoint, nodeID string) subscription.ProxyEndpoint {
		ep.Node = store.Node{ID: nodeID}
		return ep
	}
	trojanTpl := subscription.ProxyEndpoint{
		Inbound: store.InboundConfig{ID: "t", Protocol: "trojan"},
		Params:  map[string]any{"password": "shared"},
	}
	ssTpl := subscription.ProxyEndpoint{
		Inbound: store.InboundConfig{ID: "s", Protocol: "shadowsocks", Params: map[string]any{"method": "2022-blake3-aes-128-gcm"}},
		Params:  map[string]any{"method": "2022-blake3-aes-128-gcm", "password": "PSK"},
	}
	legacyTpl := subscription.ProxyEndpoint{
		Inbound: store.InboundConfig{ID: "l", Protocol: "shadowsocks", Params: map[string]any{"method": "aes-128-gcm"}},
		Params:  map[string]any{"method": "aes-128-gcm", "password": "legacy"},
	}
	admissions := map[string]store.NodeAdmission{
		"locked": {NodeID: "locked", AdmissionOnly: true, InternalSecret: secret},
	}

	// The same inbound template behaves per node.
	if ep, keep, _ := ForSubscription(onNode(trojanTpl, "open"), nil, admissions); !keep || ep.Params["password"] != "shared" {
		t.Fatalf("shared sub on open node: keep=%v %v", keep, ep.Params)
	}
	if _, keep, _ := ForSubscription(onNode(trojanTpl, "locked"), nil, admissions); keep {
		t.Fatal("shared sub must omit inbounds on admission-only nodes")
	}
	ep, keep, err := ForSubscription(onNode(trojanTpl, "open"), &alice, admissions)
	if err != nil || !keep || ep.Params["password"] != alice.Password() {
		t.Fatalf("user sub: keep=%v %v err=%v", keep, ep.Params, err)
	}
	if trojanTpl.Params["password"] != "shared" {
		t.Fatal("ForSubscription must not mutate the shared params map")
	}
	// SS2022 switches to "server-key:user-key" only on admission-only nodes.
	if ep, _, _ := ForSubscription(onNode(ssTpl, "open"), &alice, admissions); ep.Params["password"] != "PSK" {
		t.Fatalf("SS on open node: %v", ep.Params)
	}
	key, _ := alice.SSKey("2022-blake3-aes-128-gcm")
	if ep, keep, _ := ForSubscription(onNode(ssTpl, "locked"), &alice, admissions); !keep || ep.Params["password"] != "PSK:"+key {
		t.Fatalf("SS on locked node: keep=%v %v", keep, ep.Params)
	}
	// Unsupported inbounds stay shared on locked nodes, for every subscription.
	for _, user := range []*Identity{nil, &alice} {
		if ep, keep, _ := ForSubscription(onNode(legacyTpl, "locked"), user, admissions); !keep || ep.Params["password"] != "legacy" {
			t.Fatalf("legacy SS on locked node (user=%v): keep=%v %v", user != nil, keep, ep.Params)
		}
	}
}

func TestInternalEndpoint(t *testing.T) {
	secret, _ := NewSecret()
	ep := subscription.ProxyEndpoint{
		Node:    store.Node{ID: "n"},
		Inbound: store.InboundConfig{ID: "v", Protocol: "vless"},
		Params:  map[string]any{"uuid": "shared"},
	}
	out, err := InternalEndpoint(ep, map[string]store.NodeAdmission{})
	if err != nil || out.Params["uuid"] != "shared" {
		t.Fatalf("%v %v", out.Params, err)
	}
	adm := store.NodeAdmission{NodeID: "n", AdmissionOnly: true, InternalSecret: secret}
	internal, _ := Internal(adm)
	out, err = InternalEndpoint(ep, map[string]store.NodeAdmission{"n": adm})
	if err != nil || out.Params["uuid"] != internal.UUID() {
		t.Fatalf("%v %v", out.Params, err)
	}
}
