package api

import (
	"net/http"

	"github.com/ladderairport/panel/internal/store"
)

// Proxy chains are composed client-side (Clash dialer-proxy / sing-box
// detour) from standalone bindings, so CRUD and enable/disable are pure data
// changes. Nodes only need a re-push when an enabled global route plan
// proxies through the chain (nodes compose it locally as detour outbounds).

func (s *Server) handleListProxyChains(w http.ResponseWriter, _ *http.Request) {
	chains, err := s.Store.ListProxyChains()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, chains)
}

func (s *Server) handleGetProxyChain(w http.ResponseWriter, r *http.Request) {
	chain, err := s.Store.GetProxyChain(pathID(r))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, chain)
}

func (s *Server) handleCreateProxyChain(w http.ResponseWriter, r *http.Request) {
	var chain store.ProxyChain
	if err := decodeJSON(w, r, &chain); err != nil {
		writeDecodeError(w, err)
		return
	}
	chain.ID = ""
	if err := s.Store.CreateProxyChain(&chain); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, _ := s.Store.GetProxyChain(chain.ID)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateProxyChain(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	existing, err := s.Store.GetProxyChain(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var candidate store.ProxyChain
	if err := decodeJSON(w, r, &candidate); err != nil {
		writeDecodeError(w, err)
		return
	}
	candidate.ID = id
	// Enabled is toggled only through the enable/disable endpoints.
	candidate.Enabled = existing.Enabled
	if err := s.Store.ValidateProxyChain(&candidate); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.UpdateProxyChain(&candidate); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if existing.Enabled {
		s.reapplyForChainChange(r, id)
	}
	updated, _ := s.Store.GetProxyChain(id)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteProxyChain(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteProxyChain(pathID(r)); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.scheduleAdmissionReapplyIfUsers()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleEnableProxyChain(w http.ResponseWriter, r *http.Request) {
	s.setProxyChainEnabled(w, r, true)
}

func (s *Server) handleDisableProxyChain(w http.ResponseWriter, r *http.Request) {
	s.setProxyChainEnabled(w, r, false)
}

func (s *Server) setProxyChainEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id := pathID(r)
	chain, err := s.Store.GetProxyChain(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if enabled {
		// Bindings may have changed since the chain was saved.
		if err := s.Store.ValidateProxyChain(chain); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if chain.Enabled != enabled {
		if err := s.Store.SetProxyChainEnabled(id, enabled); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.reapplyForChainChange(r, id)
	}
	updated, _ := s.Store.GetProxyChain(id)
	writeJSON(w, http.StatusOK, updated)
}

// reapplyForChainChange re-pushes every node when an enabled global route
// plan proxies through the chain, since any node may compose it locally.
// Best-effort, like reapplyForRoutePlanChange.
func (s *Server) reapplyForChainChange(r *http.Request, chainID string) {
	// Chain hops also decide which proxy users each hop inbound admits.
	s.scheduleAdmissionReapplyIfUsers()
	plans, err := s.Store.ListEnabledGlobalRoutePlans()
	if err != nil {
		return
	}
	for i := range plans {
		for _, rule := range plans[i].Rules {
			if rule.Enabled && rule.Action == "proxy" && rule.TargetChainID == chainID {
				s.reapplyForRoutePlanChange(r, nil, &plans[i])
				return
			}
		}
	}
}
