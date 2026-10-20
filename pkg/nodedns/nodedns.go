// Package nodedns is the single definition of a node's sing-box 1.12 DNS block.
// The panel stores each row as a name plus a URL. Emit parses that URL into the
// typed server sing-box 1.12 expects.
package nodedns

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ladderairport/pkg/semver"
)

const (
	// NameAli is the first default row, so it is dns.final unless reordered.
	NameAli = "doh-ali"
	// NameDNSPod is the second default row. sing-box 1.12 does not query it
	// while a row above it exists.
	NameDNSPod = "doh-dnspod"
	// NameCloudflare is the third default row.
	NameCloudflare = "doh-cf"
	// NameGoogle is the fourth default row.
	NameGoogle = "doh-google"

	// SystemResolverTag is injected only when every row is a hostname, so
	// those hostnames can be resolved by the host resolver.
	SystemResolverTag = "dns-system"

	StrategyPreferIPv4 = "prefer_ipv4"
	StrategyPreferIPv6 = "prefer_ipv6"
	StrategyIPv4Only   = "ipv4_only"
	StrategyIPv6Only   = "ipv6_only"

	defaultPath         = "/dns-query"
	defaultCache        = 2048
	defaultSniffTimeout = "300ms"
	maxServers          = 32
)

// Server is one row in the ordered DNS list stored in nodes.dns_json.
// The first row is sing-box dns.final. Later rows are emitted but not queried,
// because sing-box 1.12 has no DNS failover.
type Server struct {
	Name string `json:"name"`
	URL  string `json:"url"`
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
	// BindInterface is copied onto every generated DNS server when non-empty,
	// including a system resolver added for hostname bootstrap.
	// DNS servers are dialed directly; they are never detoured through the
	// direct outbound (that loops).
	BindInterface string
	// SkipBind drops BindInterface. Android nodes cannot honor a chosen NIC.
	SkipBind bool
}

// EmitResult is the pieces a sing-box 1.12 config needs for this DNS setting.
// RuleSet stays empty: DNS is not split by domain.
type EmitResult struct {
	DNS           map[string]any
	RuleSet       map[string]any
	ChainResolver map[string]any
}

type dialServer struct {
	Name       string
	Type       string
	Host       string
	Port       int
	Path       string
	ServerName string
	IP         bool
}

// Default is the prefilled DoH list. All four URLs use IPv4 literals whose
// certificates include that IP, so dialing them needs no bootstrap lookup and
// works with bind_interface. The first row (AliDNS) is final.
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
			{Name: NameAli, URL: "https://223.5.5.5/dns-query"},
			{Name: NameDNSPod, URL: "https://1.12.12.12/dns-query"},
			{Name: NameCloudflare, URL: "https://1.1.1.1/dns-query"},
			{Name: NameGoogle, URL: "https://8.8.8.8/dns-query"},
		},
	}
}

// Parse decodes a stored dns_json value. Empty or whitespace is Default.
// Older rows shaped as type/server/port/path are rewritten to name plus URL.
// A legacy row with enabled false is dropped.
func Parse(raw string) (Config, error) {
	if strings.TrimSpace(raw) == "" {
		return Default(), nil
	}
	var doc storedConfig
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return Config{}, fmt.Errorf("解析节点 DNS 失败：%w", err)
	}
	cfg := doc.config()
	servers, err := migrateServers(doc.Servers)
	if err != nil {
		return Config{}, err
	}
	cfg.Servers = servers
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

// Normalize fills defaults, canonicalizes each URL, and rejects a config that
// cannot be emitted. A config with no servers is replaced by Default so a zero
// value does not wipe a node's DNS back to an empty block.
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
	for i := range c.Servers {
		dialed, err := normalizeServer(c.Servers[i])
		if err != nil {
			return rowErr(i, c.Servers[i].Name, err)
		}
		if _, exists := seen[dialed.Name]; exists {
			return rowErr(i, dialed.Name, fmt.Errorf("名称重复"))
		}
		seen[dialed.Name] = struct{}{}
		c.Servers[i] = Server{Name: dialed.Name, URL: formatURL(dialed)}
	}
	return nil
}

// Emit builds the sing-box dns object and the domain_resolver attached to
// chain outbounds. There are no domain or query-type DNS rules. final and the
// chain resolver are the first row.
//
// A hostname URL gets domain_resolver pointed at the first IP-literal row.
// When the list has no IP, Emit adds a type=local server named dns-system and
// uses that instead. The local resolver is the host's system resolver.
func (c Config) Emit(opts EmitOptions) (EmitResult, error) {
	if err := c.Normalize(); err != nil {
		return EmitResult{}, err
	}
	bind := strings.TrimSpace(opts.BindInterface)
	if opts.SkipBind {
		bind = ""
	}
	dialed := make([]dialServer, 0, len(c.Servers))
	for i, server := range c.Servers {
		parsed, err := parseURL(server.URL)
		if err != nil {
			return EmitResult{}, rowErr(i, server.Name, err)
		}
		parsed.Name = server.Name
		dialed = append(dialed, parsed)
	}
	final := dialed[0].Name
	bootstrap := firstIPName(dialed)
	useSystem := false
	for _, server := range dialed {
		if !server.IP {
			useSystem = bootstrap == ""
			break
		}
	}
	if useSystem {
		bootstrap = SystemResolverTag
	}
	servers := make([]map[string]any, 0, len(dialed)+1)
	for _, server := range dialed {
		item := map[string]any{
			"type":        server.Type,
			"tag":         server.Name,
			"server":      server.Host,
			"server_port": server.Port,
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
		if !server.IP {
			item["domain_resolver"] = map[string]any{
				"server":   bootstrap,
				"strategy": StrategyIPv4Only,
			}
		}
		servers = append(servers, item)
	}
	if useSystem {
		local := map[string]any{
			"type": "local",
			"tag":  SystemResolverTag,
		}
		if bind != "" {
			local["bind_interface"] = bind
		}
		servers = append(servers, local)
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

type storedConfig struct {
	Strategy         string            `json:"strategy"`
	Sniff            bool              `json:"sniff"`
	SniffOverride    bool              `json:"sniff_override_destination"`
	SniffTimeout     string            `json:"sniff_timeout"`
	IndependentCache bool              `json:"independent_cache"`
	CacheCapacity    int               `json:"cache_capacity"`
	ReverseMapping   bool              `json:"reverse_mapping"`
	DisableExpire    bool              `json:"disable_expire"`
	DisableCache     bool              `json:"disable_cache"`
	ClientSubnet     string            `json:"client_subnet"`
	Servers          []json.RawMessage `json:"servers"`
}

func (s storedConfig) config() Config {
	return Config{
		Strategy:         s.Strategy,
		Sniff:            s.Sniff,
		SniffOverride:    s.SniffOverride,
		SniffTimeout:     s.SniffTimeout,
		IndependentCache: s.IndependentCache,
		CacheCapacity:    s.CacheCapacity,
		ReverseMapping:   s.ReverseMapping,
		DisableExpire:    s.DisableExpire,
		DisableCache:     s.DisableCache,
		ClientSubnet:     s.ClientSubnet,
	}
}

type legacyServer struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Tag        string `json:"tag"`
	Type       string `json:"type"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Path       string `json:"path"`
	ServerName string `json:"server_name"`
	Enabled    *bool  `json:"enabled"`
}

func migrateServers(raw []json.RawMessage) ([]Server, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]Server, 0, len(raw))
	for i, item := range raw {
		var row legacyServer
		if err := json.Unmarshal(item, &row); err != nil {
			return nil, rowErr(i, "", err)
		}
		if strings.TrimSpace(row.URL) != "" || (strings.TrimSpace(row.Server) == "" && strings.TrimSpace(row.Type) == "") {
			name := firstNonEmpty(row.Name, row.Tag)
			out = append(out, Server{Name: name, URL: strings.TrimSpace(row.URL)})
			continue
		}
		if row.Enabled != nil && !*row.Enabled {
			continue
		}
		name := firstNonEmpty(row.Name, row.Tag)
		if strings.TrimSpace(name) == "" {
			return nil, rowErr(i, "", fmt.Errorf("缺少名称"))
		}
		dialed := dialServer{
			Name:       name,
			Type:       strings.ToLower(strings.TrimSpace(row.Type)),
			Host:       strings.Trim(strings.TrimSpace(row.Server), "[]"),
			Port:       row.ServerPort,
			Path:       strings.TrimSpace(row.Path),
			ServerName: strings.TrimSpace(row.ServerName),
		}
		if dialed.Type == "" {
			dialed.Type = "https"
		}
		if !validType(dialed.Type) {
			return nil, rowErr(i, name, fmt.Errorf("类型 %q 无效", dialed.Type))
		}
		if dialed.Port == 0 {
			dialed.Port = defaultPort(dialed.Type)
		}
		if serverUsesPath(dialed.Type) && dialed.Path == "" {
			dialed.Path = defaultPath
		}
		dialed.IP = isIPAddress(dialed.Host)
		if serverUsesTLS(dialed.Type) && dialed.ServerName == "" {
			dialed.ServerName = dialed.Host
		}
		out = append(out, Server{Name: name, URL: formatURL(dialed)})
	}
	return out, nil
}

func normalizeServer(server Server) (dialServer, error) {
	server.Name = strings.TrimSpace(server.Name)
	if !validTag(server.Name) {
		return dialServer{}, fmt.Errorf("名称 %q 无效", server.Name)
	}
	if server.Name == SystemResolverTag {
		return dialServer{}, fmt.Errorf("名称 %s 留给系统解析", SystemResolverTag)
	}
	dialed, err := parseURL(server.URL)
	if err != nil {
		return dialServer{}, err
	}
	dialed.Name = server.Name
	return dialed, nil
}

func parseURL(raw string) (dialServer, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return dialServer{}, fmt.Errorf("URL 不能为空")
	}
	if !strings.Contains(raw, "://") {
		return parseBareIP(raw)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return dialServer{}, fmt.Errorf("URL 无法解析")
	}
	if parsed.User != nil {
		return dialServer{}, fmt.Errorf("URL 不能带用户名或密码")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return dialServer{}, fmt.Errorf("URL 不能带查询参数或片段")
	}
	kind := strings.ToLower(parsed.Scheme)
	if !validType(kind) {
		return dialServer{}, fmt.Errorf("协议 %q 无效，可用 udp、tcp、tls、https、quic、h3", kind)
	}
	host := parsed.Hostname()
	if host == "" {
		return dialServer{}, fmt.Errorf("缺少主机")
	}
	ip := isIPAddress(host)
	if (kind == "udp" || kind == "tcp") && !ip {
		return dialServer{}, fmt.Errorf("%s 的地址必须是 IP", kind)
	}
	if !ip && !validHostname(host) {
		return dialServer{}, fmt.Errorf("主机 %q 无效", host)
	}
	port, err := parsePort(parsed.Port(), kind)
	if err != nil {
		return dialServer{}, err
	}
	path := parsed.EscapedPath()
	if path == "/" {
		path = ""
	}
	if serverUsesPath(kind) {
		if path == "" {
			path = defaultPath
		}
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \t") {
			return dialServer{}, fmt.Errorf("路径必须以 / 开头")
		}
	} else if path != "" {
		return dialServer{}, fmt.Errorf("%s 不能带路径", kind)
	}
	dialed := dialServer{Type: kind, Host: host, Port: port, Path: path, IP: ip}
	if serverUsesTLS(kind) {
		dialed.ServerName = host
	}
	return dialed, nil
}

func parseBareIP(raw string) (dialServer, error) {
	host := raw
	portText := ""
	if strings.HasPrefix(raw, "[") {
		if h, p, err := net.SplitHostPort(raw); err == nil {
			host, portText = h, p
		}
	} else if h, p, err := net.SplitHostPort(raw); err == nil && isIPAddress(h) {
		// IPv6 without brackets also contains colons. Only split when the
		// part before the last colon is itself an IP, so 2001:db8::1 stays intact.
		host, portText = h, p
	}
	host = strings.Trim(host, "[]")
	if !isIPAddress(host) {
		return dialServer{}, fmt.Errorf("没有协议时只能写 IP，主机名请写成 https://、tls://、quic:// 或 h3://")
	}
	port, err := parsePort(portText, "udp")
	if err != nil {
		return dialServer{}, err
	}
	return dialServer{Type: "udp", Host: host, Port: port, IP: true}, nil
}

func parsePort(raw, kind string) (int, error) {
	if raw == "" {
		return defaultPort(kind), nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("端口超出有效范围")
	}
	return port, nil
}

func formatURL(server dialServer) string {
	host := server.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	authority := host
	if server.Port != defaultPort(server.Type) {
		authority += ":" + strconv.Itoa(server.Port)
	}
	if serverUsesPath(server.Type) {
		path := server.Path
		if path == "" {
			path = defaultPath
		}
		return server.Type + "://" + authority + path
	}
	return server.Type + "://" + authority
}

func rowErr(index int, name string, err error) error {
	if err == nil {
		return nil
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("第 %d 行：%s", index+1, err.Error())
	}
	return fmt.Errorf("第 %d 行「%s」：%s", index+1, strings.TrimSpace(name), err.Error())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstIPName(servers []dialServer) string {
	for _, server := range servers {
		if server.IP {
			return server.Name
		}
	}
	return ""
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

func validHostname(host string) bool {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " \t/\\") {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return false
		}
		for i, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case r == '-' && i > 0 && i < len(label)-1:
			default:
				return false
			}
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

func isIPAddress(host string) bool {
	return net.ParseIP(strings.TrimSpace(host)) != nil
}
