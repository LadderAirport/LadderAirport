package subscription

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// DroppedEndpoint records an external endpoint removed as a duplicate.
type DroppedEndpoint struct {
	Name        string // name of the dropped endpoint
	DuplicateOf string // name of the kept endpoint with identical config
}

// MergeEndpoints concatenates local then external endpoints, drops unusable
// placeholders and external duplicates, then uniquifies names.
// See MergeEndpointsWithReport for the dedup rules.
func MergeEndpoints(local, external []ProxyEndpoint) []ProxyEndpoint {
	out, _ := MergeEndpointsWithReport(local, external)
	return out
}

// MergeEndpointsWithReport is MergeEndpoints that also reports which external
// endpoints were dropped as duplicates (names are pre-uniquify).
//
// Dedup rules:
//   - Local endpoints are never deduped against each other: one node may serve
//     several inbounds on the same port (e.g. hysteria2/UDP + VLESS/TCP on 443).
//   - An external endpoint is dropped only when its full client config
//     (protocol, server, port, credentials, TLS/SNI, reality keys, ...) equals
//     a local endpoint or an earlier external one. Names and the
//     skip-cert-verify flag are ignored. No DNS resolution is involved, so
//     CDN/relay nodes sharing an IP but differing by SNI or credentials survive.
func MergeEndpointsWithReport(local, external []ProxyEndpoint) ([]ProxyEndpoint, []DroppedEndpoint) {
	local = filterDialableEndpoints(local)
	external = filterDialableEndpoints(external)
	out := make([]ProxyEndpoint, 0, len(local)+len(external))
	seen := make(map[string]string, len(local)+len(external)) // identity -> kept name
	for _, ep := range local {
		id := endpointIdentity(ep)
		if _, ok := seen[id]; !ok {
			seen[id] = ep.Name
		}
		out = append(out, ep)
	}
	var dropped []DroppedEndpoint
	for _, ep := range external {
		id := endpointIdentity(ep)
		if kept, ok := seen[id]; ok {
			dropped = append(dropped, DroppedEndpoint{Name: ep.Name, DuplicateOf: kept})
			continue
		}
		seen[id] = ep.Name
		out = append(out, ep)
	}
	return uniquifyNames(out), dropped
}

// DedupeWarnings formats dropped duplicates as user-facing warnings.
func DedupeWarnings(dropped []DroppedEndpoint) []string {
	if len(dropped) == 0 {
		return nil
	}
	out := make([]string, 0, len(dropped))
	for _, d := range dropped {
		out = append(out, fmt.Sprintf("去重：%s 与 %s 配置完全相同，已移除", d.Name, d.DuplicateOf))
	}
	return out
}

// endpointIdentity returns a canonical key for the endpoint's client config.
// It reuses the sing-box outbound rendering so local inbounds (server-side
// params, e.g. reality private keys) and parsed external proxies normalize to
// the same shape. Tag, detour and TLS insecure are excluded.
func endpointIdentity(ep ProxyEndpoint) string {
	ep.Server = normalizeHost(ep.Server)
	ep.Name = ""
	ep.Detour = ""
	o, err := SingboxOutbound(ep)
	if err != nil {
		// Unrenderable endpoints are dropped at render time anyway; key on
		// everything we have so distinct ones never collide.
		return fmt.Sprintf("invalid|%s|%s|%d|%v", ep.Protocol, ep.Server, ep.Port, ep.Params)
	}
	delete(o, "tag")
	delete(o, "detour")
	if tls, ok := o["tls"].(map[string]any); ok {
		cp := make(map[string]any, len(tls))
		for k, v := range tls {
			if k != "insecure" {
				cp[k] = v
			}
		}
		if sn, ok := cp["server_name"].(string); ok {
			cp["server_name"] = strings.ToLower(strings.TrimSpace(sn))
		}
		o["tls"] = cp
	}
	b, err := json.Marshal(o) // map keys are sorted → canonical
	if err != nil {
		return fmt.Sprintf("%v", o) // fmt also prints maps in key order
	}
	return string(b)
}

// prefixExternalName builds "{source}-{proxy}" and sanitizes.
func prefixExternalName(sourceName, proxyName string) string {
	return sanitizeName(fmt.Sprintf("%s-%s", sourceName, proxyName))
}

func normalizeHost(server string) string {
	s := strings.TrimSpace(server)
	if s == "" {
		return ""
	}
	// Strip optional brackets for IPv6 literals like [::1]
	if strings.HasPrefix(s, "[") {
		if i := strings.Index(s, "]"); i > 0 {
			s = s[1:i]
		}
	}
	// If someone passed host:port, keep host only (defensive).
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// isDialableServer reports whether server is usable as a client dial target.
// Drops empty, unspecified (0.0.0.0 / ::), and common placeholder hosts used by
// airport "notice" nodes.
func isDialableServer(server string) bool {
	host := normalizeHost(server)
	if host == "" {
		return false
	}
	switch host {
	case "0.0.0.0", "::", "::0", "0", "localhost", "local":
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
			return false
		}
	}
	return true
}

// filterDialableEndpoints drops endpoints with unusable server or port.
func filterDialableEndpoints(eps []ProxyEndpoint) []ProxyEndpoint {
	if len(eps) == 0 {
		return eps
	}
	out := make([]ProxyEndpoint, 0, len(eps))
	for _, ep := range eps {
		if ep.Port < 1 || ep.Port > 65535 {
			continue
		}
		if !isDialableServer(ep.Server) {
			continue
		}
		out = append(out, ep)
	}
	return out
}

func uniquifyNames(eps []ProxyEndpoint) []ProxyEndpoint {
	seen := map[string]int{}
	for i := range eps {
		base := eps[i].Name
		if base == "" {
			base = "proxy"
			eps[i].Name = base
		}
		if n, ok := seen[strings.ToLower(eps[i].Name)]; ok {
			// append -2, -3, ...
			for {
				n++
				candidate := fmt.Sprintf("%s-%d", base, n)
				if _, exists := seen[strings.ToLower(candidate)]; !exists {
					eps[i].Name = candidate
					seen[strings.ToLower(candidate)] = 1
					seen[strings.ToLower(base)] = n
					break
				}
			}
		} else {
			seen[strings.ToLower(eps[i].Name)] = 1
		}
	}
	return eps
}
