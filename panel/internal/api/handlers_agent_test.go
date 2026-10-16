package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ladderairport/panel/internal/store"
)

func TestAgentReportAndConfigSyncReuseHTTPBearer(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	node := &store.Node{
		Name:        "edge",
		Token:       "node-secret",
		ControlMode: store.ControlModeUplink,
		Status:      "pending",
	}
	if err := st.CreateNode(node); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&Server{Store: st, Secret: []byte("test-secret")}).Handler())
	defer server.Close()

	reportBody, _ := json.Marshal(map[string]any{
		"node_id":           node.ID,
		"collected_at_unix": time.Now().Unix(),
		"runtime_state":     "running",
		"config_hash":       "cfg-1",
		"agent_version":     "v-test",
		"core_version":      "v0.15.12-3-g2ebfece",
		"core_commit":       "2ebfece",
		"client_type":       "android",
		"client_version":    "1.1.0",
		"capabilities":      []string{"uplink-v1"},
		"connections":       2,
		"uplink_bytes":      10,
		"downlink_bytes":    20,
		"cpu_percent":       3.5,
		"memory_rss_bytes":  4096,
	})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/agent/report", bytes.NewReader(reportBody))
	req.Header.Set("Authorization", "Bearer node-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report status = %d", resp.StatusCode)
	}

	got, err := st.GetNode(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "online" || got.RuntimeState != "running" || got.ConfigHash != "cfg-1" {
		t.Fatalf("report not applied: %+v", got)
	}
	if got.UplinkLastSeenUnix == 0 || got.Connections != 2 {
		t.Fatalf("metrics not applied: %+v", got)
	}
	if got.CoreVersion != "v0.15.12-3-g2ebfece" || got.CoreCommit != "2ebfece" ||
		got.ClientType != "android" || got.ClientVersion != "1.1.0" || got.LuciVersion != "" {
		t.Fatalf("identity not applied: %+v", got)
	}

	wrong, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/agent/report", bytes.NewReader(reportBody))
	wrong.Header.Set("Authorization", "Bearer other")
	wrong.Header.Set("Content-Type", "application/json")
	wrongResp, err := http.DefaultClient.Do(wrong)
	if err != nil {
		t.Fatal(err)
	}
	defer wrongResp.Body.Close()
	if wrongResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", wrongResp.StatusCode)
	}

	syncBody, _ := json.Marshal(map[string]any{
		"node_id":             node.ID,
		"applied_config_hash": "",
		"applied_frps_hash":   "",
	})
	syncReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/agent/config-sync", bytes.NewReader(syncBody))
	syncReq.Header.Set("Authorization", "Bearer node-secret")
	syncReq.Header.Set("Content-Type", "application/json")
	syncResp, err := http.DefaultClient.Do(syncReq)
	if err != nil {
		t.Fatal(err)
	}
	defer syncResp.Body.Close()
	if syncResp.StatusCode != http.StatusOK {
		t.Fatalf("config-sync status = %d", syncResp.StatusCode)
	}
	var first agentConfigSyncResponse
	if err := json.NewDecoder(syncResp.Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	if !first.Changed || first.ConfigJSON == "" || first.ConfigHash == "" || first.DesiredState != store.DesiredRuntimeRunning {
		t.Fatalf("first sync = %+v", first)
	}

	againBody, _ := json.Marshal(map[string]any{
		"node_id":             node.ID,
		"applied_config_hash": first.ConfigHash,
		"applied_frps_hash":   first.FRPSHash,
	})
	againReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/agent/config-sync", bytes.NewReader(againBody))
	againReq.Header.Set("Authorization", "Bearer node-secret")
	againReq.Header.Set("Content-Type", "application/json")
	againResp, err := http.DefaultClient.Do(againReq)
	if err != nil {
		t.Fatal(err)
	}
	defer againResp.Body.Close()
	if againResp.StatusCode != http.StatusOK {
		t.Fatalf("unchanged sync status = %d", againResp.StatusCode)
	}
	var second agentConfigSyncResponse
	if err := json.NewDecoder(againResp.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	if second.Changed || second.ConfigJSON != "" || second.DesiredState != store.DesiredRuntimeRunning {
		t.Fatalf("unchanged sync = %+v", second)
	}

	headReq, _ := http.NewRequest(http.MethodHead, server.URL+"/api/v1/agent/config-sync?node_id="+node.ID, nil)
	headReq.Header.Set("Authorization", "Bearer node-secret")
	headResp, err := http.DefaultClient.Do(headReq)
	if err != nil {
		t.Fatal(err)
	}
	defer headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d", headResp.StatusCode)
	}
	if headResp.Header.Get(headerConfigHash) != first.ConfigHash {
		t.Fatalf("HEAD hash = %q, want %q", headResp.Header.Get(headerConfigHash), first.ConfigHash)
	}
	if headResp.Header.Get(headerDesiredState) != store.DesiredRuntimeRunning {
		t.Fatalf("HEAD desired = %q", headResp.Header.Get(headerDesiredState))
	}

	getReq, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/agent/config-sync?node_id="+node.ID, nil)
	getReq.Header.Set("Authorization", "Bearer node-secret")
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", getResp.StatusCode)
	}
	var meta agentConfigHeadResponse
	if err := json.NewDecoder(getResp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if meta.ConfigHash != first.ConfigHash || meta.DesiredState != store.DesiredRuntimeRunning {
		t.Fatalf("GET meta = %+v", meta)
	}

	wrongHead, _ := http.NewRequest(http.MethodHead, server.URL+"/api/v1/agent/config-sync?node_id="+node.ID, nil)
	wrongHead.Header.Set("Authorization", "Bearer other")
	wrongHeadResp, err := http.DefaultClient.Do(wrongHead)
	if err != nil {
		t.Fatal(err)
	}
	defer wrongHeadResp.Body.Close()
	if wrongHeadResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong HEAD token status = %d", wrongHeadResp.StatusCode)
	}
}

// TestAgentCommandQueueLifecycle exercises the full uplink command path over
// HTTP: enqueue -> node long-poll lease -> node result -> no redelivery, plus
// the per-node Bearer gate on the poll endpoint.
func TestAgentCommandQueueLifecycle(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	node := &store.Node{
		Name:        "edge",
		Token:       "node-secret",
		ControlMode: store.ControlModeUplink,
		Status:      "pending",
	}
	if err := st.CreateNode(node); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&Server{Store: st, Secret: []byte("test-secret")}).Handler())
	defer server.Close()

	cmd := &store.AgentCommand{NodeID: node.ID, Type: cmdInterfaces, Payload: "{}"}
	if err := st.CreateAgentCommand(cmd); err != nil {
		t.Fatal(err)
	}

	// A node long-poll leases the pending command.
	pollURL := server.URL + "/api/v1/agent/commands?node_id=" + node.ID + "&wait=2s"
	pollReq, _ := http.NewRequest(http.MethodGet, pollURL, nil)
	pollReq.Header.Set("Authorization", "Bearer node-secret")
	pollResp, err := http.DefaultClient.Do(pollReq)
	if err != nil {
		t.Fatal(err)
	}
	defer pollResp.Body.Close()
	if pollResp.StatusCode != http.StatusOK {
		t.Fatalf("poll status = %d", pollResp.StatusCode)
	}
	var polled agentCommandsResponse
	if err := json.NewDecoder(pollResp.Body).Decode(&polled); err != nil {
		t.Fatal(err)
	}
	if len(polled.Commands) != 1 || polled.Commands[0].ID != cmd.ID || polled.Commands[0].Type != cmdInterfaces {
		t.Fatalf("poll = %+v", polled.Commands)
	}

	// A wrong token is rejected before any command is leased.
	wrongPoll, _ := http.NewRequest(http.MethodGet, pollURL, nil)
	wrongPoll.Header.Set("Authorization", "Bearer nope")
	wrongPollResp, err := http.DefaultClient.Do(wrongPoll)
	if err != nil {
		t.Fatal(err)
	}
	defer wrongPollResp.Body.Close()
	if wrongPollResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong poll token status = %d", wrongPollResp.StatusCode)
	}

	// The node posts a terminal result.
	resultBody, _ := json.Marshal(agentCommandResultRequest{OK: true, Result: `{"interfaces":[]}`})
	resultReq, _ := http.NewRequest(http.MethodPost,
		server.URL+"/api/v1/agent/commands/"+cmd.ID+"/result", bytes.NewReader(resultBody))
	resultReq.Header.Set("Authorization", "Bearer node-secret")
	resultReq.Header.Set("Content-Type", "application/json")
	resultResp, err := http.DefaultClient.Do(resultReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resultResp.Body.Close()
	if resultResp.StatusCode != http.StatusOK {
		t.Fatalf("result status = %d", resultResp.StatusCode)
	}

	// The stored command reflects the terminal outcome. (The browser-facing
	// GET /api/v1/commands/{id} is admin-session gated and covered elsewhere.)
	got, err := st.GetAgentCommand(cmd.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.AgentCommandSucceeded || got.Result != `{"interfaces":[]}` {
		t.Fatalf("command read = %+v", got)
	}

	// A subsequent poll must not redeliver the terminal command.
	againReq, _ := http.NewRequest(http.MethodGet,
		server.URL+"/api/v1/agent/commands?node_id="+node.ID+"&wait=0", nil)
	againReq.Header.Set("Authorization", "Bearer node-secret")
	againResp, err := http.DefaultClient.Do(againReq)
	if err != nil {
		t.Fatal(err)
	}
	defer againResp.Body.Close()
	var again agentCommandsResponse
	if err := json.NewDecoder(againResp.Body).Decode(&again); err != nil {
		t.Fatal(err)
	}
	if len(again.Commands) != 0 {
		t.Fatalf("terminal command redelivered: %+v", again.Commands)
	}
}

// TestUplinkInterfacesEnqueuesCommand verifies a live-RPC handler queues a
// command and answers 202 + command_id when the target node is uplink.
func TestUplinkInterfacesEnqueuesCommand(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	node := &store.Node{Name: "edge", Token: "tok", ControlMode: store.ControlModeUplink}
	if err := st.CreateNode(node); err != nil {
		t.Fatal(err)
	}

	s := &Server{Store: st}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/"+node.ID+"/interfaces", nil)
	req.SetPathValue("id", node.ID)
	w := httptest.NewRecorder()
	s.handleNodeInterfaces(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	var body struct {
		Queued    bool   `json:"queued"`
		CommandID string `json:"command_id"`
		Type      string `json:"type"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Queued || body.CommandID == "" || body.Type != cmdInterfaces {
		t.Fatalf("response = %+v", body)
	}
	stored, err := st.GetAgentCommand(body.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Type != cmdInterfaces || stored.Status != store.AgentCommandPending {
		t.Fatalf("stored command = %+v", stored)
	}
}
