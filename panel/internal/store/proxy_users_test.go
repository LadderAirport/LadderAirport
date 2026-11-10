package store

import "testing"

// Every admission-affecting edit must move the node config fingerprint, or the
// bootstrap retry loop would never re-push nodes that missed the debounced push.
func TestNodeConfigFingerprintTracksAdmission(t *testing.T) {
	s := openTestStore(t)
	node := &Node{Name: "edge", Address: "192.0.2.1", GRPCPort: 50051}
	if err := s.CreateNode(node); err != nil {
		t.Fatal(err)
	}
	inbound := &InboundConfig{
		Name: "vless", Protocol: "vless", Enabled: true,
		Params: map[string]any{"port": 443, "uuid": "11111111-1111-4111-8111-111111111111"},
	}
	if err := s.CreateInbound(inbound); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeInbounds(node.ID, []string{inbound.ID}); err != nil {
		t.Fatal(err)
	}
	last, err := s.NodeConfigFingerprint(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	step := func(name string, mutate func()) {
		t.Helper()
		mutate() // all edits run within the same second on purpose
		next, err := s.NodeConfigFingerprint(node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if next == last {
			t.Fatalf("%s did not move the fingerprint", name)
		}
		last = next
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	u := &ProxyUser{Name: "alice", Enabled: true, Secret: "00112233445566778899aabbccddeeff"}
	step("create user", func() { must(s.CreateProxyUser(u)) })
	sub := &Subscription{Name: "s", Token: "t-0001", Enabled: true, IncludeStandalone: true, IncludeAllInbounds: true}
	must(s.CreateSubscription(sub))
	step("bind subscription", func() { sub.UserID = u.ID; must(s.UpdateSubscription(sub)) })
	step("disable subscription", func() { sub.Disabled = true; must(s.UpdateSubscription(sub)) })
	step("enable subscription", func() { sub.Disabled = false; must(s.UpdateSubscription(sub)) })
	step("disable user", func() { u.Enabled = false; must(s.UpdateProxyUser(u)) })
	step("enable user", func() { u.Enabled = true; must(s.UpdateProxyUser(u)) })
	step("reset secret", func() { u.Secret = "ffeeddccbbaa99887766554433221100"; must(s.UpdateProxyUser(u)) })
	step("admission on", func() {
		_, err := s.SetInboundAdmission(inbound.ID, true, "0123456789abcdef0123456789abcdef")
		must(err)
	})
	step("admission off", func() {
		_, err := s.SetInboundAdmission(inbound.ID, false, "0123456789abcdef0123456789abcdef")
		must(err)
	})
	step("delete user", func() { must(s.DeleteProxyUser(u.ID)) })
}
