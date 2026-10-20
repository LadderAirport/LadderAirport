package api_test

import (
	"net/http"
	"strings"
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
	if len(servers) != 4 || dns["strategy"] != "prefer_ipv4" {
		t.Fatalf("dns = %v", dns)
	}
	if servers[0].(map[string]any)["name"] != "doh-ali" || servers[2].(map[string]any)["url"] != "https://1.1.1.1/dns-query" {
		t.Fatalf("servers = %v", servers)
	}
	id, _ := body["id"].(string)
	reordered := []any{
		map[string]any{"name": "doh-cf", "url": "https://1.1.1.1/dns-query"},
		servers[0], servers[1], servers[3],
		map[string]any{"name": "quad9", "url": "9.9.9.9"},
	}
	dns["servers"] = reordered
	resp, body = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+id, map[string]any{"dns": dns})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d body = %v", resp.StatusCode, body)
	}
	updated := body["dns"].(map[string]any)
	updatedServers := updated["servers"].([]any)
	if len(updatedServers) != 5 || updatedServers[0].(map[string]any)["url"] != "https://1.1.1.1/dns-query" {
		t.Fatalf("updated = %v", updated)
	}
	if updatedServers[4].(map[string]any)["url"] != "udp://9.9.9.9" || updatedServers[4].(map[string]any)["name"] != "quad9" {
		t.Fatalf("bare IP was not stored as udp: %v", updatedServers[4])
	}

	bad := []any{map[string]any{"name": "bad", "url": "udp://dns.google"}}
	resp, body = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+id, map[string]any{
		"dns": map[string]any{"servers": bad},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad url status = %d body = %v", resp.StatusCode, body)
	}
	if !strings.Contains(mustError(t, body), "第 1 行") {
		t.Fatalf("expected a per-row error, body = %v", body)
	}

	resp, body = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+id, map[string]any{
		"dns": map[string]any{
			"strategy": "round-robin",
			"servers":  []any{map[string]any{"name": "cf", "url": "https://one.one.one.one/dns-query"}},
		},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid strategy status = %d body = %v", resp.StatusCode, body)
	}

	resp, body = doJSON(t, client, http.MethodPut, ts.URL+"/api/v1/nodes/"+id, map[string]any{
		"dns": map[string]any{
			"servers": []any{map[string]any{"name": "cf", "url": "https://one.one.one.one/dns-query"}},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hostname-only status = %d body = %v", resp.StatusCode, body)
	}
	saved := body["dns"].(map[string]any)["servers"].([]any)
	if len(saved) != 1 || saved[0].(map[string]any)["url"] != "https://one.one.one.one/dns-query" {
		t.Fatalf("hostname-only saved = %v", saved)
	}
}

func mustError(t *testing.T, body map[string]any) string {
	t.Helper()
	for _, key := range []string{"error", "message"} {
		if text, ok := body[key].(string); ok && text != "" {
			return text
		}
	}
	t.Fatalf("response has no error text: %v", body)
	return ""
}
