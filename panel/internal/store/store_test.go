package store

import (
	"database/sql"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRemovedNodeTLSColumnsAreDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-node.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE nodes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			address TEXT NOT NULL,
			grpc_port INTEGER NOT NULL,
			token TEXT NOT NULL DEFAULT '',
			labels_json TEXT NOT NULL DEFAULT '[]',
			tls_skip_verify INTEGER NOT NULL DEFAULT 0,
			ca_cert_pem TEXT NOT NULL DEFAULT '',
			pki_migration_required INTEGER NOT NULL DEFAULT 1,
			status TEXT NOT NULL DEFAULT '',
			last_seen_unix INTEGER NOT NULL DEFAULT 0,
			config_hash TEXT NOT NULL DEFAULT '',
			created_at_unix INTEGER NOT NULL,
			updated_at_unix INTEGER NOT NULL
		);
		INSERT INTO nodes VALUES (
			'legacy-node', 'legacy', '192.0.2.10', 50051, 'token', '[]',
			1, 'old-local-ca', 1, 'online', 1, '', 1, 1
		);
	`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	n, err := s.GetNode("legacy-node")
	if err != nil {
		t.Fatal(err)
	}
	if n.PKICABundlePEM != "" || n.PKICertSerial != "" {
		t.Fatalf("legacy management trust leaked into Panel PKI fields: %+v", n)
	}
	rows, err := s.db.Query(`PRAGMA table_info(nodes)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "tls_skip_verify" || name == "ca_cert_pem" || name == "pki_migration_required" {
			t.Fatalf("removed node TLS column was not dropped: %s", name)
		}
	}
}

func TestLegacyGlobalFRPMigratesToNodeAttachments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frp.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	node := &Node{Name: "edge", Address: "192.0.2.1", GRPCPort: 50051}
	if err := st.CreateNode(node); err != nil {
		t.Fatal(err)
	}
	config := `{"server_addr":"frps.example.com","server_port":7000,"remote_port":57115,"token":"secret"}`
	inbound := &InboundConfig{Name: "ss", Protocol: "shadowsocks", Enabled: true, Params: map[string]any{
		"port": 8388, "method": "aes-256-gcm", "password": "secret", "frp_enabled": true, "frpc_config": config,
	}}
	if err := st.CreateInbound(inbound); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeInbounds(node.ID, []string{inbound.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`ALTER TABLE node_inbounds DROP COLUMN frp_enabled`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`ALTER TABLE node_inbounds DROP COLUMN frpc_config`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	attachments, err := st.ListNodeInboundAttachments(node.ID)
	if err != nil || len(attachments) != 1 || !attachments[0].FRPEnabled || attachments[0].FRPCConfig != config {
		t.Fatalf("attachments = %+v, %v", attachments, err)
	}
	global, err := st.GetInbound(inbound.ID)
	if err != nil || global.Params["frp_enabled"] != nil || global.Params["frpc_config"] != nil {
		t.Fatalf("global inbound still has FRP: %+v, %v", global, err)
	}
}

func TestSubscriptionInboundModeMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE subscriptions (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			format TEXT NOT NULL,
			token TEXT NOT NULL UNIQUE,
			inbound_ids_json TEXT NOT NULL DEFAULT '[]',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at_unix INTEGER NOT NULL,
			updated_at_unix INTEGER NOT NULL
		);
		INSERT INTO subscriptions VALUES ('all', 'all', 'clash', 'token-all', '[]', 1, 1, 1);
		INSERT INTO subscriptions VALUES ('selected', 'selected', 'clash', 'token-selected', '["in-1"]', 1, 1, 1);
		INSERT INTO subscriptions VALUES ('null-ids', 'null ids', 'clash', 'token-null', 'null', 1, 1, 1);
	`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	all, err := s.GetSubscription("all")
	if err != nil {
		t.Fatal(err)
	}
	if !all.IncludeAllInbounds || len(all.InboundIDs) != 0 {
		t.Fatalf("legacy empty filter migration = %+v, want include all", all)
	}
	selected, err := s.GetSubscription("selected")
	if err != nil {
		t.Fatal(err)
	}
	if selected.IncludeAllInbounds || len(selected.InboundIDs) != 1 || selected.InboundIDs[0] != "in-1" {
		t.Fatalf("legacy selected filter migration = %+v", selected)
	}
	nullIDs, err := s.GetSubscription("null-ids")
	if err != nil {
		t.Fatal(err)
	}
	if !nullIDs.IncludeAllInbounds || nullIDs.InboundIDs == nil || len(nullIDs.InboundIDs) != 0 {
		t.Fatalf("legacy null filter migration = %+v, want non-nil empty include-all filter", nullIDs)
	}
}

func TestCreateAndListNodes(t *testing.T) {
	s := openTestStore(t)

	n := &Node{
		Name:     "node-a",
		Address:  "10.0.0.1",
		GRPCPort: 9090,
		Labels:   []string{"edge", "prod"},
		Status:   "unknown",
	}
	if err := s.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if n.ID == "" {
		t.Fatal("expected generated node ID")
	}
	if n.CreatedAtUnix == 0 || n.UpdatedAtUnix == 0 {
		t.Fatal("expected timestamps to be set")
	}

	nodes, err := s.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("ListNodes len = %d, want 1", len(nodes))
	}
	got := nodes[0]
	if got.ID != n.ID || got.Name != "node-a" || got.Address != "10.0.0.1" || got.GRPCPort != 9090 {
		t.Fatalf("unexpected node: %+v", got)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "edge" || got.Labels[1] != "prod" {
		t.Fatalf("unexpected labels: %v", got.Labels)
	}

	// List by labels (any match)
	matched, err := s.ListNodesByLabels([]string{"prod", "missing"})
	if err != nil {
		t.Fatalf("ListNodesByLabels: %v", err)
	}
	if len(matched) != 1 || matched[0].ID != n.ID {
		t.Fatalf("ListNodesByLabels = %+v", matched)
	}
	none, err := s.ListNodesByLabels([]string{"nope"})
	if err != nil {
		t.Fatalf("ListNodesByLabels empty: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no match, got %d", len(none))
	}
}

func TestCreateInboundAndAttach(t *testing.T) {
	s := openTestStore(t)

	n := &Node{Name: "n1", Address: "127.0.0.1", GRPCPort: 9000}
	if err := s.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	in := &InboundConfig{
		Name:     "ss-main",
		Protocol: "shadowsocks",
		Params: map[string]any{
			"method":   "aes-256-gcm",
			"password": "secret",
			"port":     float64(8388),
		},
		Enabled: true,
	}
	if err := s.CreateInbound(in); err != nil {
		t.Fatalf("CreateInbound: %v", err)
	}
	if in.ID == "" {
		t.Fatal("expected generated inbound ID")
	}

	inbounds, err := s.ListInbounds()
	if err != nil {
		t.Fatalf("ListInbounds: %v", err)
	}
	if len(inbounds) != 1 || inbounds[0].Protocol != "shadowsocks" {
		t.Fatalf("ListInbounds = %+v", inbounds)
	}

	if err := s.SetNodeInbounds(n.ID, []string{in.ID}); err != nil {
		t.Fatalf("SetNodeInbounds: %v", err)
	}
	attached, err := s.ListInboundsForNode(n.ID)
	if err != nil {
		t.Fatalf("ListInboundsForNode: %v", err)
	}
	if len(attached) != 1 || attached[0].ID != in.ID {
		t.Fatalf("ListInboundsForNode = %+v", attached)
	}
	if method, ok := attached[0].Params["method"].(string); !ok || method != "aes-256-gcm" {
		t.Fatalf("params not preserved: %+v", attached[0].Params)
	}

	// Replace attachment with empty
	if err := s.SetNodeInbounds(n.ID, nil); err != nil {
		t.Fatalf("SetNodeInbounds clear: %v", err)
	}
	attached, err = s.ListInboundsForNode(n.ID)
	if err != nil {
		t.Fatalf("ListInboundsForNode after clear: %v", err)
	}
	if len(attached) != 0 {
		t.Fatalf("expected no attachments, got %d", len(attached))
	}
}

func TestProxyChainOwnsBindingsAndPreservesOrder(t *testing.T) {
	s := openTestStore(t)
	nodes := []*Node{
		{Name: "entry", Address: "10.0.0.1", GRPCPort: 9090},
		{Name: "exit", Address: "10.0.0.2", GRPCPort: 9090},
	}
	inbounds := []*InboundConfig{
		{Name: "entry-in", Protocol: "shadowsocks", Enabled: true, Params: map[string]any{"port": 8388}},
		{Name: "exit-in", Protocol: "shadowsocks", Enabled: true, Params: map[string]any{"port": 8389}},
	}
	for _, node := range nodes {
		if err := s.CreateNode(node); err != nil {
			t.Fatal(err)
		}
	}
	for _, inbound := range inbounds {
		if err := s.CreateInbound(inbound); err != nil {
			t.Fatal(err)
		}
	}

	chain := &ProxyChain{
		Name: "entry-to-exit",
		Hops: []ProxyChainHop{
			{NodeID: nodes[0].ID, InboundID: inbounds[0].ID},
			{NodeID: nodes[1].ID, InboundID: inbounds[1].ID, DialAddress: "edge.internal", DialPort: 18443},
		},
	}
	if err := s.CreateProxyChain(chain); err != nil {
		t.Fatalf("CreateProxyChain: %v", err)
	}
	got, err := s.GetProxyChain(chain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.State != "disabled" || len(got.Hops) != 2 {
		t.Fatalf("created chain = %+v", got)
	}
	if got.Hops[0].Position != 0 || got.Hops[1].Position != 1 ||
		got.Hops[1].DialAddress != "edge.internal" || got.Hops[1].DialPort != 18443 {
		t.Fatalf("hop order/overrides not preserved: %+v", got.Hops)
	}
	if err := s.DeleteNode(nodes[0].ID); err == nil ||
		!strings.Contains(err.Error(), "节点仍被代理链引用：entry-to-exit") {
		t.Fatalf("删除代理链跳点节点时应返回明确的中文错误，实际为 %v", err)
	}

	if err := s.SetNodeInbounds(nodes[0].ID, []string{inbounds[0].ID}); err == nil ||
		!strings.Contains(err.Error(), "已被代理链占用") {
		t.Fatalf("standalone binding should be blocked, got %v", err)
	}
	conflict := &ProxyChain{
		Name: "conflict",
		Hops: []ProxyChainHop{
			{NodeID: nodes[0].ID, InboundID: inbounds[0].ID},
			{NodeID: nodes[1].ID, InboundID: inbounds[1].ID},
		},
	}
	if err := s.CreateProxyChain(conflict); err == nil ||
		!strings.Contains(err.Error(), "已被另一条代理链占用") {
		t.Fatalf("overlapping chain should be blocked, got %v", err)
	}
}

func TestMigrateSubscriptionsToChainsOnce(t *testing.T) {
	s := openTestStore(t)
	local := &Subscription{
		Name: "local", Format: "clash", Token: "local-token",
		IncludeAllInbounds: true, IncludeStandalone: true, Enabled: true,
	}
	externalOnly := &Subscription{
		Name: "external", Format: "clash", Token: "external-token",
		IncludeAllInbounds: false, IncludeStandalone: false, Enabled: true,
	}
	if err := s.CreateSubscription(local); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSubscription(externalOnly); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateSubscriptionsToChains(); err != nil {
		t.Fatal(err)
	}

	gotLocal, err := s.GetSubscription(local.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotLocal.IncludeStandalone || !gotLocal.IncludeAllChains {
		t.Fatalf("local subscription not migrated to chain-only: %+v", gotLocal)
	}
	gotExternal, err := s.GetSubscription(externalOnly.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotExternal.IncludeStandalone || gotExternal.IncludeAllChains {
		t.Fatalf("external-only subscription should be unchanged: %+v", gotExternal)
	}
	settings, err := s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !settings.ChainSubscriptionMigrated {
		t.Fatal("migration marker not set")
	}
	if err := s.MigrateSubscriptionsToChains(); err != nil {
		t.Fatalf("second migration must be a no-op: %v", err)
	}
}

func TestOpenRepairsInvalidShadowsocks2022Password(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inbound := &InboundConfig{
		Name: "broken-ss2022", Protocol: "shadowsocks", Enabled: true,
		Params: map[string]any{
			"listen": "0.0.0.0", "port": 8388,
			"method": "2022-blake3-aes-256-gcm", "password": "legacy_url-safe-_",
		},
	}
	if err := s.CreateInbound(inbound); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen with repair: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	repaired, err := s.GetInbound(inbound.ID)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := repaired.Params["password"].(string)
	decoded, err := base64.StdEncoding.DecodeString(password)
	if err != nil {
		t.Fatalf("repaired password is not standard Base64: %q: %v", password, err)
	}
	if len(decoded) != 32 {
		t.Fatalf("repaired key size = %d, want 32", len(decoded))
	}
}

func TestCreateAndUpdateTask(t *testing.T) {
	s := openTestStore(t)

	task := &Task{
		Type:    "apply",
		Status:  "pending",
		NodeIDs: []string{"n1", "n2"},
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.ID == "" {
		t.Fatal("expected generated task ID")
	}

	got, err := s.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != "pending" || len(got.NodeIDs) != 2 {
		t.Fatalf("unexpected task: %+v", got)
	}

	task.Status = "success"
	task.Results = []TaskNodeResult{
		{NodeID: "n1", OK: true, Message: "ok"},
		{NodeID: "n2", OK: false, Message: "timeout"},
	}
	if err := s.UpdateTask(task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	got, err = s.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask after update: %v", err)
	}
	if got.Status != "success" {
		t.Fatalf("status = %q, want success", got.Status)
	}
	if len(got.Results) != 2 || !got.Results[0].OK || got.Results[1].OK {
		t.Fatalf("results = %+v", got.Results)
	}

	tasks, err := s.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("ListTasks len = %d", len(tasks))
	}
}

func TestGetAndSaveSettings(t *testing.T) {
	s := openTestStore(t)

	st, err := s.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if st.GRPCTimeoutSec != 10 || st.MaxConcurrency != 10 || st.ListenAddr != ":8080" {
		t.Fatalf("unexpected defaults: %+v", st)
	}
	if st.DefaultAgentToken != "" {
		t.Fatalf("expected empty default token, got %q", st.DefaultAgentToken)
	}

	st.AdminPasswordHash = "hash"
	st.DefaultAgentToken = "agent-token"
	st.GRPCTimeoutSec = 30
	st.MaxConcurrency = 5
	st.ListenAddr = ":9090"
	if err := s.SaveSettings(st); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	got, err := s.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings after save: %v", err)
	}
	if got.AdminPasswordHash != "hash" ||
		got.DefaultAgentToken != "agent-token" ||
		got.GRPCTimeoutSec != 30 ||
		got.MaxConcurrency != 5 ||
		got.ListenAddr != ":9090" {
		t.Fatalf("settings not persisted: %+v", got)
	}
}

func TestUpdateNodeGetDelete(t *testing.T) {
	s := openTestStore(t)

	n := &Node{Name: "x", Address: "1.1.1.1", GRPCPort: 1, Labels: []string{"a"}}
	if err := s.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	n.Name = "y"
	n.Status = "online"
	n.ConfigHash = "abc"
	if err := s.UpdateNode(n); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Name != "y" || got.Status != "online" || got.ConfigHash != "abc" {
		t.Fatalf("GetNode = %+v", got)
	}
	if err := s.DeleteNode(n.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	if _, err := s.GetNode(n.ID); err == nil {
		t.Fatal("expected GetNode error after delete")
	}
}

func TestUpdateNodeOperatorFieldsPreservesNewerRuntimeState(t *testing.T) {
	s := openTestStore(t)
	n := &Node{
		Name:           "edge",
		Address:        "192.0.2.10",
		GRPCPort:       50051,
		Token:          "old-token",
		Labels:         []string{"prod"},
		Status:         "unknown",
		RuntimeState:   "stopped",
		AgentVersion:   "v1",
		SingboxVersion: "1.0",
		Connections:    1,
		UplinkBytes:    2,
		DownlinkBytes:  3,
		CPUPercent:     4,
		MemoryRSSBytes: 5,
		MetricsAtUnix:  6,
		LastSeenUnix:   7,
		ConfigHash:     "old-hash",
		LastError:      "old-error",
	}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}

	// The operator starts editing this stale snapshot before a live refresh lands.
	stale, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	live := *stale
	live.Status = "online"
	live.RuntimeState = "running"
	live.AgentVersion = "v2"
	live.SingboxVersion = "2.0"
	live.Connections = 101
	live.UplinkBytes = 102
	live.DownlinkBytes = 103
	live.CPUPercent = 10.5
	live.MemoryRSSBytes = 104
	live.MetricsAtUnix = 105
	live.LastSeenUnix = 106
	live.ConfigHash = "new-hash"
	live.LastError = ""
	if err := s.UpdateNode(&live); err != nil {
		t.Fatal(err)
	}

	name := stale.Name + "-renamed"
	token := "new-token"
	labels := append(stale.Labels, "edited")
	if err := s.UpdateNodeOperatorFields(n.ID, NodeOperatorUpdate{
		Name:   &name,
		Token:  &token,
		Labels: &labels,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != name || got.Token != token || len(got.Labels) != 2 {
		t.Fatalf("operator fields were not updated: %+v", got)
	}
	if got.Status != live.Status || got.RuntimeState != live.RuntimeState ||
		got.AgentVersion != live.AgentVersion || got.SingboxVersion != live.SingboxVersion ||
		got.Connections != live.Connections || got.UplinkBytes != live.UplinkBytes ||
		got.DownlinkBytes != live.DownlinkBytes || got.CPUPercent != live.CPUPercent ||
		got.MemoryRSSBytes != live.MemoryRSSBytes || got.MetricsAtUnix != live.MetricsAtUnix ||
		got.LastSeenUnix != live.LastSeenUnix || got.ConfigHash != live.ConfigHash ||
		got.LastError != live.LastError {
		t.Fatalf("operator update overwrote newer runtime state: got=%+v live=%+v", got, live)
	}
}

func TestUpdateExternalSourceInvalidatesCacheOnFetchInputChange(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*ExternalSource)
		wantClear bool
	}{
		{
			name: "url",
			mutate: func(src *ExternalSource) {
				src.URL = "https://new.example/sub"
			},
			wantClear: true,
		},
		{
			name: "headers",
			mutate: func(src *ExternalSource) {
				src.Headers = map[string]string{"Authorization": "Bearer new"}
			},
			wantClear: true,
		},
		{
			name: "metadata-only",
			mutate: func(src *ExternalSource) {
				src.Name = "renamed"
				src.RefreshIntervalSec = 3600
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			src := &ExternalSource{
				Name:               "source",
				URL:                "https://old.example/sub",
				Headers:            map[string]string{"Authorization": "Bearer old"},
				Enabled:            true,
				RefreshIntervalSec: 60,
				LastFetchUnix:      10,
				LastSuccessUnix:    9,
				LastError:          "cached warning",
				ContentType:        "clash_yaml",
				CachedBody:         "proxies: []",
				CachedProxyCount:   4,
			}
			if err := s.CreateExternalSource(src); err != nil {
				t.Fatal(err)
			}
			tc.mutate(src)
			if err := s.UpdateExternalSource(src); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetExternalSource(src.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantClear {
				if got.CachedBody != "" || got.CachedProxyCount != 0 || got.ContentType != "" ||
					got.LastFetchUnix != 0 || got.LastSuccessUnix != 0 || got.LastError != "" {
					t.Fatalf("fetch input change retained cache: %+v", got)
				}
				return
			}
			if got.CachedBody != "proxies: []" || got.CachedProxyCount != 4 ||
				got.ContentType != "clash_yaml" || got.LastFetchUnix != 10 ||
				got.LastSuccessUnix != 9 || got.LastError != "cached warning" {
				t.Fatalf("metadata-only change cleared cache: %+v", got)
			}
		})
	}
}

func TestSaveAndLatestSnapshot(t *testing.T) {
	s := openTestStore(t)

	if err := s.SaveSnapshot(&ConfigSnapshot{
		NodeID:     "node-1",
		ConfigJSON: `{"a":1}`,
		ConfigHash: "h1",
		TaskID:     "t1",
	}); err != nil {
		t.Fatalf("SaveSnapshot 1: %v", err)
	}
	// Ensure later timestamp
	if err := s.SaveSnapshot(&ConfigSnapshot{
		NodeID:        "node-1",
		ConfigJSON:    `{"a":2}`,
		ConfigHash:    "h2",
		CreatedAtUnix: 9999999999,
	}); err != nil {
		t.Fatalf("SaveSnapshot 2: %v", err)
	}

	snap, err := s.LatestSnapshot("node-1")
	if err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	}
	if snap.ConfigHash != "h2" || snap.ConfigJSON != `{"a":2}` {
		t.Fatalf("latest = %+v", snap)
	}
}

func TestNodeEgressInterface(t *testing.T) {
	s := openTestStore(t)
	n := &Node{Name: "egress", Address: "1.2.3.4", GRPCPort: 50051, EgressInterface: "eth1"}
	if err := s.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.EgressInterface != "eth1" {
		t.Fatalf("EgressInterface = %q", got.EgressInterface)
	}
	n.EgressInterface = ""
	if err := s.UpdateNode(n); err != nil {
		t.Fatalf("UpdateNode clear: %v", err)
	}
	got, err = s.GetNode(n.ID)
	if err != nil {
		t.Fatalf("GetNode after clear: %v", err)
	}
	if got.EgressInterface != "" {
		t.Fatalf("expected empty EgressInterface, got %q", got.EgressInterface)
	}
}

func TestNodePublicAddress(t *testing.T) {
	s := openTestStore(t)
	n := &Node{
		Name:          "nat",
		Address:       "10.0.0.8",
		GRPCPort:      50051,
		PublicAddress: "edge.example.com",
	}
	if err := s.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.PublicAddress != "edge.example.com" {
		t.Fatalf("PublicAddress = %q", got.PublicAddress)
	}
	if got.Address != "10.0.0.8" {
		t.Fatalf("Address = %q", got.Address)
	}
	n.PublicAddress = ""
	if err := s.UpdateNode(n); err != nil {
		t.Fatalf("UpdateNode clear: %v", err)
	}
	got, err = s.GetNode(n.ID)
	if err != nil {
		t.Fatalf("GetNode after clear: %v", err)
	}
	if got.PublicAddress != "" {
		t.Fatalf("expected empty PublicAddress, got %q", got.PublicAddress)
	}
}

func TestNodePortMappings(t *testing.T) {
	s := openTestStore(t)
	n := &Node{
		Name:          "nat-map",
		Address:       "10.0.0.8",
		GRPCPort:      50051,
		PublicAddress: "203.0.113.9",
		PortMappings: []PortMapping{
			{ListenPort: 8443, PublicPort: 443},
			{ListenPort: 9000, PublicPort: 9000}, // identity dropped
			{ListenPort: -1, PublicPort: 1},      // invalid dropped
			{ListenPort: 1000, PublicPort: 2000},
			{ListenPort: 1000, PublicPort: 3000}, // last wins
		},
	}
	if err := s.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if len(got.PortMappings) != 2 {
		t.Fatalf("PortMappings = %+v, want 2 entries", got.PortMappings)
	}
	if got.PortMappings[0] != (PortMapping{ListenPort: 8443, PublicPort: 443}) {
		t.Fatalf("first = %+v", got.PortMappings[0])
	}
	if got.PortMappings[1] != (PortMapping{ListenPort: 1000, PublicPort: 3000}) {
		t.Fatalf("second = %+v", got.PortMappings[1])
	}
	if MapPublicPort(got.PortMappings, 8443) != 443 {
		t.Fatalf("map 8443")
	}
	if MapPublicPort(got.PortMappings, 5555) != 5555 {
		t.Fatalf("fallback")
	}

	// Clear mappings
	n.PortMappings = nil
	if err := s.UpdateNode(n); err != nil {
		t.Fatalf("UpdateNode clear: %v", err)
	}
	got, err = s.GetNode(n.ID)
	if err != nil {
		t.Fatalf("GetNode clear: %v", err)
	}
	if len(got.PortMappings) != 0 {
		t.Fatalf("expected empty mappings, got %+v", got.PortMappings)
	}
}

func TestNormalizePortMappings(t *testing.T) {
	in := []PortMapping{
		{ListenPort: 1, PublicPort: 2},
		{ListenPort: 3, PublicPort: 3},
		{ListenPort: 0, PublicPort: 9},
	}
	out := NormalizePortMappings(in)
	if len(out) != 1 || out[0].ListenPort != 1 || out[0].PublicPort != 2 {
		t.Fatalf("%+v", out)
	}
}

func TestNodeInboundNATBindings(t *testing.T) {
	s := openTestStore(t)
	n := &Node{Name: "nat", Address: "10.0.0.8", GRPCPort: 50051, PublicAddress: "node.example.com"}
	if err := s.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	in1 := &InboundConfig{Name: "ss1", Protocol: "shadowsocks", Enabled: true, Params: map[string]any{"port": float64(8443), "method": "aes-128-gcm", "password": "p"}}
	in2 := &InboundConfig{Name: "ss2", Protocol: "shadowsocks", Enabled: true, Params: map[string]any{"port": float64(9000), "method": "aes-128-gcm", "password": "p"}}
	if err := s.CreateInbound(in1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateInbound(in2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeInboundBindings(n.ID, []NodeInboundBinding{
		{InboundID: in1.ID, PublicAddress: "edge.example.com", PublicPort: 443},
		{InboundID: in2.ID, PublicAddress: "", PublicPort: 0},
	}); err != nil {
		t.Fatalf("SetNodeInboundBindings: %v", err)
	}
	atts, err := s.ListNodeInboundAttachments(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("len=%d", len(atts))
	}
	byID := map[string]NodeInboundAttachment{}
	for _, a := range atts {
		byID[a.ID] = a
	}
	if byID[in1.ID].PublicAddress != "edge.example.com" || byID[in1.ID].PublicPort != 443 {
		t.Fatalf("in1 = %+v", byID[in1.ID])
	}
	if byID[in2.ID].PublicAddress != "" || byID[in2.ID].PublicPort != 0 {
		t.Fatalf("in2 = %+v", byID[in2.ID])
	}
	// compat list still works
	ins, err := s.ListInboundsForNode(n.ID)
	if err != nil || len(ins) != 2 {
		t.Fatalf("ListInboundsForNode: %v %#v", err, ins)
	}
}

func TestCreateNodeDefaultsControlMode(t *testing.T) {
	s := openTestStore(t)
	n := &Node{Name: "uplink-edge", Token: "tok", ControlMode: ControlModeUplink}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ControlMode != ControlModeUplink || got.DesiredRuntime != DesiredRuntimeRunning {
		t.Fatalf("mode=%q runtime=%q", got.ControlMode, got.DesiredRuntime)
	}
}

func TestApplyNodeReportWritesAndDropsStale(t *testing.T) {
	s := openTestStore(t)
	n := &Node{Name: "edge", Token: "tok", ControlMode: ControlModeUplink}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	cpu := 12.5
	applied, err := s.ApplyNodeReport(n.ID, NodeReport{
		CollectedAtUnix: 1_700_000_100,
		Status:          "online",
		RuntimeState:    "running",
		ConfigHash:      "abc",
		AgentVersion:    "v1",
		HasCapabilities: true,
		Capabilities:    []string{"uplink-v1"},
		HasMetrics:      true,
		CPUPercent:      cpu,
		Connections:     3,
		UplinkBytes:     10,
		DownlinkBytes:   20,
		MemoryRSSBytes:  1024,
	})
	if err != nil || !applied {
		t.Fatalf("first report applied=%v err=%v", applied, err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "online" || got.RuntimeState != "running" || got.ConfigHash != "abc" {
		t.Fatalf("node = %+v", got)
	}
	if got.UplinkLastSeenUnix != 1_700_000_100 || got.CPUPercent != cpu || got.Connections != 3 {
		t.Fatalf("metrics = %+v", got)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0] != "uplink-v1" {
		t.Fatalf("capabilities = %v", got.Capabilities)
	}

	stale, err := s.ApplyNodeReport(n.ID, NodeReport{
		CollectedAtUnix: 1_700_000_050,
		Status:          "online",
		RuntimeState:    "stopped",
		ConfigHash:      "old",
	})
	if err != nil || stale {
		t.Fatalf("stale applied=%v err=%v", stale, err)
	}
	got, err = s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RuntimeState != "running" || got.ConfigHash != "abc" {
		t.Fatalf("stale overwrote live state: %+v", got)
	}

	if _, err := s.ApplyNodeReport(n.ID, NodeReport{CPUPercent: 101}); err == nil {
		t.Fatal("expected invalid CPU")
	}
}

func TestApplyNodeReportKeepsIdentityWhenOmitted(t *testing.T) {
	s := openTestStore(t)
	n := &Node{Name: "edge", Token: "tok", ControlMode: ControlModeUplink}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	applied, err := s.ApplyNodeReport(n.ID, NodeReport{
		CollectedAtUnix: 1_700_000_300,
		Status:          "online",
		AgentVersion:    "v0.15.12",
		CoreVersion:     "v0.15.12-3-g2ebfece",
		CoreCommit:      "2ebfece",
		ClientType:      "openwrt",
		ClientVersion:   "0.15.12-r2",
		LuciVersion:     "26.288.0734~760600c-1",
	})
	if err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if _, err := s.ApplyNodeReport(n.ID, NodeReport{
		CollectedAtUnix: 1_700_000_301,
		Status:          "online",
		RuntimeState:    "running",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentVersion != "v0.15.12" || got.CoreVersion != "v0.15.12-3-g2ebfece" ||
		got.CoreCommit != "2ebfece" || got.ClientType != "openwrt" ||
		got.ClientVersion != "0.15.12-r2" || got.LuciVersion != "26.288.0734~760600c-1" {
		t.Fatalf("empty report cleared identity: %+v", got)
	}
	if got.RuntimeState != "running" {
		t.Fatalf("runtime = %q", got.RuntimeState)
	}
}

func TestMarkUplinkUnreachableDoesNotClobberNewerReport(t *testing.T) {
	s := openTestStore(t)
	n := &Node{Name: "edge", Token: "tok", ControlMode: ControlModeUplink}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyNodeReport(n.ID, NodeReport{
		CollectedAtUnix: 1_700_000_200,
		Status:          "online",
		RuntimeState:    "running",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUplinkUnreachable(n.ID, "节点超过 45 秒未上报", 1_700_000_199); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "online" || got.RuntimeState != "running" {
		t.Fatalf("newer report was overwritten: %+v", got)
	}

	if err := s.MarkUplinkUnreachable(n.ID, "节点超过 45 秒未上报", 1_700_000_201); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "unreachable" || got.LastError != "节点超过 45 秒未上报" {
		t.Fatalf("stale node not marked: %+v", got)
	}
	if got.RuntimeState != "running" {
		t.Fatalf("runtime clobbered: %+v", got)
	}
}

func TestAgentCommandLeaseAndComplete(t *testing.T) {
	s := openTestStore(t)
	n := &Node{Name: "edge", Token: "tok", ControlMode: ControlModeUplink}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	cmd := &AgentCommand{NodeID: n.ID, Type: "probe", Payload: "{}"}
	if err := s.CreateAgentCommand(cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.ID == "" || cmd.Status != AgentCommandPending || cmd.ExpiresAtUnix == 0 {
		t.Fatalf("create defaults not applied: %+v", cmd)
	}

	// First lease claims the pending command and marks it leased.
	leased, err := s.LeaseAgentCommands(n.ID, 10, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 1 || leased[0].ID != cmd.ID || leased[0].Status != AgentCommandLeased {
		t.Fatalf("lease = %+v", leased)
	}
	if leased[0].Attempt != 1 || leased[0].LeaseExpiresUnix == 0 {
		t.Fatalf("lease bookkeeping = %+v", leased[0])
	}

	// A second immediate lease sees nothing (still within visibility timeout).
	again, err := s.LeaseAgentCommands(n.ID, 10, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("expected no redelivery within lease, got %+v", again)
	}

	// Completing is terminal and idempotent.
	updated, applied, err := s.CompleteAgentCommand(cmd.ID, true, `{"ok":true}`, "")
	if err != nil || !applied {
		t.Fatalf("complete applied=%v err=%v", applied, err)
	}
	if updated.Status != AgentCommandSucceeded || updated.Result != `{"ok":true}` {
		t.Fatalf("completed = %+v", updated)
	}
	_, applied2, err := s.CompleteAgentCommand(cmd.ID, false, "", "late")
	if err != nil {
		t.Fatal(err)
	}
	if applied2 {
		t.Fatal("duplicate completion should be a no-op")
	}
	final, err := s.GetAgentCommand(cmd.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != AgentCommandSucceeded || final.Error != "" {
		t.Fatalf("idempotency broke terminal state: %+v", final)
	}
}

func TestAgentCommandLeaseRedeliversAfterExpiry(t *testing.T) {
	s := openTestStore(t)
	n := &Node{Name: "edge", Token: "tok", ControlMode: ControlModeUplink}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	cmd := &AgentCommand{NodeID: n.ID, Type: "probe"}
	if err := s.CreateAgentCommand(cmd); err != nil {
		t.Fatal(err)
	}
	// leaseSec<=0 falls back to the default; use a negative-effect lease by
	// leasing with a tiny window then forcing re-delivery via a 0 window read.
	leased, err := s.LeaseAgentCommands(n.ID, 10, 1)
	if err != nil || len(leased) != 1 {
		t.Fatalf("first lease = %+v err=%v", leased, err)
	}
	// Rewind the lease so it appears expired, then re-lease.
	if _, err := s.db.Exec(`UPDATE agent_commands SET lease_expires_unix = ? WHERE id = ?`,
		nowUnix()-5, cmd.ID); err != nil {
		t.Fatal(err)
	}
	redelivered, err := s.LeaseAgentCommands(n.ID, 10, 60)
	if err != nil || len(redelivered) != 1 {
		t.Fatalf("redelivery = %+v err=%v", redelivered, err)
	}
	if redelivered[0].Attempt != 2 {
		t.Fatalf("attempt not incremented on redelivery: %+v", redelivered[0])
	}
}

func TestAgentCommandTTLExpiry(t *testing.T) {
	s := openTestStore(t)
	n := &Node{Name: "edge", Token: "tok", ControlMode: ControlModeUplink}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	cmd := &AgentCommand{NodeID: n.ID, Type: "probe", ExpiresAtUnix: nowUnix() - 1}
	if err := s.CreateAgentCommand(cmd); err != nil {
		t.Fatal(err)
	}
	leased, err := s.LeaseAgentCommands(n.ID, 10, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 0 {
		t.Fatalf("expired command should not be leased: %+v", leased)
	}
	got, err := s.GetAgentCommand(cmd.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != AgentCommandExpired {
		t.Fatalf("status = %q, want expired", got.Status)
	}
}
