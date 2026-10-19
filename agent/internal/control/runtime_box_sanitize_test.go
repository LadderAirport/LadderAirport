//go:build android || with_android

package control

import (
	stdjson "encoding/json"
	"testing"
)

func TestSanitizeAndroidConfig(t *testing.T) {
	input := `{
		"outbounds": [
			{"type": "direct", "tag": "direct", "bind_interface": "wlan0"},
			{"type": "dns", "tag": "dns-out"}
		],
		"inbounds": [
			{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 1080, "bind_interface": "wlan0"}
		],
		"dns": {
			"servers": [
				{"type": "https", "tag": "doh-cf", "server": "1.1.1.1", "server_port": 443, "bind_interface": "wlan0"}
			]
		},
		"route": {
			"default_interface": "rmnet0"
		}
	}`
	output, err := sanitizeAndroidConfig(input)
	if err != nil {
		t.Fatalf("sanitizeAndroidConfig failed: %v", err)
	}

	r := NewBoxRuntime(t.TempDir())
	opts, err := r.parseOptions(output)
	if err != nil {
		t.Fatalf("parseOptions on sanitized config failed: %v", err)
	}

	if opts.Route != nil && opts.Route.DefaultInterface != "" {
		t.Errorf("expected default_interface to be removed, got %s", opts.Route.DefaultInterface)
	}
	var raw map[string]any
	if err := stdjson.Unmarshal([]byte(output), &raw); err != nil {
		t.Fatalf("unmarshal sanitized output: %v", err)
	}
	obs := raw["outbounds"].([]any)
	ob0 := obs[0].(map[string]any)
	if _, ok := ob0["bind_interface"]; ok {
		t.Errorf("expected bind_interface to be removed from outbound")
	}
	ibs := raw["inbounds"].([]any)
	ib0 := ibs[0].(map[string]any)
	if _, ok := ib0["bind_interface"]; ok {
		t.Errorf("expected bind_interface to be removed from inbound")
	}
	dns := raw["dns"].(map[string]any)
	server := dns["servers"].([]any)[0].(map[string]any)
	if _, ok := server["bind_interface"]; ok {
		t.Errorf("expected bind_interface to be removed from dns server")
	}
}
