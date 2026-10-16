package api

import (
	"strings"

	"github.com/ladderairport/panel/internal/store"
	agentv1 "github.com/ladderairport/proto/gen/go/agent/v1"
)

// nodeCoreVersion is the version compared with the recommended core release.
// Old clients leave core_version empty; agent_version is that core version.
func nodeCoreVersion(n store.Node) string {
	if v := strings.TrimSpace(n.CoreVersion); v != "" {
		return v
	}
	return strings.TrimSpace(n.AgentVersion)
}

// linuxReleaseUpgradeAllowed is false for android and openwrt wrappers.
// An empty client type is an old client and keeps the historical linux upgrade.
func linuxReleaseUpgradeAllowed(clientType string) bool {
	switch strings.ToLower(strings.TrimSpace(clientType)) {
	case "android", "openwrt":
		return false
	default:
		return true
	}
}

// applyPingIdentity copies non-empty identity fields from a ping. Empty fields
// keep the values already stored, matching ApplyNodeReport.
func applyPingIdentity(n *store.Node, ping *agentv1.PingResponse) {
	if n == nil || ping == nil {
		return
	}
	set := func(dst *string, v string) {
		v = strings.TrimSpace(v)
		if v != "" {
			*dst = v
		}
	}
	set(&n.AgentVersion, ping.GetAgentVersion())
	set(&n.SingboxVersion, ping.GetSingboxVersion())
	set(&n.CoreVersion, ping.GetCoreVersion())
	set(&n.CoreCommit, ping.GetCoreCommit())
	set(&n.ClientType, ping.GetClientType())
	set(&n.ClientVersion, ping.GetClientVersion())
	set(&n.LuciVersion, ping.GetLuciVersion())
}

func cachedProbeResponse(node *store.Node, message string) map[string]any {
	if node == nil {
		return map[string]any{"ok": false, "message": message}
	}
	return map[string]any{
		"ok":             node.Status == "online",
		"status":         node.Status,
		"agent_version":  node.AgentVersion,
		"core_version":   node.CoreVersion,
		"core_commit":    node.CoreCommit,
		"client_type":    node.ClientType,
		"client_version": node.ClientVersion,
		"luci_version":   node.LuciVersion,
		"capabilities":   node.Capabilities,
		"message":        message,
	}
}
