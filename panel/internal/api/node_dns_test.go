package api_test

import (
	"net/http"
	"testing"
)

func TestNodeDNSAPIPrefillsAndRejects(t *testing.T) {
	ts, client, _ := newTestServer(t, nil, nil)
	resp := login(t, client, ts.URL, "admin")
	resp.Body.Close()

	resp, body := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/nodes", map[string]any{
		"name": "dns-node", "address": "192.0.2.9", "grpc_port": 50051,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d body = %v", resp.StatusCode, body)
	}
	dns, _ := body["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	if len(servers) != 4 || dns["final"] != nil && dns["strategy"] != "prefer_ipv4" {
		t.Fatalf("dns = %v", dns)
	}
	if servers[0].(map[string]any)["tag"] != "doh-ali" || servers[2].(map[string]any)["server"] != "1.1.1.1" {
		t.Fatalf("servers = %v", servers)
	}
	id, _ := body["id"].(string)
	servers[2].(map[string]any)["server"] = "one.one.one.one"
	reordered := []any{servers[2], servers[0], servers[1], servers[3], map[string]any{
		"tag": "quad9", "type": "udp", "server": "9.9.9.9", "server_port": 53, "enabled": true,
	}}
	servers[0].(map[string]any)["enabled"] = false
	dns["servers"] = reordered
	resp, body = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+id, map[string]any{"dns": dns})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d body = %v", resp.StatusCode, body)
	}
	updated := body["dns"].(map[string]any)
	updatedServers := updated["servers"].([]any)
	if len(updatedServers) != 5 || updatedServers[0].(map[string]any)["server"] != "one.one.one.one" {
		t.Fatalf("updated = %v", updated)
	}
	if updatedServers[1].(map[string]any)["enabled"] != false || updatedServers[4].(map[string]any)["type"] != "udp" {
		t.Fatalf("updated servers = %v", updatedServers)
	}
	for _, item := range updatedServers {
		row := item.(map[string]any)
		row["server"] = "dns.example"
		row["enabled"] = true
		if row["type"] == "udp" {
			row["type"] = "https"
			row["server_name"] = "dns.example"
			row["path"] = "/dns-query"
		}
	}
	dns["servers"] = updatedServers
	resp, body = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+id, map[string]any{"dns": dns})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("all-hostname status = %d body = %v", resp.StatusCode, body)
	}
	dns["strategy"] = "round-robin"
	resp, body = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+id, map[string]any{"dns": dns})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid strategy status = %d body = %v", resp.StatusCode, body)
	}
}
