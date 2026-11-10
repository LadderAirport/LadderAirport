// Package nodeconfig assembles the complete desired sing-box configuration for
// one Agent: its standalone bindings plus, for global route plans that target
// a proxy chain, the chain composed as detour-linked outbounds.
package nodeconfig

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"strings"

	"github.com/ladderairport/panel/internal/converter"
	"github.com/ladderairport/panel/internal/frpconnection"
	"github.com/ladderairport/panel/internal/store"
	"github.com/ladderairport/panel/internal/subscription"
	"github.com/ladderairport/pkg/hashutil"
	"github.com/ladderairport/pkg/nodedns"
)

type Builder struct {
	Store *store.Store
}

type Result struct {
	JSON string
	Hash string
}

func (b *Builder) Build(nodeID string) (Result, error) {
	chains, err := b.Store.ListProxyChains()
	if err != nil {
		return Result{}, err
	}
	return b.BuildWithChains(nodeID, enabledChains(chains))
}

// BuildWithChains builds against an explicit effective chain set. Chains are
// not deployed onto their hops (hops are ordinary standalone bindings); they
// only matter here when an enabled global route plan proxies through one.
func (b *Builder) BuildWithChains(nodeID string, chains []store.ProxyChain) (Result, error) {
	if b == nil || b.Store == nil {
		return Result{}, fmt.Errorf("节点配置构建器尚未配置")
	}
	node, err := b.Store.GetNode(nodeID)
	if err != nil {
		return Result{}, err
	}
	inbounds, err := b.Store.ListInboundsForNode(nodeID)
	if err != nil {
		return Result{}, err
	}
	bindInterface := strings.TrimSpace(node.EgressInterface)
	if strings.EqualFold(strings.TrimSpace(node.ClientType), "android") {
		// Android node builds cannot bind a chosen NIC. Omit the field here
		// so a pushed config does not fail dial; the agent also strips it.
		bindInterface = ""
	}
	inbounds, err = b.applyManagedTLS(nodeID, inbounds)
	if err != nil {
		return Result{}, err
	}
	routeRules, chainOutbounds, err := b.globalRouteRules(nodeID, chains, bindInterface)
	if err != nil {
		return Result{}, err
	}
	convertOpts := converter.ConvertOptions{
		BindInterface: bindInterface,
		AllowEmpty:    true,
		Outbounds:     chainOutbounds,
		RouteRules:    routeRules,
	}
	if nodedns.SupportsSchema(node.SingboxVersion) {
		dnsCfg := node.DNS
		if err := dnsCfg.Normalize(); err != nil {
			return Result{}, fmt.Errorf("节点 DNS：%w", err)
		}
		emitted, err := dnsCfg.Emit(nodedns.EmitOptions{
			BindInterface: bindInterface,
			Failover:      nodedns.SupportsFailover(node.SingboxVersion),
		})
		if err != nil {
			return Result{}, fmt.Errorf("节点 DNS：%w", err)
		}
		convertOpts.DNS = emitted.DNS
		convertOpts.DNSRuleSet = emitted.RuleSet
		convertOpts.ChainDomainResolver = emitted.ChainResolver
		convertOpts.Sniff = dnsCfg.Sniff
		convertOpts.SniffOverride = dnsCfg.SniffOverride
		convertOpts.SniffTimeout = dnsCfg.SniffTimeout
		convertOpts.DropInboundSniff = nodedns.DropsInboundSniffFields(node.SingboxVersion)
		convertOpts.ResolveStrategy = dnsCfg.Strategy
	}
	raw, err := converter.Convert(inbounds, convertOpts)
	if err != nil {
		return Result{}, err
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return Result{}, fmt.Errorf("解析节点配置失败：%w", err)
	}
	converted, _ := document["inbounds"].([]any)
	frpc := map[string]string{}
	frpcPorts := map[string]string{}
	frpcNames := map[string]string{}
	usedLocalPorts := map[int]bool{}
	for _, item := range converted {
		entry, _ := item.(map[string]any)
		if port, ok := entry["listen_port"].(float64); ok {
			usedLocalPorts[int(port)] = true
		}
	}
	convertedIndex := 0
	for _, inbound := range inbounds {
		if !inbound.Enabled {
			continue
		}
		if convertedIndex >= len(converted) {
			return Result{}, fmt.Errorf("FRPC 入站映射数量不一致")
		}
		entry, _ := converted[convertedIndex].(map[string]any)
		convertedIndex++
		frpConfig, frpEnabled, err := frpconnection.ParseParams(inbound.Params)
		if err != nil {
			return Result{}, fmt.Errorf("入站 %q：%w", inbound.Name, err)
		}
		if !frpEnabled {
			continue
		}
		if inbound.Protocol == "hysteria2" || inbound.Protocol == "tuic" {
			return Result{}, fmt.Errorf("入站 %q 是 UDP/QUIC 协议，当前 FRP 直连仅支持 TCP", inbound.Name)
		}
		if entry["transport"] != nil || entry["network"] == "udp" {
			return Result{}, fmt.Errorf("入站 %q 的传输方式暂不支持 FRP TCP 直连", inbound.Name)
		}
		portKey := fmt.Sprintf("%s:%d:%d", frpConfig.ServerAddr, frpConfig.ServerPort, frpConfig.RemotePort)
		if previous, exists := frpcPorts[portKey]; exists {
			return Result{}, fmt.Errorf("入站 %q 与 %q 使用了同一 FRPS 对外端口", inbound.Name, previous)
		}
		frpcPorts[portKey] = inbound.Name
		tag, _ := entry["tag"].(string)
		proxyName := strings.TrimSpace(frpConfig.ProxyName)
		if proxyName == "" {
			proxyName = tag
		}
		nameKey := fmt.Sprintf("%s:%d:%s:%s", frpConfig.ServerAddr, frpConfig.ServerPort, frpConfig.User, proxyName)
		if previous, exists := frpcNames[nameKey]; exists {
			return Result{}, fmt.Errorf("入站 %q 与 %q 使用了同一 FRPS 代理名称", inbound.Name, previous)
		}
		frpcNames[nameKey] = inbound.Name
		localPort, err := allocateFRPLocalPort(tag, usedLocalPorts)
		if err != nil {
			return Result{}, fmt.Errorf("入站 %q：%w", inbound.Name, err)
		}
		usedLocalPorts[localPort] = true
		config := map[string]any{
			"server_addr": frpConfig.ServerAddr, "server_port": frpConfig.ServerPort,
			"remote_port": frpConfig.RemotePort, "token": frpConfig.Token,
			"user": frpConfig.User, "proxy_name": frpConfig.ProxyName,
			"tls_enable":                        frpConfig.TLSEnable,
			"tls_disable_custom_tls_first_byte": frpConfig.TLSDisableCustomTLSFirstByte,
			"local_port":                        localPort,
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			return Result{}, fmt.Errorf("编码入站 %q 的 FRPC 配置失败：%w", inbound.Name, err)
		}
		frpc[tag] = string(encoded)
		entry["listen"] = "127.0.0.1"
		entry["listen_port"] = localPort
	}
	if len(frpc) != 0 {
		document["ladder_frpc"] = frpc
		raw, err = json.Marshal(document)
		if err != nil {
			return Result{}, fmt.Errorf("编码 FRPC 配置失败：%w", err)
		}
	}
	return Result{JSON: string(raw), Hash: hashutil.SHA256Hex(raw)}, nil
}

func allocateFRPLocalPort(tag string, used map[int]bool) (int, error) {
	const first, count = 49152, 16384
	h := fnv.New32a()
	_, _ = h.Write([]byte(tag))
	start := int(h.Sum32() % count)
	for offset := 0; offset < count; offset++ {
		port := first + (start+offset)%count
		if !used[port] {
			return port, nil
		}
	}
	return 0, fmt.Errorf("没有可用的本地高位端口")
}

// globalRouteRules resolves enabled global route plans into converter rules
// for this node. proxy actions route into the target chain composed locally
// as detour-linked outbounds: a node that is itself hop k continues from hop
// k+1 (the chain exit maps to direct); any other node dials the whole chain
// from hop 0. Rules targeting a disabled or unresolvable chain are skipped.
// process_name rules never apply on the agent side (subscription only).
func (b *Builder) globalRouteRules(nodeID string, chains []store.ProxyChain, bindInterface string) ([]converter.RouteRule, []map[string]any, error) {
	plans, err := b.Store.ListEnabledGlobalRoutePlans()
	if err != nil {
		return nil, nil, err
	}
	out := []converter.RouteRule{}
	outbounds := []map[string]any{}
	exitTags := map[string]string{} // chain id -> composed exit tag
	for _, plan := range plans {
		for _, rule := range plan.Rules {
			if !rule.Enabled {
				continue
			}
			if rule.MatchType == "process_name" {
				continue
			}
			resolved := converter.RouteRule{
				MatchType:  rule.MatchType,
				MatchValue: rule.MatchValue,
			}
			switch rule.Action {
			case "direct":
				resolved.Outbound = "direct"
			case "block":
				resolved.Reject = true
			case "proxy":
				tag, ok := exitTags[rule.TargetChainID]
				if !ok {
					chain, found := findEnabledChain(chains, rule.TargetChainID)
					if !found {
						log.Printf("debug: 代理链 %s 未启用，跳过全局路由计划 %q 的规则（%s %s）",
							rule.TargetChainID, plan.Name, rule.MatchType, rule.MatchValue)
						continue
					}
					composed, exit, err := b.composeChainFrom(chain, chainStartForNode(chain, nodeID), bindInterface)
					if err != nil {
						return nil, nil, fmt.Errorf("全局路由计划 %q：%w", plan.Name, err)
					}
					outbounds = append(outbounds, composed...)
					tag = exit
					exitTags[rule.TargetChainID] = tag
				}
				resolved.Outbound = tag
			default:
				continue
			}
			out = append(out, resolved)
		}
	}
	return out, outbounds, nil
}

func findEnabledChain(chains []store.ProxyChain, chainID string) (store.ProxyChain, bool) {
	for _, chain := range chains {
		if chain.ID == chainID && chain.Enabled {
			return chain, true
		}
	}
	return store.ProxyChain{}, false
}

// chainStartForNode returns the first hop this node must dial: the hop after
// its own position when it is on the chain, otherwise the entry hop.
func chainStartForNode(chain store.ProxyChain, nodeID string) int {
	for i, hop := range chain.Hops {
		if hop.NodeID == nodeID {
			return i + 1
		}
	}
	return 0
}

// composeChainFrom builds outbounds for hops [from, len(hops)) where each hop
// dials through the previous one via detour. Only the first outbound dials
// directly, so it alone carries bind_interface. It returns the exit tag, or
// "direct" when from is past the last hop (this node is the chain exit).
func (b *Builder) composeChainFrom(chain store.ProxyChain, from int, bindInterface string) ([]map[string]any, string, error) {
	if from >= len(chain.Hops) {
		return nil, "direct", nil
	}
	out := make([]map[string]any, 0, len(chain.Hops)-from)
	previous := ""
	for i := from; i < len(chain.Hops); i++ {
		ep, err := b.ResolveHopEndpoint(chain, i)
		if err != nil {
			return nil, "", err
		}
		outbound, err := subscription.SingboxOutbound(ep)
		if err != nil {
			return nil, "", fmt.Errorf("代理链 %q 第 %d 跳的出站配置失败：%w", chain.Name, i+1, err)
		}
		tag := ChainOutboundTag(chain.ID, i)
		outbound["tag"] = tag
		if previous == "" {
			if bindInterface != "" {
				outbound["bind_interface"] = bindInterface
			}
		} else {
			outbound["detour"] = previous
		}
		out = append(out, outbound)
		previous = tag
	}
	return out, previous, nil
}

func (b *Builder) applyManagedTLS(nodeID string, inbounds []store.InboundConfig) ([]store.InboundConfig, error) {
	out := make([]store.InboundConfig, len(inbounds))
	copy(out, inbounds)
	idx, err := b.LoadManagedTLSIndex()
	if err != nil {
		return nil, err
	}
	for i := range out {
		resolved, _, _, err := b.ResolveManagedTLSIndexed(idx, nodeID, out[i])
		if err != nil {
			return nil, err
		}
		out[i] = resolved
	}
	return out, nil
}

// ResolveManagedTLS overlays the per-node binding on a copy of inbound and
// returns the managed client-facing hostname when enabled.
func (b *Builder) ResolveManagedTLS(
	nodeID string,
	inbound store.InboundConfig,
) (store.InboundConfig, string, bool, error) {
	bindings, err := b.Store.ListNodeInboundTLSBindings(nodeID)
	if err != nil {
		return inbound, "", false, err
	}
	binding := findManagedBinding(bindings, inbound.ID)
	if binding == nil {
		return inbound, "", false, nil
	}
	domain, err := b.Store.GetManagedDomain(binding.ManagedDomainID)
	if err != nil {
		return inbound, "", false, err
	}
	certificate, err := b.Store.GetProtocolCertificate(binding.CertificateID)
	if err != nil {
		return inbound, "", false, err
	}
	return applyManagedTLSBinding(inbound, nodeID, binding, domain, certificate)
}

// ManagedTLSIndex preloads managed-TLS bindings, domains and certificates so
// bulk resolution (subscription rendering, config builds) avoids
// per-endpoint database queries.
type ManagedTLSIndex struct {
	bindings map[string][]store.NodeInboundTLSBinding // node ID -> bindings
	domains  map[string]*store.ManagedDomain
	certs    map[string]*store.ProtocolCertificate
}

// LoadManagedTLSIndex fetches all bindings, managed domains and protocol
// certificates in three queries.
func (b *Builder) LoadManagedTLSIndex() (*ManagedTLSIndex, error) {
	bindings, err := b.Store.ListAllNodeInboundTLSBindings()
	if err != nil {
		return nil, err
	}
	domainList, err := b.Store.ListManagedDomains()
	if err != nil {
		return nil, err
	}
	certList, err := b.Store.ListProtocolCertificates()
	if err != nil {
		return nil, err
	}
	idx := &ManagedTLSIndex{
		bindings: bindings,
		domains:  make(map[string]*store.ManagedDomain, len(domainList)),
		certs:    make(map[string]*store.ProtocolCertificate, len(certList)),
	}
	for i := range domainList {
		idx.domains[domainList[i].ID] = &domainList[i]
	}
	for i := range certList {
		idx.certs[certList[i].ID] = &certList[i]
	}
	return idx, nil
}

// ResolveManagedTLSIndexed is ResolveManagedTLS served from a preloaded index.
func (b *Builder) ResolveManagedTLSIndexed(
	idx *ManagedTLSIndex,
	nodeID string,
	inbound store.InboundConfig,
) (store.InboundConfig, string, bool, error) {
	binding := findManagedBinding(idx.bindings[nodeID], inbound.ID)
	if binding == nil {
		return inbound, "", false, nil
	}
	domain, ok := idx.domains[binding.ManagedDomainID]
	if !ok {
		return inbound, "", false, fmt.Errorf("托管域名不存在：%s", binding.ManagedDomainID)
	}
	certificate, ok := idx.certs[binding.CertificateID]
	if !ok {
		return inbound, "", false, fmt.Errorf("协议证书不存在：%s", binding.CertificateID)
	}
	return applyManagedTLSBinding(inbound, nodeID, binding, domain, certificate)
}

// findManagedBinding picks the managed-mode binding for inboundID, if any.
func findManagedBinding(bindings []store.NodeInboundTLSBinding, inboundID string) *store.NodeInboundTLSBinding {
	for i := range bindings {
		if bindings[i].InboundID == inboundID && bindings[i].Mode == "managed" {
			return &bindings[i]
		}
	}
	return nil
}

// applyManagedTLSBinding validates ownership/readiness and overlays the
// managed certificate paths onto a copy of inbound.
func applyManagedTLSBinding(
	inbound store.InboundConfig,
	nodeID string,
	binding *store.NodeInboundTLSBinding,
	domain *store.ManagedDomain,
	certificate *store.ProtocolCertificate,
) (store.InboundConfig, string, bool, error) {
	if domain.NodeID != nodeID || certificate.NodeID != nodeID ||
		certificate.ManagedDomainID != domain.ID {
		return inbound, "", false, fmt.Errorf("入站 %s 的托管 TLS 绑定归属不一致", inbound.Name)
	}
	if (certificate.Status != "active" && certificate.Status != "deploying") ||
		certificate.ActiveCertPath == "" || certificate.ActiveKeyPath == "" {
		return inbound, "", false, fmt.Errorf("入站 %s 的托管证书尚未就绪", inbound.Name)
	}
	if !supportsManagedTLS(inbound) {
		return inbound, "", false, fmt.Errorf("入站 %s 的协议 %s 不支持托管 TLS", inbound.Name, inbound.Protocol)
	}
	certPath := certificate.ActiveCertPath
	keyPath := certificate.ActiveKeyPath
	if certificate.Status == "deploying" {
		certPath = certificate.CandidateCertPath
		keyPath = certificate.CandidateKeyPath
	}
	if certPath == "" || keyPath == "" {
		return inbound, "", false, fmt.Errorf("入站 %s 的托管证书文件尚未就绪", inbound.Name)
	}
	params := cloneParams(inbound.Params)
	delete(params, "tls_cert_pem")
	delete(params, "tls_key_pem")
	params["tls_cert_path"] = certPath
	params["tls_key_path"] = keyPath
	params["server_name"] = domain.FQDN
	switch inbound.Protocol {
	case "vless", "vmess":
		params["tls_mode"] = "tls"
	}
	inbound.Params = params
	return inbound, domain.FQDN, true, nil
}

func supportsManagedTLS(inbound store.InboundConfig) bool {
	switch inbound.Protocol {
	case "trojan", "hysteria2", "tuic", "anytls":
		return true
	case "vless", "vmess":
		mode, _ := inbound.Params["tls_mode"].(string)
		return mode != "reality"
	default:
		return false
	}
}

func cloneParams(params map[string]any) map[string]any {
	out := make(map[string]any, len(params)+3)
	for key, value := range params {
		out[key] = value
	}
	return out
}

func enabledChains(chains []store.ProxyChain) []store.ProxyChain {
	out := make([]store.ProxyChain, 0, len(chains))
	for _, chain := range chains {
		if chain.Enabled {
			out = append(out, chain)
		}
	}
	return out
}

func ChainOutboundTag(chainID string, position int) string {
	id := strings.ReplaceAll(strings.ToLower(chainID), "-", "")
	if len(id) > 12 {
		id = id[:12]
	}
	return fmt.Sprintf("chain-%s-hop-%d", id, position)
}

// ResolveHopEndpoint resolves the address and credentials used to reach a hop
// from its consumer (the client for hop zero, otherwise the previous hop).
// The hop references a standalone binding, so NAT/FRP overrides come from
// that binding. Address priority: hop dial_address → FRP server → managed TLS
// hostname → binding public_address → node public_address → node address.
// Port priority: hop dial_port → FRP remote port → binding public_port →
// node port mapping of the listen port.
func (b *Builder) ResolveHopEndpoint(chain store.ProxyChain, position int) (subscription.ProxyEndpoint, error) {
	if position < 0 || position >= len(chain.Hops) {
		return subscription.ProxyEndpoint{}, fmt.Errorf("代理链 %q 的跳点位置超出范围：%d", chain.Name, position+1)
	}
	hop := chain.Hops[position]
	node, err := b.Store.GetNode(hop.NodeID)
	if err != nil {
		return subscription.ProxyEndpoint{}, err
	}
	att, err := b.Store.GetNodeInboundAttachment(hop.NodeID, hop.InboundID)
	if err != nil {
		return subscription.ProxyEndpoint{}, fmt.Errorf("代理链 %q 第 %d 跳：%w", chain.Name, position+1, err)
	}
	if !att.Enabled {
		return subscription.ProxyEndpoint{}, fmt.Errorf("代理链 %q 第 %d 跳的入站已禁用", chain.Name, position+1)
	}
	in, managedHostname, managed, err := b.ResolveManagedTLS(hop.NodeID, att.EffectiveInbound())
	if err != nil {
		return subscription.ProxyEndpoint{}, err
	}
	frp, frpEnabled, err := frpconnection.ParseParams(in.Params)
	if err != nil {
		return subscription.ProxyEndpoint{}, fmt.Errorf("代理链 %q 第 %d 跳：%w", chain.Name, position+1, err)
	}
	address := strings.TrimSpace(hop.DialAddress)
	if address == "" && frpEnabled {
		address = frp.ServerAddr
	}
	if address == "" && managed {
		address = managedHostname
	}
	if address == "" {
		address = strings.TrimSpace(att.PublicAddress)
	}
	if address == "" {
		address = strings.TrimSpace(node.PublicAddress)
	}
	if address == "" {
		address = strings.TrimSpace(node.Address)
	}
	if address == "" {
		return subscription.ProxyEndpoint{}, fmt.Errorf("代理链 %q 第 %d 跳没有拨号地址", chain.Name, position+1)
	}
	port := hop.DialPort
	if port == 0 && frpEnabled {
		port = frp.RemotePort
	}
	if port == 0 && att.PublicPort >= 1 && att.PublicPort <= 65535 {
		port = att.PublicPort
	}
	if port == 0 {
		listenPort, err := paramInt(in.Params, "port")
		if err != nil || listenPort < 1 || listenPort > 65535 {
			return subscription.ProxyEndpoint{}, fmt.Errorf("代理链 %q 第 %d 跳的入站端口无效", chain.Name, position+1)
		}
		port = store.MapPublicPort(node.PortMappings, listenPort)
	}
	skip := hop.TLSSkipVerify
	return subscription.ProxyEndpoint{
		Name:          chain.Name,
		Node:          *node,
		Inbound:       in,
		Server:        address,
		Port:          port,
		Protocol:      in.Protocol,
		Params:        in.Params,
		TLSSkipVerify: &skip,
	}, nil
}

func paramInt(params map[string]any, key string) (int, error) {
	value, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("缺少 %s", key)
	}
	switch n := value.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	case json.Number:
		v, err := n.Int64()
		return int(v), err
	default:
		return 0, fmt.Errorf("%s 无效", key)
	}
}
