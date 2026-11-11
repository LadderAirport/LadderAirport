package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/ladderairport/panel/internal/proxyuser"
	"github.com/ladderairport/panel/internal/store"
	"github.com/ladderairport/panel/internal/subscription"
)

// admissionReapplyDelay batches bursts of user / admission edits into one
// node re-push: agents restart sing-box on every config change, dropping all
// connections on that node. Variable for tests.
var admissionReapplyDelay = 5 * time.Second

type proxyUserView struct {
	store.ProxyUser
	SubscriptionCount int `json:"subscription_count"`
}

func (s *Server) proxyUserViews(users []store.ProxyUser) ([]proxyUserView, error) {
	subs, err := s.Store.ListSubscriptions()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, sub := range subs {
		if sub.UserID != "" {
			counts[sub.UserID]++
		}
	}
	out := make([]proxyUserView, 0, len(users))
	for _, u := range users {
		out = append(out, proxyUserView{ProxyUser: u, SubscriptionCount: counts[u.ID]})
	}
	return out, nil
}

func (s *Server) handleListProxyUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListProxyUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views, err := s.proxyUserViews(users)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, views)
}

type proxyUserBody struct {
	Name    *string `json:"name"`
	Note    *string `json:"note"`
	Enabled *bool   `json:"enabled"`
}

func (s *Server) handleCreateProxyUser(w http.ResponseWriter, r *http.Request) {
	var body proxyUserBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err)
		return
	}
	if body.Name == nil || strings.TrimSpace(*body.Name) == "" {
		writeError(w, http.StatusBadRequest, "必须提供用户名")
		return
	}
	secret, err := proxyuser.NewSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	u := &store.ProxyUser{Name: *body.Name, Enabled: true, Secret: secret}
	if body.Note != nil {
		u.Note = strings.TrimSpace(*body.Note)
	}
	if body.Enabled != nil {
		u.Enabled = *body.Enabled
	}
	if err := s.Store.CreateProxyUser(u); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A new user has no subscriptions yet, so nodes need no re-push.
	writeJSON(w, http.StatusCreated, proxyUserView{ProxyUser: *u})
}

func (s *Server) handleUpdateProxyUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.loadProxyUser(w, r)
	if !ok {
		return
	}
	var body proxyUserBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err)
		return
	}
	wasEnabled := u.Enabled
	if body.Name != nil {
		if strings.TrimSpace(*body.Name) == "" {
			writeError(w, http.StatusBadRequest, "用户名不能为空")
			return
		}
		u.Name = *body.Name
	}
	if body.Note != nil {
		u.Note = strings.TrimSpace(*body.Note)
	}
	if body.Enabled != nil {
		u.Enabled = *body.Enabled
	}
	if err := s.Store.UpdateProxyUser(u); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if wasEnabled != u.Enabled {
		s.scheduleAdmissionReapply()
	}
	s.writeProxyUser(w, u)
}

func (s *Server) handleDeleteProxyUser(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteProxyUser(pathID(r)); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.scheduleAdmissionReapply()
	w.WriteHeader(http.StatusNoContent)
}

// handleResetProxyUserCredentials rotates the user's secret: every derived
// UUID / password changes, so clients must refresh the subscription.
func (s *Server) handleResetProxyUserCredentials(w http.ResponseWriter, r *http.Request) {
	u, ok := s.loadProxyUser(w, r)
	if !ok {
		return
	}
	secret, err := proxyuser.NewSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	u.Secret = secret
	if err := s.Store.UpdateProxyUser(u); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.scheduleAdmissionReapply()
	s.writeProxyUser(w, u)
}

func (s *Server) loadProxyUser(w http.ResponseWriter, r *http.Request) (*store.ProxyUser, bool) {
	u, err := s.Store.GetProxyUser(pathID(r))
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return u, true
}

func (s *Server) writeProxyUser(w http.ResponseWriter, u *store.ProxyUser) {
	views, err := s.proxyUserViews([]store.ProxyUser{*u})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, views[0])
}

// validateSubscriptionUser normalizes a subscription's user binding.
func (s *Server) validateSubscriptionUser(userID string) (string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", nil
	}
	if _, err := s.Store.GetProxyUser(userID); err != nil {
		if isNotFound(err) {
			return "", fmt.Errorf("用户不存在：%s", userID)
		}
		return "", err
	}
	return userID, nil
}

// --- Node admission ---

type sharedInboundView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Reason   string `json:"reason"`
}

type nodeAdmissionView struct {
	NodeID        string `json:"node_id"`
	AdmissionOnly bool   `json:"admission_only"`
	// SharedInbounds are attached inbounds that cannot distinguish users and
	// therefore keep their shared credential even when admission-only is on.
	SharedInbounds []sharedInboundView `json:"shared_inbounds"`
}

func (s *Server) nodeAdmissionViews() ([]nodeAdmissionView, error) {
	nodes, err := s.Store.ListNodes()
	if err != nil {
		return nil, err
	}
	attachments, err := s.Store.ListAllNodeInboundAttachments()
	if err != nil {
		return nil, err
	}
	admissions, err := s.Store.ListNodeAdmissions()
	if err != nil {
		return nil, err
	}
	out := make([]nodeAdmissionView, 0, len(nodes))
	for _, n := range nodes {
		view := nodeAdmissionView{NodeID: n.ID, AdmissionOnly: admissions[n.ID].AdmissionOnly, SharedInbounds: []sharedInboundView{}}
		for _, att := range attachments[n.ID] {
			in := att.EffectiveInbound()
			if ok, reason := proxyuser.Supports(in); !ok {
				view.SharedInbounds = append(view.SharedInbounds, sharedInboundView{
					ID: in.ID, Name: in.Name, Protocol: in.Protocol, Reason: reason,
				})
			}
		}
		out = append(out, view)
	}
	return out, nil
}

func (s *Server) handleListNodeAdmissions(w http.ResponseWriter, r *http.Request) {
	views, err := s.nodeAdmissionViews()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handlePutNodeAdmission(w http.ResponseWriter, r *http.Request) {
	nodeID := pathID(r)
	if _, err := s.Store.GetNode(nodeID); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body struct {
		AdmissionOnly *bool `json:"admission_only"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err)
		return
	}
	if body.AdmissionOnly == nil {
		writeError(w, http.StatusBadRequest, "必须提供 admission_only")
		return
	}
	secret, err := proxyuser.NewSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	before, err := s.Store.ListNodeAdmissions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	adm, err := s.Store.SetNodeAdmission(nodeID, *body.AdmissionOnly, secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if before[nodeID].AdmissionOnly != adm.AdmissionOnly {
		s.scheduleAdmissionReapply()
	}
	views, err := s.nodeAdmissionViews()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, v := range views {
		if v.NodeID == nodeID {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	writeError(w, http.StatusNotFound, "节点不存在")
}

// applySubscriptionCredentials substitutes the bound user's credentials into
// local endpoints and chain hops, and omits what an admission-only node
// would reject (a chain is omitted as a whole when any hop is).
func (s *Server) applySubscriptionCredentials(
	sub *store.Subscription, local []subscription.ProxyEndpoint, chains []subscription.Chain,
) ([]subscription.ProxyEndpoint, []subscription.Chain, error) {
	admissions, err := s.Store.ListNodeAdmissions()
	if err != nil {
		return nil, nil, err
	}
	var user *proxyuser.Identity
	if sub.UserID != "" {
		u, err := s.Store.GetProxyUser(sub.UserID)
		if err != nil {
			return nil, nil, err
		}
		id, err := proxyuser.ForUser(*u)
		if err != nil {
			return nil, nil, err
		}
		user = &id
	}
	if user == nil && len(admissions) == 0 {
		return local, chains, nil
	}
	outLocal := make([]subscription.ProxyEndpoint, 0, len(local))
	for _, ep := range local {
		rewritten, keep, err := proxyuser.ForSubscription(ep, user, admissions)
		if err != nil {
			return nil, nil, fmt.Errorf("节点 %q：%w", ep.Name, err)
		}
		if keep {
			outLocal = append(outLocal, rewritten)
		}
	}
	outChains := make([]subscription.Chain, 0, len(chains))
	for _, chain := range chains {
		hops := make([]subscription.ProxyEndpoint, 0, len(chain.Hops))
		keepChain := true
		for _, hop := range chain.Hops {
			rewritten, keep, err := proxyuser.ForSubscription(hop, user, admissions)
			if err != nil {
				return nil, nil, fmt.Errorf("代理链 %q：%w", chain.Name, err)
			}
			if !keep {
				keepChain = false
				break
			}
			hops = append(hops, rewritten)
		}
		if keepChain {
			chain.Hops = hops
			outChains = append(outChains, chain)
		}
	}
	return outLocal, outChains, nil
}

// scheduleAdmissionReapply re-pushes every node's config shortly after the
// last admission-affecting change. Agents skip restarts when the config hash
// is unchanged, so only nodes whose user lists changed reconnect.
func (s *Server) scheduleAdmissionReapply() {
	if s.Runner == nil {
		return
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if s.admissionTimer != nil {
		s.admissionTimer.Stop()
	}
	s.admissionTimer = time.AfterFunc(admissionReapplyDelay, s.reapplyAllNodes)
}

func (s *Server) reapplyAllNodes() {
	nodes, err := s.Store.ListNodes()
	if err != nil || len(nodes) == 0 {
		return
	}
	nodeIDs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		nodeIDs = append(nodeIDs, node.ID)
	}
	task := &store.Task{Type: "apply", Status: "pending", NodeIDs: nodeIDs}
	if err := s.Store.CreateTask(task); err != nil {
		log.Printf("用户准入变更：创建下发任务失败：%v", err)
		return
	}
	if err := s.Runner.RunTask(context.Background(), task.ID); err != nil {
		log.Printf("用户准入变更：下发任务 %s 失败：%v", task.ID, err)
	}
}

// scheduleAdmissionReapplyIfUsers re-pushes after chain edits only when some
// subscription is user-bound (chain hops feed the admission plan).
func (s *Server) scheduleAdmissionReapplyIfUsers() {
	if has, err := s.Store.HasUserBoundSubscriptions(); err == nil && has {
		s.scheduleAdmissionReapply()
	}
}
