package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acmeservice "github.com/ladderairport/panel/internal/acme"
	"github.com/ladderairport/panel/internal/api"
	"github.com/ladderairport/panel/internal/batch"
	"github.com/ladderairport/panel/internal/dnsprovider"
	"github.com/ladderairport/panel/internal/dnsproviders"
	"github.com/ladderairport/panel/internal/pki"
	"github.com/ladderairport/panel/internal/secretstore"
	"github.com/ladderairport/panel/internal/store"
	agentv1 "github.com/ladderairport/proto/gen/go/agent/v1"
	"google.golang.org/grpc/metadata"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

type mockRPC struct {
	mu      sync.Mutex
	calls   []string
	applyOK bool
	startOK bool
	stopOK  bool
}

func (m *mockRPC) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "close")
	return nil
}

func (m *mockRPC) ApplyConfig(_ context.Context, _, _ string, _ bool) (*agentv1.ApplyConfigResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "apply")
	if !m.applyOK {
		return &agentv1.ApplyConfigResponse{Ok: false, Message: "apply failed"}, nil
	}
	return &agentv1.ApplyConfigResponse{Ok: true, Message: "applied", AppliedHash: "h"}, nil
}

func (m *mockRPC) Start(_ context.Context) (*agentv1.StartResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "start")
	if !m.startOK {
		return &agentv1.StartResponse{Ok: false, Message: "start failed"}, nil
	}
	return &agentv1.StartResponse{Ok: true, Message: "started"}, nil
}

func (m *mockRPC) Stop(_ context.Context) (*agentv1.StopResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "stop")
	if !m.stopOK {
		return &agentv1.StopResponse{Ok: false, Message: "stop failed"}, nil
	}
	return &agentv1.StopResponse{Ok: true, Message: "stopped"}, nil
}

type mockLive struct {
	pingOK    bool
	metricsOK bool
}

func (m *mockLive) Close() error { return nil }

func (m *mockLive) Ping(_ context.Context) (*agentv1.PingResponse, error) {
	if !m.pingOK {
		return nil, context.DeadlineExceeded
	}
	return &agentv1.PingResponse{AgentVersion: "test-agent", SingboxVersion: "1.0"}, nil
}

func (m *mockLive) GetStatus(_ context.Context) (*agentv1.GetStatusResponse, error) {
	if !m.pingOK {
		return nil, context.DeadlineExceeded
	}
	return &agentv1.GetStatusResponse{State: "running", ConfigHash: "abc"}, nil
}

func (m *mockLive) GetMetrics(_ context.Context) (*agentv1.GetMetricsResponse, error) {
	if !m.metricsOK {
		return nil, context.DeadlineExceeded
	}
	return &agentv1.GetMetricsResponse{
		Connections:    3,
		UplinkBytes:    100,
		DownlinkBytes:  200,
		CpuPercent:     1.5,
		MemoryRssBytes: 4096,
	}, nil
}

func (m *mockLive) ProbeOutbound(_ context.Context, _, _ string) (*agentv1.ProbeOutboundResponse, error) {
	return &agentv1.ProbeOutboundResponse{Ok: true, DelayMs: 1}, nil
}

// logStream is a minimal StreamLogs client for SSE tests.
type logStream struct {
	lines []*agentv1.LogLine
	idx   int
}

func (s *logStream) Recv() (*agentv1.LogLine, error) {
	if s.idx >= len(s.lines) {
		return nil, io.EOF
	}
	line := s.lines[s.idx]
	s.idx++
	return line, nil
}

func (s *logStream) Header() (metadata.MD, error) { return nil, nil }
func (s *logStream) Trailer() metadata.MD         { return nil }
func (s *logStream) CloseSend() error             { return nil }
func (s *logStream) Context() context.Context     { return context.Background() }
func (s *logStream) SendMsg(any) error            { return nil }
func (s *logStream) RecvMsg(any) error            { return io.EOF }

func (m *mockLive) ListInterfaces(_ context.Context) (*agentv1.ListInterfacesResponse, error) {
	return &agentv1.ListInterfacesResponse{
		Interfaces: []*agentv1.NetworkInterface{
			{Name: "eth0", Addresses: []string{"10.0.0.2/24"}, Up: true},
			{Name: "lo0", Addresses: []string{"127.0.0.1/8"}, Up: true, Loopback: true},
		},
	}, nil
}

func (m *mockLive) UpgradeAgent(_ context.Context, version, repo, downloadURL, sha256 string) (*agentv1.UpgradeAgentResponse, error) {
	return &agentv1.UpgradeAgentResponse{
		Ok:              true,
		Message:         "staged; waiting for upgrade helper to apply and restart",
		Version:         version,
		StagedPath:      "/var/lib/ladder-agent/upgrade/ladder-agent-linux-amd64",
		PreviousVersion: "v0.0.0-test",
	}, nil
}

func (m *mockLive) StreamLogs(_ context.Context, _ string, _ int32) (agentv1.AgentControl_StreamLogsClient, error) {
	return &logStream{lines: []*agentv1.LogLine{
		{TsUnixMs: 1, Level: "info", Message: "hello"},
	}}, nil
}

func newTestServer(t *testing.T, dial batch.DialFunc, live api.LiveDialFunc) (*httptest.Server, *http.Client, *store.Store) {
	t.Helper()
	st := openTestStore(t)
	if err := api.EnsureAdminPassword(st); err != nil {
		t.Fatalf("EnsureAdminPassword: %v", err)
	}
	// The generated initial password is random; tests log in with a fixed one.
	settings, err := st.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	hash, err := api.HashPassword("admin")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	settings.AdminPasswordHash = hash
	if err := st.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	runner := batch.NewRunner(st, func() string {
		s, _ := st.GetSettings()
		if s == nil {
			return ""
		}
		return s.DefaultAgentToken
	})
	ca, err := pki.Open(filepath.Join(t.TempDir(), "pki"))
	if err != nil {
		t.Fatalf("Open PKI: %v", err)
	}
	runner.PKI = ca
	if dial != nil {
		runner.Dial = dial
	}
	secrets, err := secretstore.New(bytes.Repeat([]byte{0x41}, 32))
	if err != nil {
		t.Fatalf("secretstore.New: %v", err)
	}
	dnsRegistry := dnsprovider.NewRegistry()
	if err := dnsproviders.RegisterBuiltins(dnsRegistry); err != nil {
		t.Fatalf("RegisterBuiltins: %v", err)
	}

	srv := &api.Server{
		Store:        st,
		Runner:       runner,
		Secret:       []byte("test-session-secret-at-least-32b"),
		Dial:         live,
		PKI:          ca,
		Secrets:      secrets,
		DNSProviders: dnsRegistry,
		ACME:         &acmeservice.Service{Secrets: secrets},
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar}
	return ts, client, st
}

func login(t *testing.T, client *http.Client, baseURL, password string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"password": password})
	resp, err := client.Post(baseURL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return resp
}

func doJSON(t *testing.T, client *http.Client, method, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(raw) == 0 {
		return resp, nil
	}
	var out map[string]any
	// May be array — try object first.
	if err := json.Unmarshal(raw, &out); err != nil {
		var arr []any
		if err2 := json.Unmarshal(raw, &arr); err2 != nil {
			t.Fatalf("decode response %q: %v", string(raw), err)
		}
		return resp, map[string]any{"_array": arr}
	}
	return resp, out
}

func TestAuthRequired(t *testing.T) {
	ts, client, _ := newTestServer(t, nil, nil)

	resp, err := client.Get(ts.URL + "/api/v1/nodes")
	if err != nil {
		t.Fatalf("GET nodes: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	ts, client, _ := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "wrong")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestLogoutClearsSession(t *testing.T) {
	ts, client, _ := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", resp.StatusCode)
	}

	resp, err = client.Get(ts.URL + "/api/v1/nodes")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("nodes after logout status = %d, want 401", resp.StatusCode)
	}
}

func TestLogoutClearsMissingOrInvalidSession(t *testing.T) {
	ts, _, _ := newTestServer(t, nil, nil)
	for _, tc := range []struct {
		name   string
		cookie string
	}{
		{name: "missing"},
		{name: "invalid", cookie: "not-a-valid-session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/logout", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "session", Value: tc.cookie, Path: "/"})
			}
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("logout status = %d, want 204", resp.StatusCode)
			}
			var cleared *http.Cookie
			for _, cookie := range resp.Cookies() {
				if cookie.Name == "session" {
					cleared = cookie
					break
				}
			}
			if cleared == nil {
				t.Fatal("logout response did not clear session cookie")
			}
			if cleared.Value != "" || cleared.Path != "/" || cleared.MaxAge >= 0 || !cleared.Expires.Before(time.Now()) {
				t.Fatalf("unexpected cleared cookie: %+v", cleared)
			}
		})
	}
}

func TestUpdateNodeIsPartialAndPreservesLiveFields(t *testing.T) {
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()

	n := &store.Node{
		Name:            "edge-one",
		Address:         "203.0.113.10",
		GRPCPort:        50051,
		Token:           "node-token",
		Labels:          []string{"edge", "prod"},
		PKICABundlePEM:  "certificate",
		PublicAddress:   "edge.example.com",
		PortMappings:    []store.PortMapping{{ListenPort: 8443, PublicPort: 443}},
		EgressInterface: "eth0",
		Status:          "online",
		LastSeenUnix:    1234,
		ConfigHash:      "config-hash",
		RuntimeState:    "running",
		AgentVersion:    "v1.2.3",
		SingboxVersion:  "v1.11.0",
		Connections:     17,
		UplinkBytes:     100,
		DownlinkBytes:   200,
		CPUPercent:      12.5,
		MemoryRSSBytes:  4096,
		MetricsAtUnix:   1220,
		LastError:       "cached warning",
	}
	if err := st.CreateNode(n); err != nil {
		t.Fatal(err)
	}

	// Runtime fields mirror a stale browser snapshot and must be ignored. An
	// explicitly empty address, however, is an operator edit and must be saved.
	resp, body := doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+n.ID, map[string]any{
		"address":          "",
		"status":           "unreachable",
		"runtime_state":    "stopped",
		"agent_version":    "stale",
		"connections":      0,
		"last_seen_unix":   1,
		"config_hash":      "stale-hash",
		"metrics_at_unix":  1,
		"memory_rss_bytes": 0,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update node status = %d body=%v", resp.StatusCode, body)
	}
	got, err := st.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != "" || got.Status != "pending" {
		t.Fatalf("address/status = %q/%q, want empty/pending", got.Address, got.Status)
	}
	if got.RuntimeState != "running" || got.AgentVersion != "v1.2.3" || got.Connections != 17 ||
		got.LastSeenUnix != 1234 || got.ConfigHash != "config-hash" || got.MetricsAtUnix != 1220 ||
		got.MemoryRSSBytes != 4096 {
		t.Fatalf("live fields overwritten by partial update: %+v", got)
	}
	if got.Token != "node-token" || len(got.Labels) != 2 ||
		got.PKICABundlePEM != "certificate" || got.PublicAddress != "edge.example.com" ||
		got.EgressInterface != "eth0" || len(got.PortMappings) != 1 {
		t.Fatalf("omitted editable fields were not preserved: %+v", got)
	}

	resp, body = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+n.ID, map[string]any{
		"token":            "",
		"labels":           []string{},
		"public_address":   "",
		"port_mappings":    []any{},
		"egress_interface": "",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear node fields status = %d body=%v", resp.StatusCode, body)
	}
	got, err = st.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "" || len(got.Labels) != 0 || got.PKICABundlePEM != "certificate" || got.PublicAddress != "" ||
		len(got.PortMappings) != 0 || got.EgressInterface != "" {
		t.Fatalf("explicit empty values were not saved: %+v", got)
	}

	resp, _ = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+n.ID, map[string]any{"grpc_port": 0})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid grpc_port status = %d, want 400", resp.StatusCode)
	}
}

func TestInboundEnabledDefaultsAndPartialUpdate(t *testing.T) {
	ts, client, _ := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()

	resp, created := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/inbounds", map[string]any{
		"name": "default-enabled", "protocol": "shadowsocks",
		"params": map[string]any{"port": 8388, "method": "aes-256-gcm"},
	})
	if resp.StatusCode != http.StatusCreated || created["enabled"] != true {
		t.Fatalf("default enabled create status=%d body=%v", resp.StatusCode, created)
	}
	id, _ := created["id"].(string)
	resp, updated := doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/inbounds/"+id, map[string]any{
		"name": "renamed",
	})
	if resp.StatusCode != http.StatusOK || updated["enabled"] != true {
		t.Fatalf("partial update disabled inbound: status=%d body=%v", resp.StatusCode, updated)
	}

	resp, created = doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/inbounds", map[string]any{
		"name": "explicit-disabled", "protocol": "shadowsocks", "enabled": false,
		"params": map[string]any{"port": 8389, "method": "aes-256-gcm"},
	})
	if resp.StatusCode != http.StatusCreated || created["enabled"] != false {
		t.Fatalf("explicit disabled create status=%d body=%v", resp.StatusCode, created)
	}
}

func TestInboundFRPSConnectionValidation(t *testing.T) {
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()
	resp, created := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/inbounds", map[string]any{
		"name": "frp-ss", "protocol": "shadowsocks", "params": map[string]any{"port": 8388, "method": "aes-256-gcm"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create inbound status = %d, body = %v", resp.StatusCode, created)
	}
	node := &store.Node{Name: "edge", Address: "192.0.2.10", GRPCPort: 50051, Status: "online"}
	if err := st.CreateNode(node); err != nil {
		t.Fatal(err)
	}
	config := `user = "s-account"
auth.token = "secret"
serverAddr = "frps.example.com"
serverPort = 7000
transport.tls.enable = false
transport.tls.disableCustomTLSFirstByte = false
[[proxies]]
name = "LadderPro"
type = "tcp"
localIP = "192.168.123.141"
localPort = 64192
remotePort = 20001`
	url := ts.URL + "/api/v1/nodes/" + node.ID + "/inbounds"
	resp, body := doJSON(t, client, http.MethodPut, url, map[string]any{
		"bindings":    []map[string]any{{"inbound_id": created["id"], "frp_enabled": true, "frpc_config": config}},
		"skip_deploy": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid FRP node binding status = %d, body = %v", resp.StatusCode, body)
	}
	attachments, err := st.ListNodeInboundAttachments(node.ID)
	if err != nil || len(attachments) != 1 || !attachments[0].FRPEnabled || attachments[0].FRPCConfig != config {
		t.Fatalf("FRP binding not persisted: %+v, %v", attachments, err)
	}
	resp, body = doJSON(t, client, http.MethodPut, url, map[string]any{
		"bindings": []map[string]any{{"inbound_id": created["id"], "frp_enabled": true,
			"frpc_config": `serverAddr = "frps.example.com"
serverPort = 7000
auth.token = "secret"
[[proxies]]
type = "tcp"
remotePort = 0`}},
		"skip_deploy": true,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid FRP node binding status = %d, body = %v", resp.StatusCode, body)
	}
	resp, body = doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/inbounds", map[string]any{
		"name": "global-frp", "protocol": "shadowsocks", "params": map[string]any{
			"port": 8389, "method": "aes-256-gcm", "frp_enabled": true, "frpc_config": config,
		},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("global FRP settings must be rejected: status = %d, body = %v", resp.StatusCode, body)
	}
}

func TestSubscriptionCanExcludeAllLocalInbounds(t *testing.T) {
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()

	n := &store.Node{Name: "edge", Address: "203.0.113.20", GRPCPort: 50051, Status: "online"}
	if err := st.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	in := &store.InboundConfig{
		Name: "ss", Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{"port": 8388, "method": "aes-256-gcm", "password": "secret"},
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeInbounds(n.ID, []string{in.ID}); err != nil {
		t.Fatal(err)
	}
	resp, bad := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/subscriptions", map[string]any{
		"name": "bad-source", "format": "clash", "inbound_ids": []string{},
		"include_all_inbounds": false, "external_source_ids": []string{"missing-source"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid source create status=%d body=%v", resp.StatusCode, bad)
	}
	list, err := st.ListSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("failed create left subscription behind: %+v", list)
	}

	resp, externalOnly := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/subscriptions", map[string]any{
		"name": "external-only", "format": "clash", "inbound_ids": []string{},
		"include_all_inbounds": false,
	})
	if resp.StatusCode != http.StatusCreated || externalOnly["include_all_inbounds"] != false {
		t.Fatalf("external-only create status=%d body=%v", resp.StatusCode, externalOnly)
	}
	externalID, _ := externalOnly["id"].(string)
	resp, err = client.Get(ts.URL + "/api/v1/subscriptions/" + externalID + "/preview")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("external-only preview included local endpoint: status=%d", resp.StatusCode)
	}

	// Legacy clients omitted the new flag and used [] for all local inbounds.
	resp, legacy := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/subscriptions", map[string]any{
		"name": "legacy-all", "format": "clash", "inbound_ids": []string{},
	})
	if resp.StatusCode != http.StatusCreated || legacy["include_all_inbounds"] != true {
		t.Fatalf("legacy create status=%d body=%v", resp.StatusCode, legacy)
	}
	legacyID, _ := legacy["id"].(string)
	legacyBefore, err := st.GetSubscription(legacyID)
	if err != nil {
		t.Fatal(err)
	}
	resp, bad = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/subscriptions/"+legacyID, map[string]any{
		"name": "should-roll-back", "rotate_token": true,
		"external_source_ids": []string{"missing-source"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid source update status=%d body=%v", resp.StatusCode, bad)
	}
	legacyAfter, err := st.GetSubscription(legacyID)
	if err != nil {
		t.Fatal(err)
	}
	if legacyAfter.Name != legacyBefore.Name || legacyAfter.Token != legacyBefore.Token {
		t.Fatalf("failed update was partially committed: before=%+v after=%+v", legacyBefore, legacyAfter)
	}
	resp, err = client.Get(ts.URL + "/api/v1/subscriptions/" + legacyID + "/preview")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("legacy all-local preview status=%d body=%s", resp.StatusCode, body)
	}
}

func TestFleetFlow(t *testing.T) {
	rpc := &mockRPC{applyOK: true, startOK: true, stopOK: true}
	live := &mockLive{pingOK: true, metricsOK: true}

	ts, client, _ := newTestServer(t,
		func(_ context.Context, n store.Node, _ string) (batch.NodeRPC, error) {
			return rpc, nil
		},
		func(_ context.Context, n store.Node, _ string) (api.NodeLive, error) {
			return live, nil
		},
	)

	// Login with default password.
	resp := login(t, client, ts.URL, "admin")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("login status = %d body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()

	// Create node.
	resp, nodeObj := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/nodes", map[string]any{
		"name":      "edge-1",
		"address":   "10.0.0.1",
		"grpc_port": 50051,
		"labels":    []string{"edge"},
		"token":     "tok",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create node status = %d body=%v", resp.StatusCode, nodeObj)
	}
	nodeID, _ := nodeObj["id"].(string)
	if nodeID == "" {
		t.Fatalf("missing node id: %v", nodeObj)
	}

	// Set public base URL so install command includes auto-enroll.
	resp, _ = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/settings", map[string]any{
		"public_base_url": ts.URL,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put settings for public_base_url status = %d", resp.StatusCode)
	}

	// Bootstrap node with install command.
	resp, boot := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/nodes/bootstrap", map[string]any{
		"name": "edge-boot", "grpc_port": 50051,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap status = %d body=%v", resp.StatusCode, boot)
	}
	cmd, _ := boot["install_command"].(string)
	if !strings.Contains(cmd, "LADDER_ENROLL_TOKEN=") || strings.Contains(cmd, "LADDER_TLS=") {
		t.Fatalf("install_command = %q", cmd)
	}
	if !strings.Contains(cmd, "LADDER_PANEL=") {
		t.Fatalf("expected LADDER_PANEL in command: %q", cmd)
	}
	bootNode, _ := boot["node"].(map[string]any)
	bootID, _ := bootNode["id"].(string)
	bootToken, _ := boot["token"].(string)
	if bootID == "" || bootToken == "" {
		t.Fatalf("bootstrap node id/token missing: %v", boot)
	}
	resp, inst := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/nodes/"+bootID+"/install-command", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("install-command status = %d", resp.StatusCode)
	}
	if install, _ := inst["install_command"].(string); install == "" {
		t.Fatalf("install-command body = %v", inst)
	}

	// Create inbound (shadowsocks).
	resp, inObj := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/inbounds", map[string]any{
		"name":     "ss-main",
		"protocol": "shadowsocks",
		"enabled":  true,
		"params": map[string]any{
			"listen":   "0.0.0.0",
			"port":     8388,
			"method":   "aes-128-gcm",
			"password": "password123",
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create inbound status = %d body=%v", resp.StatusCode, inObj)
	}
	inboundID, _ := inObj["id"].(string)
	if inboundID == "" {
		t.Fatalf("missing inbound id: %v", inObj)
	}

	// Attach inbound to node (auto-deploys apply+start).
	resp, attach := doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+nodeID+"/inbounds", map[string]any{
		"inbound_ids": []string{inboundID},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("attach status = %d body=%v", resp.StatusCode, attach)
	}
	if attach["deployed"] != true {
		t.Fatalf("expected auto deploy, got %v", attach)
	}
	rpc.mu.Lock()
	calls := append([]string{}, rpc.calls...)
	rpc.mu.Unlock()
	foundApply := false
	for _, c := range calls {
		if c == "apply" {
			foundApply = true
			break
		}
	}
	if !foundApply {
		t.Fatalf("expected apply on attach, calls=%v", calls)
	}

	// Preview config.
	resp, preview := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/nodes/"+nodeID+"/config/preview", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d body=%v", resp.StatusCode, preview)
	}
	inbounds, ok := preview["inbounds"].([]any)
	if !ok || len(inbounds) != 1 {
		t.Fatalf("preview inbounds = %v", preview["inbounds"])
	}

	// Templates list.
	resp, tmpl := doJSON(t, client, http.MethodGet, ts.URL+"/api/v1/templates", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("templates status = %d", resp.StatusCode)
	}
	arr, _ := tmpl["_array"].([]any)
	if len(arr) < 7 {
		t.Fatalf("expected >=7 templates, got %v", tmpl)
	}

	// Unknown protocol rejected.
	resp, badIn := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/inbounds", map[string]any{
		"name": "nope", "protocol": "not-a-protocol", "enabled": true,
		"params": map[string]any{"port": 1},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown protocol status = %d body=%v", resp.StatusCode, badIn)
	}

	// Probe.
	resp, probe := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/nodes/"+nodeID+"/probe", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe status = %d body=%v", resp.StatusCode, probe)
	}
	if probe["agent_version"] != "test-agent" {
		t.Fatalf("probe agent_version = %v", probe["agent_version"])
	}

	// Metrics.
	resp, metrics := doJSON(t, client, http.MethodGet, ts.URL+"/api/v1/nodes/"+nodeID+"/metrics", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d body=%v", resp.StatusCode, metrics)
	}
	if metrics["connections"] != float64(3) {
		t.Fatalf("connections = %v", metrics["connections"])
	}

	// Batch apply via injectable runner dial.
	resp, taskObj := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/batch/apply", map[string]any{
		"node_ids": []string{nodeID},
		"labels":   []string{},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch apply status = %d body=%v", resp.StatusCode, taskObj)
	}
	if taskObj["status"] != "success" {
		t.Fatalf("task status = %v results=%v", taskObj["status"], taskObj["results"])
	}
	if taskObj["type"] != "apply" {
		t.Fatalf("task type = %v", taskObj["type"])
	}

	// Get task by id.
	taskID, _ := taskObj["id"].(string)
	resp, got := doJSON(t, client, http.MethodGet, ts.URL+"/api/v1/tasks/"+taskID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task status = %d", resp.StatusCode)
	}
	if got["id"] != taskID {
		t.Fatalf("get task id = %v", got["id"])
	}

	// Settings GET/PUT.
	resp, settings := doJSON(t, client, http.MethodGet, ts.URL+"/api/v1/settings", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get settings status = %d", resp.StatusCode)
	}
	if _, hasHash := settings["admin_password_hash"]; hasHash {
		t.Fatal("admin_password_hash must not be exposed in JSON")
	}

	resp, settings = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/settings", map[string]any{
		"default_agent_token": "new-token",
		"grpc_timeout_sec":    15,
		"max_concurrency":     5,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put settings status = %d body=%v", resp.StatusCode, settings)
	}
	if settings["default_agent_token"] != "new-token" {
		t.Fatalf("token = %v", settings["default_agent_token"])
	}
	if settings["grpc_timeout_sec"] != float64(15) {
		t.Fatalf("timeout = %v", settings["grpc_timeout_sec"])
	}
}

func TestSingleNodeApply(t *testing.T) {
	rpc := &mockRPC{applyOK: true}
	ts, client, st := newTestServer(t,
		func(_ context.Context, _ store.Node, _ string) (batch.NodeRPC, error) {
			return rpc, nil
		},
		nil,
	)

	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}

	n := &store.Node{Name: "n1", Address: "127.0.0.1", GRPCPort: 50051, Token: "t"}
	if err := st.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	in := &store.InboundConfig{
		Name: "ss", Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{"listen": "0.0.0.0", "port": 1000, "method": "aes-128-gcm", "password": "x"},
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatalf("CreateInbound: %v", err)
	}
	if err := st.SetNodeInbounds(n.ID, []string{in.ID}); err != nil {
		t.Fatalf("SetNodeInbounds: %v", err)
	}

	resp, task := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/nodes/"+n.ID+"/apply", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d body=%v", resp.StatusCode, task)
	}
	if task["status"] != "success" {
		t.Fatalf("status = %v", task)
	}
}

func TestBatchByLabels(t *testing.T) {
	rpc := &mockRPC{startOK: true, applyOK: true}
	ts, client, st := newTestServer(t,
		func(_ context.Context, _ store.Node, _ string) (batch.NodeRPC, error) {
			return rpc, nil
		},
		nil,
	)

	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()

	n1 := &store.Node{Name: "a", Address: "1.1.1.1", GRPCPort: 1, Labels: []string{"prod"}, Token: "t"}
	n2 := &store.Node{Name: "b", Address: "2.2.2.2", GRPCPort: 1, Labels: []string{"dev"}, Token: "t"}
	n3 := &store.Node{Name: "c", Address: "3.3.3.3", GRPCPort: 1, Labels: []string{"prod"}, Token: "t"}
	if err := st.CreateNode(n1); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(n2); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(n3); err != nil {
		t.Fatal(err)
	}

	// Start only matches the prod-labeled subset (n1).
	resp, task := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/batch/start", map[string]any{
		"node_ids": []string{},
		"labels":   []string{"prod"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch start status = %d body=%v", resp.StatusCode, task)
	}
	ids, _ := task["node_ids"].([]any)
	if len(ids) != 2 {
		t.Fatalf("node_ids = %v, want 2 (both prod)", ids)
	}
	if task["status"] != "success" {
		t.Fatalf("status = %v", task["status"])
	}

	// Batch apply by shared label should target both prod nodes with per-node results.
	in := &store.InboundConfig{
		Name: "ss-batch", Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{"listen": "0.0.0.0", "port": 2000, "method": "aes-128-gcm", "password": "x"},
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatalf("CreateInbound: %v", err)
	}
	if err := st.SetNodeInbounds(n1.ID, []string{in.ID}); err != nil {
		t.Fatalf("SetNodeInbounds n1: %v", err)
	}
	if err := st.SetNodeInbounds(n3.ID, []string{in.ID}); err != nil {
		t.Fatalf("SetNodeInbounds n3: %v", err)
	}

	resp, task = doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/batch/apply", map[string]any{
		"node_ids": []string{},
		"labels":   []string{"prod"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch apply status = %d body=%v", resp.StatusCode, task)
	}
	if task["status"] != "success" {
		t.Fatalf("batch apply status = %v results=%v", task["status"], task["results"])
	}
	ids, _ = task["node_ids"].([]any)
	if len(ids) != 2 {
		t.Fatalf("batch apply node_ids = %v, want 2", ids)
	}
	results, _ := task["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("batch apply results = %v, want 2", results)
	}
}

func TestPasswordChangeRevokesSession(t *testing.T) {
	ts, client, _ := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", resp.StatusCode)
	}

	resp, _ = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/settings", map[string]any{
		"new_password": "rotated-password",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put settings status = %d", resp.StatusCode)
	}

	// The pre-change cookie must be rejected even though it is unexpired.
	resp, err := client.Get(ts.URL + "/api/v1/nodes")
	if err != nil {
		t.Fatal(err)
		resp.Body.Close()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("nodes after password change status = %d, want 401", resp.StatusCode)
	}

	// Old password is gone; new one works.
	resp = login(t, client, ts.URL, "admin")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password status = %d, want 401", resp.StatusCode)
	}
	resp = login(t, client, ts.URL, "rotated-password")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("new password status = %d", resp.StatusCode)
	}
}

func TestLoginRateLimitedAfterFailures(t *testing.T) {
	ts, client, _ := newTestServer(t, nil, nil)
	for i := 0; i < 5; i++ {
		resp := login(t, client, ts.URL, "wrong-password")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", i+1, resp.StatusCode)
		}
	}
	// Sixth attempt is throttled even with the correct password.
	resp := login(t, client, ts.URL, "admin")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("throttled login status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
}

func TestUpdateNodeDDNSEnabled(t *testing.T) {
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()
	n := &store.Node{Name: "ddns-node", Address: "192.0.2.40", GRPCPort: 50051, DDNSEnabled: true}
	if err := st.CreateNode(n); err != nil {
		t.Fatal(err)
	}

	resp, body := doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+n.ID, map[string]any{
		"ddns_enabled": false,
	})
	if resp.StatusCode != http.StatusOK || body["ddns_enabled"] != false {
		t.Fatalf("disable status = %d body = %#v", resp.StatusCode, body)
	}
	got, err := st.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DDNSEnabled {
		t.Fatal("ddns_enabled was not persisted as false")
	}

	// Omitted field preserves the stored value.
	resp, _ = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+n.ID, map[string]any{
		"name": "ddns-node-renamed",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename status = %d", resp.StatusCode)
	}
	got, err = st.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DDNSEnabled {
		t.Fatal("omitted ddns_enabled was not preserved")
	}

	resp, body = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+n.ID, map[string]any{
		"ddns_enabled": true,
	})
	if resp.StatusCode != http.StatusOK || body["ddns_enabled"] != true {
		t.Fatalf("re-enable status = %d body = %#v", resp.StatusCode, body)
	}
}

func TestNodeUpgradeRejectsWrapperClients(t *testing.T) {
	ts, client, st := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()

	for _, clientType := range []string{"android", "openwrt"} {
		n := &store.Node{
			Name:        clientType,
			Token:       "tok-" + clientType,
			ControlMode: store.ControlModeUplink,
			ClientType:  clientType,
		}
		if err := st.CreateNode(n); err != nil {
			t.Fatal(err)
		}
		// CreateNode does not persist identity columns; store them the way a report does.
		if _, err := st.ApplyNodeReport(n.ID, store.NodeReport{
			CollectedAtUnix: time.Now().Unix(),
			Status:          "online",
			AgentVersion:    "v0.15.12",
			CoreVersion:     "v0.15.12",
			ClientType:      clientType,
			ClientVersion:   "wrapper",
		}); err != nil {
			t.Fatal(err)
		}
		resp, body := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/nodes/"+n.ID+"/upgrade", map[string]any{})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s upgrade status = %d body = %#v", clientType, resp.StatusCode, body)
		}
	}

	linuxNode := &store.Node{Name: "linux", Token: "tok-linux", ControlMode: store.ControlModeUplink}
	if err := st.CreateNode(linuxNode); err != nil {
		t.Fatal(err)
	}
	resp, body := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/nodes/"+linuxNode.ID+"/upgrade", map[string]any{
		"version": "v0.15.12",
	})
	if resp.StatusCode != http.StatusAccepted || body["queued"] != true {
		t.Fatalf("empty client_type upgrade status = %d body = %#v", resp.StatusCode, body)
	}
}
