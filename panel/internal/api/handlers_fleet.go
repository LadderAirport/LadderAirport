package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ladderairport/panel/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func classifyRPCError(err error) string {
	if err == nil {
		return "online"
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unauthenticated, codes.PermissionDenied:
			return "unauthorized"
		}
	}
	// string fallback for wrapped errors
	msg := err.Error()
	if strings.Contains(msg, "Unauthenticated") || strings.Contains(msg, "invalid token") || strings.Contains(msg, "令牌无效") {
		return "unauthorized"
	}
	return "unreachable"
}

// FleetOverview is a PPanel-style multi-node summary.
type FleetOverview struct {
	TotalNodes   int          `json:"total_nodes"`
	OnlineNodes  int          `json:"online_nodes"`
	OfflineNodes int          `json:"offline_nodes"`
	RunningNodes int          `json:"running_nodes"`
	Nodes        []store.Node `json:"nodes"`
	RefreshedAt  int64        `json:"refreshed_at"`
}

func (s *Server) handleFleetOverview(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.Store.ListNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts, err := s.Store.CountInboundsByNode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ov := buildOverview(nodes, counts)
	s.annotateUplinkWS(ov.Nodes)
	writeJSON(w, http.StatusOK, ov)
}

// handleFleetRefresh probes all nodes in parallel, pulls status + metrics, persists cache.
func (s *Server) handleFleetRefresh(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.Store.ListNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(nodes) == 0 {
		writeJSON(w, http.StatusOK, buildOverview(nil, nil))
		return
	}

	maxConc := 10
	if s.Runner != nil {
		maxConc = s.Runner.ConcurrencyLimit()
	}
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	updated := make([]store.Node, len(nodes))

	for i := range nodes {
		i := i
		n := nodes[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			refreshed := s.refreshOneNode(r.Context(), n)
			mu.Lock()
			updated[i] = refreshed
			mu.Unlock()
		}()
	}
	wg.Wait()

	counts, _ := s.Store.CountInboundsByNode()
	ov := buildOverview(updated, counts)
	s.annotateUplinkWS(ov.Nodes)
	writeJSON(w, http.StatusOK, ov)
}

// annotateUplinkWS fills UplinkWSConnected for uplink nodes in-place so the UI
// can distinguish a live WS control channel (full gRPC parity) from an uplink
// node that is only reachable via the HTTP report/config-sync fallback.
func (s *Server) annotateUplinkWS(nodes []store.Node) {
	for i := range nodes {
		nodes[i].UplinkWSConnected = s.uplinkWSConnected(nodes[i])
	}
}

func (s *Server) refreshOneNode(parent context.Context, n store.Node) store.Node {
	if n.ControlMode == store.ControlModeUplink {
		if n.UplinkLastSeenUnix > 0 && time.Since(time.Unix(n.UplinkLastSeenUnix, 0)) > uplinkStaleAfter() {
			cutoff := time.Now().Add(-uplinkStaleAfter()).Unix()
			_ = s.Store.MarkUplinkUnreachable(n.ID, "节点超过 45 秒未上报", cutoff)
			if latest, err := s.Store.GetNode(n.ID); err == nil {
				return *latest
			}
			n.Status = "unreachable"
			n.LastError = "节点超过 45 秒未上报"
		}
		return n
	}
	ctx, cancel := context.WithTimeout(parent, s.opTimeout())
	defer cancel()

	client, err := s.liveDial(ctx, n, s.nodeToken(&n))
	if err != nil {
		n.Status = "unreachable"
		n.LastError = fmt.Sprintf("连接节点失败：%v", err)
		n.RuntimeState = ""
		_ = s.Store.UpdateNode(&n)
		return n
	}
	defer func() { _ = client.Close() }()

	ping, err := client.Ping(ctx)
	if err != nil {
		n.Status = classifyRPCError(err)
		n.LastError = err.Error()
		_ = s.Store.UpdateNode(&n)
		return n
	}
	n.Status = "online"
	n.LastSeenUnix = time.Now().Unix()
	applyPingIdentity(&n, ping)
	n.Capabilities = append([]string{}, ping.GetCapabilities()...)
	n.LastError = ""

	if st, err := client.GetStatus(ctx); err == nil {
		n.RuntimeState = st.GetState()
		if st.GetConfigHash() != "" {
			n.ConfigHash = st.GetConfigHash()
		}
		if st.GetLastError() != "" {
			n.LastError = st.GetLastError()
		}
	}
	if m, err := client.GetMetrics(ctx); err == nil {
		n.Connections = m.GetConnections()
		n.UplinkBytes = m.GetUplinkBytes()
		n.DownlinkBytes = m.GetDownlinkBytes()
		n.CPUPercent = m.GetCpuPercent()
		n.MemoryRSSBytes = m.GetMemoryRssBytes()
		n.MetricsAtUnix = time.Now().Unix()
	}
	_ = s.Store.UpdateNode(&n)
	return n
}

func buildOverview(nodes []store.Node, counts map[string]int) FleetOverview {
	ov := FleetOverview{
		Nodes:       []store.Node{},
		RefreshedAt: time.Now().Unix(),
	}
	if nodes == nil {
		return ov
	}
	for _, n := range nodes {
		if counts != nil {
			n.InboundCount = counts[n.ID]
		}
		ov.TotalNodes++
		// Treat legacy "running" (mistakenly written to status) as online.
		if n.Status == "online" || n.Status == "running" {
			ov.OnlineNodes++
			if n.Status == "running" && n.RuntimeState == "" {
				n.RuntimeState = "running"
			}
		}
		if n.RuntimeState == "running" || n.Status == "running" {
			ov.RunningNodes++
		}
		ov.Nodes = append(ov.Nodes, n)
	}
	ov.OfflineNodes = ov.TotalNodes - ov.OnlineNodes
	return ov
}
