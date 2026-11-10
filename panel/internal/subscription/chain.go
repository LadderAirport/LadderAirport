package subscription

import (
	"fmt"
	"strings"
)

// ChainSourceName is the display group for composed proxy chains.
const ChainSourceName = "链式代理"

// Chain is one proxy chain to compose client-side: Hops are ordered entry
// first, each already resolved to the address its predecessor dials.
type Chain struct {
	Name string
	Hops []ProxyEndpoint
}

// AppendChains appends each chain as detour-linked endpoints: hop i dials
// through hop i-1 (Clash dialer-proxy / sing-box detour). Only the exit hop is
// selectable and carries the chain name; intermediate hops are Hidden and named
// "<chain>-跳N". Names are made unique against eps (case-insensitive) so
// references never collide with merged standalone/external endpoints.
//
// Chains must be appended after MergeEndpoints: merge dedupes external endpoints and
// renames, which would drop shared hops and break detour references.
func AppendChains(eps []ProxyEndpoint, chains []Chain) []ProxyEndpoint {
	taken := make(map[string]bool, len(eps))
	for _, ep := range eps {
		taken[strings.ToLower(ep.Name)] = true
	}
	unique := func(base string) string {
		base = SanitizeName(base)
		name := base
		for n := 2; taken[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		taken[strings.ToLower(name)] = true
		return name
	}
	for _, chain := range chains {
		if len(chain.Hops) == 0 {
			continue
		}
		previous := ""
		last := len(chain.Hops) - 1
		for i, hop := range chain.Hops {
			if i == last {
				hop.Name = unique(chain.Name)
				hop.SourceName = ChainSourceName
			} else {
				hop.Name = unique(fmt.Sprintf("%s-跳%d", chain.Name, i+1))
				hop.Hidden = true
			}
			hop.SourceID = ""
			hop.Detour = previous
			eps = append(eps, hop)
			previous = hop.Name
		}
	}
	return eps
}
