package api

import (
	"net/http"
	"strings"

	"github.com/ladderairport/panel/internal/inboundfill"
	"github.com/ladderairport/panel/internal/store"
	"github.com/ladderairport/panel/internal/templates"
)

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, templates.List())
}

func (s *Server) handleListInbounds(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListInbounds()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type createInboundBody struct {
	Name     string         `json:"name"`
	Protocol string         `json:"protocol"`
	Params   map[string]any `json:"params"`
	Enabled  *bool          `json:"enabled"`
}

func (s *Server) handleCreateInbound(w http.ResponseWriter, r *http.Request) {
	var body createInboundBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err)
		return
	}
	if body.Name == "" || body.Protocol == "" {
		writeError(w, http.StatusBadRequest, "必须提供名称和协议")
		return
	}
	if _, ok := templates.Get(body.Protocol); !ok {
		writeError(w, http.StatusBadRequest, "未知协议")
		return
	}
	// Auto-generate passwords, UUIDs, TLS PEMs, Reality keys when omitted.
	filled, err := inboundfill.Fill(body.Protocol, body.Params)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if hasTemplateFRP(filled) {
		writeError(w, http.StatusBadRequest, "请在 Agent 管理页面的入站关联中配置 FRP")
		return
	}
	delete(filled, "frp_enabled")
	delete(filled, "frpc_config")
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	in := store.InboundConfig{
		Name:     body.Name,
		Protocol: body.Protocol,
		Params:   filled,
		Enabled:  enabled,
	}
	if err := s.Store.CreateInbound(&in); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleUpdateInbound(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	existing, err := s.Store.GetInbound(id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body struct {
		Name     *string        `json:"name"`
		Protocol *string        `json:"protocol"`
		Params   map[string]any `json:"params"`
		Enabled  *bool          `json:"enabled"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err)
		return
	}
	// Chains are composed client-side, so protocol/param edits simply flow
	// into the next subscription refresh. Disabling would leave an enabled
	// chain pointing at a hop that no longer listens.
	if body.Enabled != nil && !*body.Enabled && existing.Enabled {
		used, err := s.Store.InboundUsedByEnabledChain(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if used {
			writeError(w, http.StatusConflict, "该入站正被已启用的代理链作为跳点使用，请先停用代理链再禁用入站")
			return
		}
	}
	if body.Name != nil && *body.Name != "" {
		existing.Name = *body.Name
	}
	if body.Protocol != nil && *body.Protocol != "" {
		existing.Protocol = *body.Protocol
	}
	if _, ok := templates.Get(existing.Protocol); !ok {
		writeError(w, http.StatusBadRequest, "未知协议")
		return
	}
	if body.Params != nil {
		// Merge: keep existing secrets if client omitted them, then fill remaining gaps.
		merged := map[string]any{}
		for k, v := range existing.Params {
			merged[k] = v
		}
		for k, v := range body.Params {
			merged[k] = v
		}
		filled, err := inboundfill.Fill(existing.Protocol, merged)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		existing.Params = filled
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	if hasTemplateFRP(existing.Params) {
		writeError(w, http.StatusBadRequest, "请在 Agent 管理页面的入站关联中配置 FRP")
		return
	}
	delete(existing.Params, "frp_enabled")
	delete(existing.Params, "frpc_config")
	if err := s.Store.UpdateInbound(existing); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, err := s.Store.GetInbound(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func hasTemplateFRP(params map[string]any) bool {
	if params["frp_enabled"] == true {
		return true
	}
	config, _ := params["frpc_config"].(string)
	return strings.TrimSpace(config) != ""
}

func (s *Server) handleDeleteInbound(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if used, err := s.Store.InboundUsedByAnyChain(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if used {
		writeError(w, http.StatusConflict, "该入站仍被代理链引用")
		return
	}
	if err := s.Store.DeleteInbound(id); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
