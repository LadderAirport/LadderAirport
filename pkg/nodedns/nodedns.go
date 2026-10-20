// Package nodedns is the single definition of a node's sing-box 1.12 DNS block.
// The panel converter and the agent default injector both call it, so the four
// DoH upstreams cannot drift apart.
package nodedns

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/ladderairport/pkg/semver"
)

const (
	// TagAli is the first default row, so it is dns.final unless the operator
	// reorders the list. It is not a domestic-only server.
	TagAli = "doh-ali"
	// TagDNSPod is the second default row. sing-box 1.12 does not query it
	// while a server above it is enabled.
	TagDNSPod = "doh-dnspod"
	// TagCloudflare is the third default row.
	TagCloudflare = "doh-cf"
	// TagGoogle is the fourth default row.
	TagGoogle = "doh-google"

	StrategyPreferIPv4 = "prefer_ipv4"
	StrategyPreferIPv6 = "prefer_ipv6"
	StrategyIPv4Only   = "ipv4_only"
	StrategyIPv6Only   = "ipv6_only"

	defaultPath         = "/dns-query"
	defaultCache        = 2048
	defaultSniffTimeout = "300ms"
	maxServers          = 32
)

// Server is one row in the ordered DNS list. The first enabled row is
// sing-box dns.final. Later enabled rows are emitted but not queried, because
// sing-box 1.12 has no DNS failover. A missing Enabled value is treated as
// enabled so older dns_json rows keep working.
type Server struct {
	Tag        string `json:"tag"`
	Type       string `json:"type"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Path       string `json:"path,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	Enabled    *bool  `json:"enabled,omitempty"`
}

// Config is the per-node DNS setting stored in nodes.dns_json and returned by
// the node API. Empty storage parses as Default.
type Config struct {
	Strategy         string   `json:"strategy"`
	Sniff            bool     `json:"sniff"`
	SniffOverride    bool     `json:"sniff_override_destination"`
	SniffTimeout     string   `json:"sniff_timeout"`
	IndependentCache bool     `json:"independent_cache"`
	CacheCapacity    int      `json:"cache_capacity"`
	ReverseMapping   bool     `json:"reverse_mapping"`
	DisableExpire    bool     `json:"disable_expire"`
	DisableCache     bool     `json:"disable_cache"`
	ClientSubnet     string   `json:"client_subnet,omitempty"`
	Servers          []Server `json:"servers"`
}

// EmitOptions controls dial fields on the generated sing-box servers.
type EmitOptions struct {
	// BindInterface is copied onto every DoH server when non-empty.
	// DNS servers are dialed directly; they are never detoured through the
	// direct outbound (that loops).
	BindInterface string
	// SkipBind drops BindInterface. Android nodes cannot honor a chosen NIC.
	SkipBind bool
}

// EmitResult is the pieces a sing-box 1.12 config needs for this DNS setting.
// RuleSet is unused: DNS is not split by domain, so nothing downloads geosite.
type EmitResult struct {
	DNS           map[string]any
	RuleSet       map[string]any
	ChainResolver map[string]any
}

// Default is the prefilled DoH list. Row order is the list order: the first
// enabled row (AliDNS) is final. The other three stay in the list so they can
// be reordered, but 1.12 does not send queries to them.
// Addresses are IPv4 literals verified against the provider certificate, so
// dialing them needs no bootstrap lookup and works with bind_interface.
func Default() Config {
	return Config{
		Strategy:         StrategyPreferIPv4,
		Sniff:            true,
		SniffOverride:    true,
		SniffTimeout:     defaultSniffTimeout,
		IndependentCache: true,
		CacheCapacity:    defaultCache,
		ReverseMapping:   true,
		Servers: []Server{
			enabledServer(TagAli, "https", "223.5.5.5", 443, defaultPath, "dns.alidns.com"),
			enabledServer(TagDNSPod, "https", "1.12.12.12", 443, defaultPath, "doh.pub"),
			enabledServer(TagCloudflare, "https", "1.1.1.1", 443, defaultPath, "cloudflare-dns.com"),
			enabledServer(TagGoogle, "https", "8.8.8.8", 443, defaultPath, "dns.google"),
		},
	}
}

// Parse decodes a stored dns_json value. Empty or whitespace is Default.
func Parse(raw string) (Config, error) {
	if strings.TrimSpace(raw) == "" {
		return Default(), nil
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return Config{}, fmt.Errorf("解析节点 DNS 失败：%w", err)
	}
	if err := cfg.Normalize(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// SupportsSchema reports whether a node's reported sing-box version can accept
// the typed DNS block, route resolve action, and domain_resolver object.
// Empty or unparseable versions return false so older agents keep their old JSON.
func SupportsSchema(singboxVersion string) bool {
	parsed := semver.Parse(singboxVersion)
	if !parsed.Valid {
		return false
	}
	minimum := semver.V{Major: 1, Minor: 12, Valid: true}
	return semver.Compare(parsed, minimum) >= 0
}

// Normalize fills defaults and rejects a config that cannot be emitted.
// A config with no servers is replaced by Default so a zero value does not
// wipe a node's DNS back to an empty block.
func (c *Config) Normalize() error {
	if c == nil {
		return fmt.Errorf("节点 DNS 不能为空")
	}
	if len(c.Servers) == 0 {
		*c = Default()
		return nil
	}
	switch c.Strategy {
	case "", StrategyPreferIPv4:
		c.Strategy = StrategyPreferIPv4
	case StrategyPreferIPv6, StrategyIPv4Only, StrategyIPv6Only:
	default:
		return fmt.Errorf("strategy %q 无效", c.Strategy)
	}
	timeout := strings.TrimSpace(c.SniffTimeout)
	if timeout == "" {
		timeout = defaultSniffTimeout
	}
	parsedTimeout, err := time.ParseDuration(timeout)
	if err != nil || parsedTimeout <= 0 {
		return fmt.Errorf("sniff_timeout %q 无效", c.SniffTimeout)
	}
	c.SniffTimeout = timeout
	if c.CacheCapacity == 0 {
		c.CacheCapacity = defaultCache
	}
	if c.CacheCapacity < 1024 {
		return fmt.Errorf("cache_capacity 必须大于或等于 1024")
	}
	subnet, err := normalizeClientSubnet(c.ClientSubnet)
	if err != nil {
		return err
	}
	c.ClientSubnet = subnet

	if len(c.Servers) > maxServers {
		return fmt.Errorf("DNS 服务器不能超过 %d 台", maxServers)
	}
	seen := make(map[string]struct{}, len(c.Servers))
	enabled := make([]Server, 0, len(c.Servers))
	for i := range c.Servers {
		normalized, err := normalizeServer(c.Servers[i])
		if err != nil {
			return err
		}
		if _, exists := seen[normalized.Tag]; exists {
			return fmt.Errorf("DNS 服务器标签 %q 重复", normalized.Tag)
		}
		seen[normalized.Tag] = struct{}{}
		c.Servers[i] = normalized
		if serverEnabled(normalized) {
			enabled = append(enabled, normalized)
		}
	}
	if len(enabled) == 0 {
		return fmt.Errorf("至少启用一台 DNS 服务器作为 final")
	}
	if err := requireBootstrap(enabled); err != nil {
		return err
	}
	return nil
}

// Emit builds the sing-box dns object and the domain_resolver attached to
// chain outbounds. There are no domain or query-type DNS rules. final and the
// chain resolver are the first enabled server.
func (c Config) Emit(opts EmitOptions) (EmitResult, error) {
	if err := c.Normalize(); err != nil {
		return EmitResult{}, err
	}
	bind := strings.TrimSpace(opts.BindInterface)
	if opts.SkipBind {
		bind = ""
	}
	enabled := enabledServers(c.Servers)
	final := enabled[0].Tag
	bootstrap := bootstrapTag(enabled)
	servers := make([]map[string]any, 0, len(enabled))
	for _, server := range enabled {
		item := map[string]any{
			"type":        server.Type,
			"tag":         server.Tag,
			"server":      server.Server,
			"server_port": server.ServerPort,
		}
		if serverUsesPath(server.Type) {
			item["path"] = server.Path
		}
		if serverUsesTLS(server.Type) {
			item["tls"] = map[string]any{
				"enabled":     true,
				"server_name": server.ServerName,
			}
		}
		if bind != "" {
			item["bind_interface"] = bind
		}
		if !isIPAddress(server.Server) {
			item["domain_resolver"] = map[string]any{
				"server":   bootstrap,
				"strategy": StrategyIPv4Only,
			}
		}
		servers = append(servers, item)
	}
	dns := map[string]any{
		"servers":           servers,
		"final":             final,
		"strategy":          c.Strategy,
		"independent_cache": c.IndependentCache,
		"cache_capacity":    c.CacheCapacity,
		"reverse_mapping":   c.ReverseMapping,
	}
	if c.DisableCache {
		dns["disable_cache"] = true
	}
	if c.DisableExpire {
		dns["disable_expire"] = true
	}
	if c.ClientSubnet != "" {
		dns["client_subnet"] = c.ClientSubnet
	}
	return EmitResult{
		DNS: dns,
		ChainResolver: map[string]any{
			"server":   final,
			"strategy": StrategyIPv4Only,
		},
	}, nil
}

func normalizeServer(server Server) (Server, error) {
	server.Tag = strings.TrimSpace(server.Tag)
	if !validTag(server.Tag) {
		return Server{}, fmt.Errorf("DNS 服务器标签 %q 无效", server.Tag)
	}
	server.Type = strings.ToLower(strings.TrimSpace(server.Type))
	if server.Type == "" {
		server.Type = "https"
	}
	if !validType(server.Type) {
		return Server{}, fmt.Errorf("%s 的类型 %q 无效", server.Tag, server.Type)
	}
	server.Server = strings.Trim(strings.TrimSpace(server.Server), "[]")
	if server.Server == "" || strings.ContainsAny(server.Server, " \t/\\") || strings.Contains(server.Server, "://") {
		return Server{}, fmt.Errorf("%s 的 server 无效", server.Tag)
	}
	if server.ServerPort == 0 {
		server.ServerPort = defaultPort(server.Type)
	}
	if server.ServerPort < 1 || server.ServerPort > 65535 {
		return Server{}, fmt.Errorf("%s 的端口超出有效范围", server.Tag)
	}
	if serverUsesPath(server.Type) {
		server.Path = strings.TrimSpace(server.Path)
		if server.Path == "" {
			server.Path = defaultPath
		}
		if !strings.HasPrefix(server.Path, "/") || strings.ContainsAny(server.Path, " \t") {
			return Server{}, fmt.Errorf("%s 的 path 必须以 / 开头", server.Tag)
		}
	} else {
		server.Path = ""
	}
	if serverUsesTLS(server.Type) {
		server.ServerName = strings.TrimSpace(server.ServerName)
		if server.ServerName == "" || strings.ContainsAny(server.ServerName, " /\\") || strings.Contains(server.ServerName, "://") {
			return Server{}, fmt.Errorf("%s 必须提供 TLS server_name", server.Tag)
		}
	} else {
		server.ServerName = ""
	}
	on := serverEnabled(server)
	server.Enabled = &on
	return server, nil
}

func normalizeClientSubnet(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.Contains(raw, "/") {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !prefix.IsValid() {
			return "", fmt.Errorf("client_subnet 不是合法前缀")
		}
		return prefix.Masked().String(), nil
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil || !addr.IsValid() {
		return "", fmt.Errorf("client_subnet 不是合法 IP 或前缀")
	}
	bits := 32
	if addr.Is6() {
		bits = 128
	}
	return netip.PrefixFrom(addr, bits).String(), nil
}

func enabledServer(tag, kind, server string, port int, path, name string) Server {
	on := true
	return Server{
		Tag: tag, Type: kind, Server: server, ServerPort: port,
		Path: path, ServerName: name, Enabled: &on,
	}
}

func serverEnabled(server Server) bool {
	return server.Enabled == nil || *server.Enabled
}

func enabledServers(servers []Server) []Server {
	out := make([]Server, 0, len(servers))
	for _, server := range servers {
		if serverEnabled(server) {
			out = append(out, server)
		}
	}
	return out
}

func validTag(tag string) bool {
	if tag == "" || len(tag) > 32 {
		return false
	}
	for i, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

func validType(kind string) bool {
	switch kind {
	case "udp", "tcp", "tls", "https", "quic", "h3":
		return true
	default:
		return false
	}
}

func serverUsesTLS(kind string) bool {
	switch kind {
	case "tls", "https", "quic", "h3":
		return true
	default:
		return false
	}
}

func serverUsesPath(kind string) bool {
	return kind == "https" || kind == "h3"
}

func defaultPort(kind string) int {
	switch kind {
	case "udp", "tcp":
		return 53
	case "tls", "quic":
		return 853
	default:
		return 443
	}
}

func requireBootstrap(enabled []Server) error {
	needsBootstrap := false
	for _, server := range enabled {
		if !isIPAddress(server.Server) {
			needsBootstrap = true
			break
		}
	}
	if !needsBootstrap {
		return nil
	}
	if bootstrapTag(enabled) == "" {
		return fmt.Errorf("使用主机名的 DNS 服务器需要另一台启用的 IP 服务器做引导")
	}
	return nil
}

func bootstrapTag(servers []Server) string {
	for _, server := range servers {
		if serverEnabled(server) && isIPAddress(server.Server) {
			return server.Tag
		}
	}
	return ""
}

func isIPAddress(host string) bool {
	return net.ParseIP(strings.TrimSpace(host)) != nil
}
