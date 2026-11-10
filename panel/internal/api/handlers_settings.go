package api

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// normalizeCIDRList validates and normalizes a comma-separated CIDR list.
func normalizeCIDRList(raw string) (string, error) {
	parts := []string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return "", fmt.Errorf("trusted_proxy_cidrs 包含无效 CIDR：%s", part)
		}
		parts = append(parts, prefix.Masked().String())
	}
	return strings.Join(parts, ","), nil
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.GetSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type putSettingsBody struct {
	DefaultAgentToken *string `json:"default_agent_token"`
	GRPCTimeoutSec    *int    `json:"grpc_timeout_sec"`
	MaxConcurrency    *int    `json:"max_concurrency"`
	ListenAddr        *string `json:"listen_addr"`
	PublicBaseURL     *string `json:"public_base_url"`
	TrustedProxyCIDRs *string `json:"trusted_proxy_cidrs"`
	NewPassword       *string `json:"new_password"`
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.GetSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body putSettingsBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err)
		return
	}
	if body.DefaultAgentToken != nil {
		st.DefaultAgentToken = *body.DefaultAgentToken
	}
	if body.GRPCTimeoutSec != nil {
		if *body.GRPCTimeoutSec <= 0 {
			writeError(w, http.StatusBadRequest, "grpc_timeout_sec 必须为正数")
			return
		}
		st.GRPCTimeoutSec = *body.GRPCTimeoutSec
	}
	if body.MaxConcurrency != nil {
		if *body.MaxConcurrency <= 0 {
			writeError(w, http.StatusBadRequest, "max_concurrency 必须为正数")
			return
		}
		st.MaxConcurrency = *body.MaxConcurrency
	}
	if body.ListenAddr != nil {
		st.ListenAddr = *body.ListenAddr
	}
	if body.PublicBaseURL != nil {
		st.PublicBaseURL = strings.TrimSpace(*body.PublicBaseURL)
	}
	if body.TrustedProxyCIDRs != nil {
		normalized, err := normalizeCIDRList(*body.TrustedProxyCIDRs)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		st.TrustedProxyCIDRs = normalized
	}
	if body.NewPassword != nil {
		if *body.NewPassword == "" {
			writeError(w, http.StatusBadRequest, "new_password 不能为空")
			return
		}
		hash, err := HashPassword(*body.NewPassword)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		st.AdminPasswordHash = hash
	}
	if err := s.Store.SaveSettings(st); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.NewPassword != nil {
		// Revoke all existing sessions after a password change.
		if _, err := s.Store.BumpSessionVersion(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// Reflect timeout/concurrency on runner when present.
	if s.Runner != nil {
		if body.GRPCTimeoutSec != nil {
			s.Runner.Timeout.Store(int64(time.Duration(*body.GRPCTimeoutSec) * time.Second))
		}
		if body.MaxConcurrency != nil {
			s.Runner.MaxConcurrency.Store(int64(*body.MaxConcurrency))
		}
	}
	out, err := s.Store.GetSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
