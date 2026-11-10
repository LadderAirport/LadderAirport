package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ladderairport/panel/internal/api"
	"github.com/ladderairport/panel/internal/proxyuser"
	"github.com/ladderairport/panel/internal/store"
)

func singboxOutboundsByType(t *testing.T, raw []byte) map[string]map[string]any {
	t.Helper()
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, o := range cfg.Outbounds {
		if typ, _ := o["type"].(string); typ == "vless" || typ == "shadowsocks" {
			out[typ] = o
		}
	}
	return out
}

func TestProxyUserSubscriptionAdmission(t *testing.T) {
	t.Cleanup(api.SetAdmissionReapplyDelay(time.Hour))
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()

	node := &store.Node{Name: "pu", Address: "192.0.2.31", GRPCPort: 50051}
	if err := st.CreateNode(node); err != nil {
		t.Fatal(err)
	}
	vless := &store.InboundConfig{Name: "pu-vless", Protocol: "vless", Enabled: true, Params: map[string]any{
		"port": 14001, "uuid": "22222222-2222-4222-8222-222222222222",
	}}
	ss := &store.InboundConfig{Name: "pu-ss", Protocol: "shadowsocks", Enabled: true, Params: map[string]any{
		"port": 14002, "method": "aes-128-gcm", "password": "shared-ss",
	}}
	for _, in := range []*store.InboundConfig{vless, ss} {
		if err := st.CreateInbound(in); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetNodeInbounds(node.ID, []string{vless.ID, ss.ID}); err != nil {
		t.Fatal(err)
	}

	r, user := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/proxy-users", map[string]any{"name": "alice"})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create user = %d %v", r.StatusCode, user)
	}
	if _, leaked := user["secret"]; leaked {
		t.Fatal("user secret must not be exposed")
	}
	userID := user["id"].(string)
	r, sub := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/subscriptions", map[string]any{
		"name": "alice-sub", "include_all_inbounds": true, "include_standalone": true, "user_id": userID,
	})
	if r.StatusCode != http.StatusCreated || sub["user_id"] != userID {
		t.Fatalf("create sub = %d %v", r.StatusCode, sub)
	}
	r, bad := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/subscriptions", map[string]any{"name": "x", "user_id": "nope"})
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown user = %d %v", r.StatusCode, bad)
	}
	shared := &store.Subscription{Name: "shared", Token: "shared-token-0001", Enabled: true, IncludeAllInbounds: true, IncludeStandalone: true}
	if err := st.CreateSubscription(shared); err != nil {
		t.Fatal(err)
	}
	userURL := ts.URL + "/sub/" + sub["token"].(string) + "?flag=singbox"
	sharedURL := ts.URL + "/sub/" + shared.Token + "?flag=singbox"

	stored, err := st.GetProxyUser(userID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := proxyuser.ForUser(*stored)
	got := singboxOutboundsByType(t, fetchSub(t, client, userURL))
	if got["vless"]["uuid"] != id.UUID() {
		t.Fatalf("user sub vless = %v", got["vless"])
	}
	if got["shadowsocks"]["password"] != "shared-ss" {
		t.Fatalf("legacy SS keeps the shared password: %v", got["shadowsocks"])
	}

	// Legacy SS cannot be admission-only; VLESS can.
	if r, body := doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/inbounds/"+ss.ID+"/admission", map[string]any{"admission_only": true}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("legacy SS admission = %d %v", r.StatusCode, body)
	}
	if r, body := doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/inbounds/"+vless.ID+"/admission", map[string]any{"admission_only": true}); r.StatusCode != http.StatusOK || body["admission_only"] != true {
		t.Fatalf("vless admission = %d %v", r.StatusCode, body)
	}
	got = singboxOutboundsByType(t, fetchSub(t, client, sharedURL))
	if _, ok := got["vless"]; ok || got["shadowsocks"] == nil {
		t.Fatalf("shared sub must drop the admission-only inbound: %v", got)
	}
	if got = singboxOutboundsByType(t, fetchSub(t, client, userURL)); got["vless"]["uuid"] != id.UUID() {
		t.Fatalf("user sub after admission-only = %v", got)
	}
	// Switching an admission-only inbound to an unsupported protocol is rejected.
	if r, body := doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/inbounds/"+vless.ID, map[string]any{
		"protocol": "shadowsocks", "params": map[string]any{"port": 14001, "method": "aes-128-gcm", "password": "p"},
	}); r.StatusCode != http.StatusBadRequest || !strings.Contains(fmt.Sprint(body["error"]), "仅准入用户") {
		t.Fatalf("incompatible inbound edit = %d %v", r.StatusCode, body)
	}

	// Resetting credentials changes the derived UUID.
	if r, body := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/proxy-users/"+userID+"/reset-credentials", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("reset = %d %v", r.StatusCode, body)
	}
	if got = singboxOutboundsByType(t, fetchSub(t, client, userURL)); got["vless"]["uuid"] == id.UUID() {
		t.Fatal("reset must rotate the derived UUID")
	}

	// A disabled user loses the subscription link.
	if r, body := doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/proxy-users/"+userID, map[string]any{"enabled": false}); r.StatusCode != http.StatusOK {
		t.Fatalf("disable = %d %v", r.StatusCode, body)
	}
	resp, err = client.Get(userURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled user sub = %d", resp.StatusCode)
	}

	// Deleting the user unbinds its subscriptions.
	if r, _ := doJSON(t, client, http.MethodDelete, ts.URL+"/api/v1/proxy-users/"+userID, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", r.StatusCode)
	}
	after, err := st.GetSubscription(sub["id"].(string))
	if err != nil || after.UserID != "" {
		t.Fatalf("sub after user delete = %+v %v", after, err)
	}
}
