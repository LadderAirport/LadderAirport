package proxyuser

import (
	"fmt"
	"sort"

	"github.com/ladderairport/panel/internal/store"
	"github.com/ladderairport/panel/internal/subscription"
)

// Plan maps inbounds to the users admitted on them. A user is admitted to an
// inbound when one of the user's live subscriptions (enabled, not disabled)
// exposes it, either as a standalone inbound or as a hop of an enabled chain
// it includes — the same selection the subscription renderer uses.
type Plan struct {
	everywhere map[string]Identity            // user ID -> identity (IncludeAllInbounds)
	byInbound  map[string]map[string]Identity // inbound ID -> user ID -> identity
}

// BuildPlan computes the admission plan. Users with undecodable secrets are
// reported as errors rather than silently dropped.
func BuildPlan(users []store.ProxyUser, subs []store.Subscription, chains []store.ProxyChain) (*Plan, error) {
	p := &Plan{everywhere: map[string]Identity{}, byInbound: map[string]map[string]Identity{}}
	byID := make(map[string]store.ProxyUser, len(users))
	for _, u := range users {
		byID[u.ID] = u
	}
	chainByID := make(map[string]store.ProxyChain, len(chains))
	for _, c := range chains {
		chainByID[c.ID] = c
	}
	for _, sub := range subs {
		if sub.UserID == "" || !sub.Enabled || sub.Disabled {
			continue
		}
		u, ok := byID[sub.UserID]
		if !ok || !u.Enabled {
			continue
		}
		id, err := ForUser(u)
		if err != nil {
			return nil, err
		}
		if sub.IncludeStandalone {
			if sub.IncludeAllInbounds {
				p.everywhere[u.ID] = id
			} else {
				for _, inboundID := range sub.InboundIDs {
					p.add(inboundID, id)
				}
			}
		}
		addChain := func(c store.ProxyChain) {
			if !c.Enabled {
				return
			}
			for _, hop := range c.Hops {
				p.add(hop.InboundID, id)
			}
		}
		if sub.IncludeAllChains {
			for _, c := range chains {
				addChain(c)
			}
		} else {
			for _, chainID := range sub.ChainIDs {
				if c, ok := chainByID[chainID]; ok {
					addChain(c)
				}
			}
		}
	}
	return p, nil
}

func (p *Plan) add(inboundID string, id Identity) {
	if inboundID == "" {
		return
	}
	m := p.byInbound[inboundID]
	if m == nil {
		m = map[string]Identity{}
		p.byInbound[inboundID] = m
	}
	m[id.Name] = id
}

// UsersFor returns the users admitted on an inbound, sorted by name so the
// generated node config (and its hash) is stable.
func (p *Plan) UsersFor(inboundID string) []Identity {
	if p == nil {
		return nil
	}
	merged := map[string]Identity{}
	for k, v := range p.everywhere {
		merged[k] = v
	}
	for k, v := range p.byInbound[inboundID] {
		merged[k] = v
	}
	out := make([]Identity, 0, len(merged))
	for _, v := range merged {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// InboundUsers is the server-side user list for one inbound.
type InboundUsers struct {
	// ReplaceDefault drops the inbound's shared credential (admission-only).
	ReplaceDefault bool
	Users          []map[string]any
}

// ServerUsers builds the sing-box users for an inbound on a node. ok=false
// means the inbound config needs no change (no admitted users and not
// admission-only, or an inbound that cannot distinguish users).
func ServerUsers(in store.InboundConfig, node store.NodeAdmission, admitted []Identity) (InboundUsers, bool, error) {
	only := AdmissionOnly(in, node)
	if only {
		internal, err := Internal(node)
		if err != nil {
			return InboundUsers{}, false, err
		}
		admitted = append([]Identity{internal}, admitted...)
	} else if len(admitted) == 0 || !UsesUsers(in, false) {
		return InboundUsers{}, false, nil
	}
	out := InboundUsers{ReplaceDefault: only, Users: make([]map[string]any, 0, len(admitted))}
	for _, id := range admitted {
		u, err := ServerUser(in, id)
		if err != nil {
			return InboundUsers{}, false, fmt.Errorf("入站 %q：%w", in.Name, err)
		}
		out.Users = append(out.Users, u)
	}
	return out, true, nil
}

// InternalEndpoint swaps a node-side chain hop onto the panel's internal
// credential when the hop inbound only admits proxy users on its node.
func InternalEndpoint(ep subscription.ProxyEndpoint, admissions map[string]store.NodeAdmission) (subscription.ProxyEndpoint, error) {
	adm := admissions[ep.Node.ID]
	if !AdmissionOnly(ep.Inbound, adm) {
		return ep, nil
	}
	internal, err := Internal(adm)
	if err != nil {
		return ep, err
	}
	params, err := ClientParams(ep.Inbound, ep.Params, internal)
	if err != nil {
		return ep, err
	}
	ep.Params = params
	return ep, nil
}

// ForSubscription rewrites one rendered endpoint for a subscription.
// user == nil means a shared (unbound) subscription. keep=false means the
// endpoint must be omitted: its node is admission-only and the subscription
// has no credential the inbound would accept.
func ForSubscription(ep subscription.ProxyEndpoint, user *Identity, admissions map[string]store.NodeAdmission) (out subscription.ProxyEndpoint, keep bool, err error) {
	only := AdmissionOnly(ep.Inbound, admissions[ep.Node.ID])
	if user != nil && UsesUsers(ep.Inbound, only) {
		params, err := ClientParams(ep.Inbound, ep.Params, *user)
		if err != nil {
			return ep, false, err
		}
		ep.Params = params
		return ep, true, nil
	}
	return ep, !only, nil
}
