package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/ladderairport/panel/internal/nodeconfig"
	"github.com/ladderairport/panel/internal/store"
	"github.com/ladderairport/panel/internal/subscription"
)

func (s *Server) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListSubscriptions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]subView, 0, len(list))
	for i := range list {
		out = append(out, s.enrichSub(r, &list[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

type createSubBody struct {
	Name               string   `json:"name"`
	Format             string   `json:"format"`
	InboundIDs         []string `json:"inbound_ids"`
	IncludeAllInbounds *bool    `json:"include_all_inbounds"`
	IncludeStandalone  *bool    `json:"include_standalone"`
	ChainIDs           []string `json:"chain_ids"`
	IncludeAllChains   *bool    `json:"include_all_chains"`
	ExternalSourceIDs  []string `json:"external_source_ids"`
	UserID             string   `json:"user_id"`
	Enabled            *bool    `json:"enabled"`
}

func (s *Server) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	var body createSubBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "必须提供名称")
		return
	}
	format := strings.ToLower(strings.TrimSpace(body.Format))
	token, err := randomToken(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	// Backward compatibility: legacy clients use an empty inbound_ids array for
	// "all local inbounds". New clients send include_all_inbounds explicitly so
	// false + [] can represent an external-only subscription.
	includeAll := len(body.InboundIDs) == 0
	if body.IncludeAllInbounds != nil {
		includeAll = *body.IncludeAllInbounds
	}
	settings, _ := s.Store.GetSettings()
	migrated := settings != nil && settings.ChainSubscriptionMigrated
	includeStandalone := !migrated
	if body.IncludeStandalone != nil {
		includeStandalone = *body.IncludeStandalone
	}
	includeAllChains := migrated
	if body.IncludeAllChains != nil {
		includeAllChains = *body.IncludeAllChains
	}
	userID, err := s.validateSubscriptionUser(body.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sub := &store.Subscription{
		UserID:             userID,
		Name:               body.Name,
		Format:             format,
		Token:              token,
		InboundIDs:         body.InboundIDs,
		IncludeAllInbounds: includeAll,
		IncludeStandalone:  includeStandalone,
		ChainIDs:           body.ChainIDs,
		IncludeAllChains:   includeAllChains,
		Enabled:            enabled,
	}
	if err := s.Store.CreateSubscription(sub); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.ExternalSourceIDs != nil {
		if err := s.Store.SetSubscriptionExternalSources(sub.ID, body.ExternalSourceIDs); err != nil {
			// Association replacement is transactional. Remove the just-created
			// subscription as well so a 400 response never leaves a hidden row behind.
			if cleanupErr := s.Store.DeleteSubscription(sub.ID); cleanupErr != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("%v（清理失败：%v）", err, cleanupErr))
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if sub.UserID != "" {
		s.scheduleAdmissionReapply()
	}
	writeJSON(w, http.StatusCreated, s.enrichSub(r, sub))
}

func (s *Server) handleUpdateSubscription(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	existing, err := s.Store.GetSubscription(id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body struct {
		Name               *string  `json:"name"`
		Format             *string  `json:"format"`
		InboundIDs         []string `json:"inbound_ids"`
		IncludeAllInbounds *bool    `json:"include_all_inbounds"`
		IncludeStandalone  *bool    `json:"include_standalone"`
		ChainIDs           []string `json:"chain_ids"`
		IncludeAllChains   *bool    `json:"include_all_chains"`
		ExternalSourceIDs  []string `json:"external_source_ids"`
		RoutePlanID        *string  `json:"route_plan_id"`
		UserID             *string  `json:"user_id"`
		Enabled            *bool    `json:"enabled"`
		Rotate             bool     `json:"rotate_token"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err)
		return
	}
	before := *existing
	before.InboundIDs = append([]string{}, existing.InboundIDs...)
	before.ChainIDs = append([]string{}, existing.ChainIDs...)
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		existing.Name = *body.Name
	}
	if body.Format != nil {
		existing.Format = strings.ToLower(strings.TrimSpace(*body.Format))
	}
	if body.InboundIDs != nil {
		existing.InboundIDs = body.InboundIDs
		if body.IncludeAllInbounds == nil {
			// Preserve legacy update semantics: [] meant all, a non-empty list meant selected.
			existing.IncludeAllInbounds = len(body.InboundIDs) == 0
		}
	}
	if body.IncludeAllInbounds != nil {
		existing.IncludeAllInbounds = *body.IncludeAllInbounds
	}
	if body.IncludeStandalone != nil {
		existing.IncludeStandalone = *body.IncludeStandalone
	}
	if body.ChainIDs != nil {
		existing.ChainIDs = body.ChainIDs
		if body.IncludeAllChains == nil {
			existing.IncludeAllChains = len(body.ChainIDs) == 0
		}
	}
	if body.IncludeAllChains != nil {
		existing.IncludeAllChains = *body.IncludeAllChains
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	if body.RoutePlanID != nil {
		planID := strings.TrimSpace(*body.RoutePlanID)
		if planID != "" {
			plan, err := s.Store.GetRoutePlan(planID)
			if err != nil {
				if isNotFound(err) {
					writeError(w, http.StatusBadRequest, fmt.Sprintf("路由计划不存在：%s", planID))
					return
				}
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if plan.Scope != "subscription" {
				writeError(w, http.StatusBadRequest, "只能绑定订阅级路由计划（scope=subscription）")
				return
			}
		}
		existing.RoutePlanID = planID
	}
	if body.UserID != nil {
		userID, err := s.validateSubscriptionUser(*body.UserID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		existing.UserID = userID
	}
	if body.Rotate {
		tok, err := randomToken(16)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		existing.Token = tok
	}
	if err := s.Store.UpdateSubscription(existing); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.ExternalSourceIDs != nil {
		if err := s.Store.SetSubscriptionExternalSources(id, body.ExternalSourceIDs); err != nil {
			if rollbackErr := s.Store.UpdateSubscription(&before); rollbackErr != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("%v（回滚失败：%v）", err, rollbackErr))
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if before.UserID != "" || existing.UserID != "" {
		s.scheduleAdmissionReapply()
	}
	updated, _ := s.Store.GetSubscription(id)
	writeJSON(w, http.StatusOK, s.enrichSub(r, updated))
}

func (s *Server) handleDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	old, _ := s.Store.GetSubscription(id)
	if err := s.Store.DeleteSubscription(id); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if old != nil && old.UserID != "" {
		s.scheduleAdmissionReapply()
	}
	w.WriteHeader(http.StatusNoContent)
}

func detectFormat(r *http.Request) string {
	if r == nil {
		return "v2ray"
	}
	q := r.URL.Query()
	// 1. Explicit query parameter check: format, flag, target
	for _, key := range []string{"format", "flag", "target"} {
		if val := strings.ToLower(strings.TrimSpace(q.Get(key))); val != "" {
			switch val {
			case "clash", "mihomo", "clashmeta":
				return "clash"
			case "singbox", "sing-box":
				return "singbox"
			case "v2ray", "v2rayn", "v2rayng", "base64", "links":
				return "v2ray"
			}
		}
	}
	// Check boolean flag presence in query string
	if q.Has("clash") {
		return "clash"
	}
	if q.Has("singbox") || q.Has("sing-box") {
		return "singbox"
	}
	if q.Has("v2ray") {
		return "v2ray"
	}

	// 2. User-Agent header inspection
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	if ua != "" {
		for _, kw := range []string{"clash", "mihomo", "stash", "openclash"} {
			if strings.Contains(ua, kw) {
				return "clash"
			}
		}
		for _, kw := range []string{"sing-box", "singbox", "sbox", "sfi", "sfa", "sfo", "sfm"} {
			if strings.Contains(ua, kw) {
				return "singbox"
			}
		}
		for _, kw := range []string{"v2ray", "v2rayn", "v2rayng", "shadowrocket", "quantumult", "surge", "nekobox", "passwall"} {
			if strings.Contains(ua, kw) {
				return "v2ray"
			}
		}
	}

	// 3. Fallback format
	return "v2ray"
}

func parseUseDomain(r *http.Request) bool {
	if r == nil {
		return true
	}
	q := r.URL.Query()
	for _, key := range []string{"use_domain", "domain", "fqdn", "use_fqdn"} {
		if val := strings.ToLower(strings.TrimSpace(q.Get(key))); val != "" {
			switch val {
			case "false", "0", "no", "off":
				return false
			case "true", "1", "yes", "on":
				return true
			}
		}
	}
	if q.Get("no_domain") == "true" || q.Get("no_domain") == "1" {
		return false
	}
	return true
}

func (s *Server) handlePreviewSubscription(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	sub, err := s.Store.GetSubscription(id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	format := detectFormat(r)
	useDomain := parseUseDomain(r)
	body, ctype, err := s.renderSubscription(r.Context(), sub, format, useDomain)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// handlePublicSubscription serves GET /sub/{token} without admin auth.
func (s *Server) handlePublicSubscription(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeError(w, http.StatusNotFound, "订阅不存在")
		return
	}
	sub, err := s.Store.GetSubscriptionByToken(token)
	if err != nil {
		writeError(w, http.StatusNotFound, "订阅不存在")
		return
	}
	if sub.Disabled {
		// Disabled subscriptions are indistinguishable from unknown tokens.
		writeError(w, http.StatusNotFound, "订阅不存在")
		return
	}
	if !sub.Enabled {
		writeError(w, http.StatusForbidden, "订阅已禁用")
		return
	}
	if sub.UserID != "" {
		// A disabled or deleted user loses the link along with node access.
		if u, err := s.Store.GetProxyUser(sub.UserID); err != nil || !u.Enabled {
			writeError(w, http.StatusNotFound, "订阅不存在")
			return
		}
	}
	format := detectFormat(r)
	useDomain := parseUseDomain(r)
	body, ctype, err := s.renderSubscription(r.Context(), sub, format, useDomain)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Profile-Update-Interval", "24")
	w.Header().Set("Content-Disposition", contentDisposition(subFilename(sub, format)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) renderSubscription(ctx context.Context, sub *store.Subscription, format string, useDomain bool) ([]byte, string, error) {
	nodes, err := s.Store.ListNodes()
	if err != nil {
		return nil, "", err
	}
	// Batch-prefetch attachments for all nodes (avoids one query per node).
	nodeAttachments, err := s.Store.ListAllNodeInboundAttachments()
	if err != nil {
		return nil, "", err
	}
	local := []subscription.ProxyEndpoint{}
	if sub.IncludeStandalone && (sub.IncludeAllInbounds || len(sub.InboundIDs) > 0) {
		filter := sub.InboundIDs
		if sub.IncludeAllInbounds {
			filter = nil
		}
		local, err = subscription.CollectEndpointsFromAttachments(nodes, nodeAttachments, filter)
		if err != nil {
			return nil, "", err
		}
		nodeManagedDomains := map[string]string{}
		if mds, err := s.Store.ListManagedDomains(); err == nil {
			for _, md := range mds {
				if md.Enabled && strings.TrimSpace(md.FQDN) != "" && md.NodeID != "" {
					nodeManagedDomains[md.NodeID] = strings.TrimSpace(md.FQDN)
				}
			}
		}

		builder := &nodeconfig.Builder{Store: s.Store}
		// Preload TLS bindings/domains/certificates once for all endpoints.
		tlsIndex, err := builder.LoadManagedTLSIndex()
		if err != nil {
			return nil, "", err
		}
		for i := range local {
			nodeID := local[i].Node.ID
			boundDomain := nodeManagedDomains[nodeID]

			resolved, hostname, managed, err := builder.ResolveManagedTLSIndexed(
				tlsIndex, nodeID, local[i].Inbound,
			)
			if err != nil {
				return nil, "", err
			}
			if managed && hostname != "" {
				verify := false
				local[i].Inbound = resolved
				local[i].Params = resolved.Params
				if useDomain {
					local[i].Server = hostname
				} else if local[i].Node.Address != "" {
					local[i].Server = strings.TrimSpace(local[i].Node.Address)
				}
				local[i].TLSSkipVerify = &verify
			} else if useDomain && boundDomain != "" && !subscription.IsDomainHost(local[i].Server) {
				local[i].Server = boundDomain
			} else if !useDomain && subscription.IsDomainHost(local[i].Server) && local[i].Node.Address != "" {
				local[i].Server = strings.TrimSpace(local[i].Node.Address)
			}
		}
	}
	chains, err := s.subscriptionChains(sub)
	if err != nil {
		return nil, "", err
	}
	local, chains, err = s.applySubscriptionCredentials(sub, local, chains)
	if err != nil {
		return nil, "", err
	}

	var external []subscription.ProxyEndpoint
	if s.Aggregator != nil {
		sources, err := s.Store.ListExternalSourcesForSubscription(sub.ID)
		if err != nil {
			return nil, "", err
		}
		// EndpointsForSources soft-fails individual sources.
		external, _ = s.Aggregator.EndpointsForSources(ctx, sources)
	}
	eps, dropped := subscription.MergeEndpointsWithReport(local, external)
	if len(dropped) > 0 {
		log.Printf("订阅 %q：移除 %d 个配置重复的外部节点：%s",
			sub.Name, len(dropped), strings.Join(subscription.DedupeWarnings(dropped), "；"))
	}
	eps = subscription.AppendChains(eps, chains)
	if len(eps) == 0 {
		return nil, "", fmt.Errorf("没有可用的代理端点，请检查节点地址、入站关联和外部源")
	}

	// Inject the bound subscription-scope route plan (dangling, disabled or
	// non-subscription-scope bindings are ignored).
	planRules := []store.RoutePlanRule{}
	if sub.RoutePlanID != "" {
		if plan, err := s.Store.GetRoutePlan(sub.RoutePlanID); err == nil &&
			plan.Enabled && plan.Scope == "subscription" {
			planRules = plan.Rules
		}
	}

	switch format {
	case "clash":
		b, err := subscription.RenderClashWithRules(eps, planRules)
		return b, "text/yaml; charset=utf-8", err
	case "singbox":
		b, err := subscription.RenderSingboxWithRules(eps, planRules)
		return b, "application/json; charset=utf-8", err
	case "v2ray":
		b, err := subscription.RenderV2ray(eps)
		return b, "text/plain; charset=utf-8", err
	default:
		b, err := subscription.RenderV2ray(eps)
		return b, "text/plain; charset=utf-8", err
	}
}

// subscriptionChains resolves every hop of the enabled chains this
// subscription exposes. The client composes them (dialer-proxy / detour), so
// each hop resolves to the address its predecessor dials.
func (s *Server) subscriptionChains(sub *store.Subscription) ([]subscription.Chain, error) {
	if !sub.IncludeAllChains && len(sub.ChainIDs) == 0 {
		return nil, nil
	}
	chains, err := s.Store.ListProxyChains()
	if err != nil {
		return nil, err
	}
	filter := map[string]bool{}
	for _, id := range sub.ChainIDs {
		filter[id] = true
	}
	builder := &nodeconfig.Builder{Store: s.Store}
	out := []subscription.Chain{}
	for _, chain := range chains {
		if !chain.Enabled || (!sub.IncludeAllChains && !filter[chain.ID]) {
			continue
		}
		composed := subscription.Chain{Name: chain.Name, Hops: make([]subscription.ProxyEndpoint, 0, len(chain.Hops))}
		for i := range chain.Hops {
			ep, err := builder.ResolveHopEndpoint(chain, i)
			if err != nil {
				return nil, err
			}
			composed.Hops = append(composed.Hops, ep)
		}
		out = append(out, composed)
	}
	return out, nil
}

// handleRotateSubscriptionToken issues a new subscription token, immediately
// invalidating the previous public link.
func (s *Server) handleRotateSubscriptionToken(w http.ResponseWriter, r *http.Request) {
	sub, err := s.Store.GetSubscription(pathID(r))
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, err := randomToken(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sub.Token = token
	if err := s.Store.UpdateSubscription(sub); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// handleDisableSubscription cuts off the public subscription link (404 for
// /sub/{token}) without deleting the subscription.
func (s *Server) handleDisableSubscription(w http.ResponseWriter, r *http.Request) {
	s.setSubscriptionDisabled(w, r, true)
}

func (s *Server) handleEnableSubscription(w http.ResponseWriter, r *http.Request) {
	s.setSubscriptionDisabled(w, r, false)
}

func (s *Server) setSubscriptionDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	sub, err := s.Store.GetSubscription(pathID(r))
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sub.Disabled != disabled {
		sub.Disabled = disabled
		if err := s.Store.UpdateSubscription(sub); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if sub.UserID != "" {
			s.scheduleAdmissionReapply()
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type subView struct {
	store.Subscription
	URL               string   `json:"url"`
	ExternalSourceIDs []string `json:"external_source_ids"`
}

func (s *Server) enrichSub(r *http.Request, sub *store.Subscription) subView {
	ids, err := s.Store.ListExternalSourceIDsForSubscription(sub.ID)
	if err != nil || ids == nil {
		ids = []string{}
	}
	return subView{
		Subscription:      *sub,
		URL:               s.subURL(r, sub.Token),
		ExternalSourceIDs: ids,
	}
}

func (s *Server) subURL(r *http.Request, token string) string {
	base := ""
	if st, err := s.Store.GetSettings(); err == nil {
		base = strings.TrimRight(st.PublicBaseURL, "/")
	}
	if base == "" && r != nil {
		scheme := s.requestScheme(r)
		host := r.Host
		if host == "" {
			host = "localhost"
		}
		base = scheme + "://" + host
	}
	if base == "" {
		return "/sub/" + token
	}
	return base + "/sub/" + token
}

func subFilename(sub *store.Subscription, format string) string {
	base := sanitizeFilename(sub.Name)
	if base == "" {
		base = "subscription"
	}
	switch format {
	case "clash":
		// Clash 把 Content-Disposition 文件名当作配置名展示，不要带 .yaml。
		return base
	case "singbox":
		return base + ".json"
	case "v2ray":
		return base + ".txt"
	default:
		return base + ".txt"
	}
}

// contentDisposition builds a Content-Disposition value with ASCII fallback + UTF-8 filename*.
func contentDisposition(filename string) string {
	ascii := asciiFilenameFallback(filename)
	// RFC 5987 filename*
	encoded := url.PathEscape(filename)
	// PathEscape uses %20 for space; RFC 5987 prefers %20 which is fine.
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, encoded)
}

func asciiFilenameFallback(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r > unicode.MaxASCII || r < 32 || r == '"' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.Trim(b.String(), ".-_ ")
	if out == "" {
		// Keep extension if present.
		if i := strings.LastIndex(name, "."); i >= 0 && i < len(name)-1 {
			ext := name[i:]
			safeExt := ""
			for _, r := range ext {
				if r > unicode.MaxASCII || r < 32 {
					continue
				}
				safeExt += string(r)
			}
			if safeExt != "" && safeExt != "." {
				return "subscription" + safeExt
			}
		}
		return "subscription.bin"
	}
	return out
}

// sanitizeFilename keeps a safe basename for Content-Disposition.
// Strips path separators and control chars; collapses whitespace to '-'.
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(name))
	prevDash := false
	for _, r := range name {
		switch {
		case r < 32 || r == 127:
			continue
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
			r == '"' || r == '<' || r == '>' || r == '|' || r == '\'' || r == ';':
			continue
		case r == ' ' || r == '\t':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		default:
			b.WriteRune(r)
			prevDash = false
		}
	}
	out := strings.Trim(b.String(), ".-_")
	// Avoid overly long filenames (clients / FS limits).
	runes := []rune(out)
	if len(runes) > 80 {
		out = string(runes[:80])
		out = strings.TrimRight(out, ".-_")
	}
	return out
}

func randomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
