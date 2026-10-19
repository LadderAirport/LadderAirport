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
	// TagAli serves geosite-cn names that are not the .cn TLD (for example baidu.com).
	TagAli = "doh-ali"
	// TagDNSPod serves domain suffix cn. It is matched before geosite-cn.
	TagDNSPod = "doh-dnspod"
	// TagCloudflare is dns.final (non-CN queries other than AAAA/HTTPS) and the
	// chain-outbound domain_resolver.
	TagCloudflare = "doh-cf"
	// TagGoogle serves non-CN AAAA and HTTPS queries. sing-box 1.12 has no DNS
	// failover, so this tag is a query partition, not a backup for Cloudflare.
	TagGoogle = "doh-google"

	RuleSetGeositeCN = "geosite-cn"
	GeositeCNURL     = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs"

	StrategyPreferIPv4 = "prefer_ipv4"
	StrategyPreferIPv6 = "prefer_ipv6"
	StrategyIPv4Only   = "ipv4_only"
	StrategyIPv6Only   = "ipv6_only"

	defaultPath         = "/dns-query"
	defaultPort         = 443
	defaultCache        = 2048
	defaultSniffTimeout = "300ms"
)

// Server is one editable DoH upstream. Tag and Type are fixed; the operator
// edits the dial address, port, path, and TLS server name.
type Server struct {
	Tag        string `json:"tag"`
	Type       string `json:"type"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Path       string `json:"path"`
	ServerName string `json:"server_name"`
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
type EmitResult struct {
	DNS           map[string]any
	RuleSet       map[string]any
	ChainResolver map[string]any
}

// Default is the prefilled DoH set: two domestic and two overseas.
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
			{Tag: TagAli, Type: "https", Server: "223.5.5.5", ServerPort: defaultPort, Path: defaultPath, ServerName: "dns.alidns.com"},
			{Tag: TagDNSPod, Type: "https", Server: "1.12.12.12", ServerPort: defaultPort, Path: defaultPath, ServerName: "doh.pub"},
			{Tag: TagCloudflare, Type: "https", Server: "1.1.1.1", ServerPort: defaultPort, Path: defaultPath, ServerName: "cloudflare-dns.com"},
			{Tag: TagGoogle, Type: "https", Server: "8.8.8.8", ServerPort: defaultPort, Path: defaultPath, ServerName: "dns.google"},
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

	if len(c.Servers) != 4 {
		return fmt.Errorf("必须提供 4 个 DNS 服务器（%s、%s、%s、%s）", TagAli, TagDNSPod, TagCloudflare, TagGoogle)
	}
	byTag := make(map[string]Server, 4)
	for _, server := range c.Servers {
		normalized, err := normalizeServer(server)
		if err != nil {
			return err
		}
		if _, exists := byTag[normalized.Tag]; exists {
			return fmt.Errorf("DNS 服务器标签 %q 重复", normalized.Tag)
		}
		byTag[normalized.Tag] = normalized
	}
	ordered := make([]Server, 0, 4)
	for _, tag := range []string{TagAli, TagDNSPod, TagCloudflare, TagGoogle} {
		server, ok := byTag[tag]
		if !ok {
			return fmt.Errorf("缺少 DNS 服务器 %s", tag)
		}
		ordered = append(ordered, server)
	}
	if bootstrapTag(ordered) == "" {
		return fmt.Errorf("至少有一个 DNS 服务器必须是 IP，用来引导 DoH 域名")
	}
	c.Servers = ordered
	return nil
}

// Emit builds the sing-box dns object, the geosite-cn rule set, and the
// domain_resolver attached to chain outbounds.
func (c Config) Emit(opts EmitOptions) (EmitResult, error) {
	if err := c.Normalize(); err != nil {
		return EmitResult{}, err
	}
	bind := strings.TrimSpace(opts.BindInterface)
	if opts.SkipBind {
		bind = ""
	}
	bootstrap := bootstrapTag(c.Servers)
	servers := make([]map[string]any, 0, len(c.Servers))
	for _, server := range c.Servers {
		item := map[string]any{
			"type":        "https",
			"tag":         server.Tag,
			"server":      server.Server,
			"server_port": server.ServerPort,
			"path":        server.Path,
			"tls": map[string]any{
				"enabled":     true,
				"server_name": server.ServerName,
			},
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
		"servers": servers,
		"rules": []map[string]any{
			{"domain_suffix": []string{"cn"}, "action": "route", "server": TagDNSPod},
			{"rule_set": []string{RuleSetGeositeCN}, "action": "route", "server": TagAli},
			{"query_type": []string{"AAAA", "HTTPS"}, "action": "route", "server": TagGoogle},
		},
		"final":             TagCloudflare,
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
		RuleSet: map[string]any{
			"type":            "remote",
			"tag":             RuleSetGeositeCN,
			"format":          "binary",
			"url":             GeositeCNURL,
			"download_detour": "direct",
		},
		ChainResolver: map[string]any{
			"server":   TagCloudflare,
			"strategy": StrategyIPv4Only,
		},
	}, nil
}

func normalizeServer(server Server) (Server, error) {
	server.Tag = strings.TrimSpace(server.Tag)
	switch server.Tag {
	case TagAli, TagDNSPod, TagCloudflare, TagGoogle:
	default:
		return Server{}, fmt.Errorf("DNS 服务器标签 %q 无效", server.Tag)
	}
	server.Type = strings.TrimSpace(server.Type)
	if server.Type == "" {
		server.Type = "https"
	}
	if server.Type != "https" {
		return Server{}, fmt.Errorf("%s 的类型必须是 https", server.Tag)
	}
	server.Server = strings.TrimSpace(server.Server)
	if server.Server == "" || strings.ContainsAny(server.Server, " \t/") {
		return Server{}, fmt.Errorf("%s 的 server 无效", server.Tag)
	}
	if server.ServerPort == 0 {
		server.ServerPort = defaultPort
	}
	if server.ServerPort < 1 || server.ServerPort > 65535 {
		return Server{}, fmt.Errorf("%s 的端口超出有效范围", server.Tag)
	}
	server.Path = strings.TrimSpace(server.Path)
	if server.Path == "" {
		server.Path = defaultPath
	}
	if !strings.HasPrefix(server.Path, "/") || strings.ContainsAny(server.Path, " \t") {
		return Server{}, fmt.Errorf("%s 的 path 必须以 / 开头", server.Tag)
	}
	server.ServerName = strings.TrimSpace(server.ServerName)
	if server.ServerName == "" || strings.ContainsAny(server.ServerName, " /\\") || strings.Contains(server.ServerName, "://") {
		return Server{}, fmt.Errorf("%s 必须提供 TLS server_name", server.Tag)
	}
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

func bootstrapTag(servers []Server) string {
	for _, tag := range []string{TagCloudflare, TagGoogle, TagAli, TagDNSPod} {
		for _, server := range servers {
			if server.Tag == tag && isIPAddress(server.Server) {
				return tag
			}
		}
	}
	return ""
}

func isIPAddress(host string) bool {
	return net.ParseIP(strings.TrimSpace(host)) != nil
}
