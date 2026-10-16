package uplinkws

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"time"

	"github.com/coder/websocket"
	"github.com/ladderairport/pkg/uplinkws"
	agentv1 "github.com/ladderairport/proto/gen/go/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// marshalOpts/unmarshalOpts keep the JSON payload shape identical to the Panel
// side (which mirrors gRPC's protojson) so both ends exchange the same concrete
// agent.v1 messages regardless of transport.
var marshalOpts = protojson.MarshalOptions{UseProtoNames: false}
var unmarshalOpts = protojson.UnmarshalOptions{DiscardUnknown: true}

// jsonUnmarshal is a thin wrapper so client.go can decode frames without
// importing encoding/json directly.
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// reportPayload mirrors the HTTP /agent/report body so the Panel converges node
// state identically whether the report arrives over WS or the HTTP fallback.
type reportPayload struct {
	NodeID          string   `json:"node_id"`
	CollectedAtUnix int64    `json:"collected_at_unix"`
	RuntimeState    string   `json:"runtime_state"`
	ConfigHash      string   `json:"config_hash"`
	LastError       string   `json:"last_error"`
	AgentVersion    string   `json:"agent_version"`
	SingboxVersion  string   `json:"singbox_version"`
	CoreVersion     string   `json:"core_version,omitempty"`
	CoreCommit      string   `json:"core_commit,omitempty"`
	ClientType      string   `json:"client_type,omitempty"`
	ClientVersion   string   `json:"client_version,omitempty"`
	LuciVersion     string   `json:"luci_version,omitempty"`
	Capabilities    []string `json:"capabilities"`
	Connections     int64    `json:"connections"`
	UplinkBytes     int64    `json:"uplink_bytes"`
	DownlinkBytes   int64    `json:"downlink_bytes"`
	CPUPercent      float64  `json:"cpu_percent"`
	MemoryRSSBytes  int64    `json:"memory_rss_bytes"`
}

// writeFrame marshals and writes a single frame under the write mutex, bounded
// by writeTimeout independently of any request context.
func (s *session) writeFrame(frame *uplinkws.Frame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.ws.Write(ctx, websocket.MessageText, data)
}

func (s *session) writeResponse(id uint64, payload json.RawMessage) {
	_ = s.writeFrame(&uplinkws.Frame{Type: uplinkws.FrameResponse, ID: id, Payload: payload})
}

func (s *session) writeError(id uint64, err error) {
	_ = s.writeFrame(&uplinkws.Frame{Type: uplinkws.FrameResponse, ID: id, Error: toWireError(err)})
}

// toWireError converts a Go error (possibly a gRPC status) into the wire error
// so the Panel preserves the exact status code (e.g. codes.Unimplemented).
func toWireError(err error) *uplinkws.Error {
	st, _ := status.FromError(err)
	return &uplinkws.Error{Code: uint32(st.Code()), Message: st.Message()}
}

// reportLoop pushes an unsolicited status report immediately and then on every
// ReportEvery tick until the socket ends.
func (s *session) reportLoop(ctx context.Context) {
	s.pushReport(ctx)
	ticker := time.NewTicker(s.client.cfg.ReportEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pushReport(ctx)
		}
	}
}

func (s *session) pushReport(ctx context.Context) {
	srv := s.client.cfg.Server
	st, err := srv.GetStatus(ctx, &agentv1.GetStatusRequest{})
	if err != nil {
		return
	}
	metrics, _ := srv.GetMetrics(ctx, &agentv1.GetMetricsRequest{})
	ping, _ := srv.Ping(ctx, &agentv1.PingRequest{})
	body := reportPayload{
		NodeID:          s.client.cfg.NodeID,
		CollectedAtUnix: time.Now().Unix(),
		RuntimeState:    st.GetState(),
		ConfigHash:      st.GetConfigHash(),
		LastError:       st.GetLastError(),
	}
	if ping != nil {
		body.AgentVersion = ping.GetAgentVersion()
		body.SingboxVersion = ping.GetSingboxVersion()
		body.CoreVersion = ping.GetCoreVersion()
		body.CoreCommit = ping.GetCoreCommit()
		body.ClientType = ping.GetClientType()
		body.ClientVersion = ping.GetClientVersion()
		body.LuciVersion = ping.GetLuciVersion()
		// Advertise WS uplink support alongside the node's native capabilities.
		body.Capabilities = append(ping.GetCapabilities(), Capability)
	} else {
		body.Capabilities = []string{Capability}
	}
	if metrics != nil {
		body.Connections = metrics.GetConnections()
		body.UplinkBytes = metrics.GetUplinkBytes()
		body.DownlinkBytes = metrics.GetDownlinkBytes()
		body.CPUPercent = metrics.GetCpuPercent()
		body.MemoryRSSBytes = metrics.GetMemoryRssBytes()
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return
	}
	_ = s.writeFrame(&uplinkws.Frame{Type: uplinkws.FrameReport, Payload: payload})
}

// handleRequest dispatches one Panel→Agent request against the local control
// surface and replies with a terminal response (or opens a stream for logs).
func (s *session) handleRequest(ctx context.Context, frame *uplinkws.Frame) {
	if frame.Method == uplinkws.MethodStreamLogs {
		s.handleStreamLogs(ctx, frame)
		return
	}
	payload, err := s.dispatchUnary(ctx, frame.Method, frame.Payload)
	if err != nil {
		s.writeError(frame.ID, err)
		return
	}
	s.writeResponse(frame.ID, payload)
}

// invoke decodes payload into req, runs call, and marshals the response into a
// JSON payload. It centralises the protojson round trip shared by every RPC.
func (s *session) invoke(payload json.RawMessage, req proto.Message, call func() (proto.Message, error)) (json.RawMessage, error) {
	if len(payload) > 0 {
		if err := unmarshalOpts.Unmarshal(payload, req); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}
	resp, err := call()
	if err != nil {
		return nil, err
	}
	out, err := marshalOpts.Marshal(resp)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return out, nil
}

// dispatchUnary maps a method name to the matching control-surface call. It
// covers the full AgentControl unary surface so an uplink node answers every
// operation live, at parity with the push/gRPC control plane.
func (s *session) dispatchUnary(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
	srv := s.client.cfg.Server
	switch method {
	case uplinkws.MethodPing:
		req := &agentv1.PingRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.Ping(ctx, req) })
	case uplinkws.MethodApplyConfig:
		req := &agentv1.ApplyConfigRequest{}
		res, err := s.invoke(payload, req, func() (proto.Message, error) { return srv.ApplyConfig(ctx, req) })
		if err == nil {
			log.Printf("uplink WS 收到并成功应用配置：hash=%s", req.GetConfigHash())
		}
		return res, err
	case uplinkws.MethodStart:
		req := &agentv1.StartRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.Start(ctx, req) })
	case uplinkws.MethodStop:
		req := &agentv1.StopRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.Stop(ctx, req) })
	case uplinkws.MethodGetStatus:
		req := &agentv1.GetStatusRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.GetStatus(ctx, req) })
	case uplinkws.MethodGetMetrics:
		req := &agentv1.GetMetricsRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.GetMetrics(ctx, req) })
	case uplinkws.MethodListInterfaces:
		req := &agentv1.ListInterfacesRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.ListInterfaces(ctx, req) })
	case uplinkws.MethodUpgradeAgent:
		req := &agentv1.UpgradeAgentRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.UpgradeAgent(ctx, req) })
	case uplinkws.MethodProbeOutbound:
		req := &agentv1.ProbeOutboundRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.ProbeOutbound(ctx, req) })
	case uplinkws.MethodGetPublicAddresses:
		req := &agentv1.GetPublicAddressesRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.GetPublicAddresses(ctx, req) })
	case uplinkws.MethodPrepareProtocolCertificate:
		req := &agentv1.PrepareProtocolCertificateRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.PrepareProtocolCertificate(ctx, req) })
	case uplinkws.MethodInstallProtocolCertificate:
		req := &agentv1.InstallProtocolCertificateRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.InstallProtocolCertificate(ctx, req) })
	case uplinkws.MethodGetProtocolCertificateStatus:
		req := &agentv1.GetProtocolCertificateStatusRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.GetProtocolCertificateStatus(ctx, req) })
	case uplinkws.MethodDeleteProtocolCertificateGeneration:
		req := &agentv1.DeleteProtocolCertificateGenerationRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.DeleteProtocolCertificateGeneration(ctx, req) })
	case uplinkws.MethodApplyFRPServerConfig:
		req := &agentv1.ApplyFRPServerConfigRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.ApplyFRPServerConfig(ctx, req) })
	case uplinkws.MethodStartFRPServer:
		req := &agentv1.StartFRPServerRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.StartFRPServer(ctx, req) })
	case uplinkws.MethodStopFRPServer:
		req := &agentv1.StopFRPServerRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.StopFRPServer(ctx, req) })
	case uplinkws.MethodGetFRPServerStatus:
		req := &agentv1.GetFRPServerStatusRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.GetFRPServerStatus(ctx, req) })
	case uplinkws.MethodGetFRPServerMappings:
		req := &agentv1.GetFRPServerMappingsRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.GetFRPServerMappings(ctx, req) })
	case uplinkws.MethodGetNodeMetrics:
		req := &agentv1.GetNodeMetricsRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.GetNodeMetrics(ctx, req) })
	case uplinkws.MethodGetBBRStatus:
		req := &agentv1.GetBBRStatusRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.GetBBRStatus(ctx, req) })
	case uplinkws.MethodSetBBR:
		req := &agentv1.SetBBRRequest{}
		return s.invoke(payload, req, func() (proto.Message, error) { return srv.SetBBR(ctx, req) })
	default:
		return nil, status.Errorf(codes.Unimplemented, "未知方法 %q", method)
	}
}

// handleStreamLogs runs a server-streaming log subscription, forwarding each
// LogLine as a FrameStream item and terminating with EOF or an error frame. It
// registers a cancel func so a Panel FrameCancel aborts the subscription.
func (s *session) handleStreamLogs(ctx context.Context, frame *uplinkws.Frame) {
	req := &agentv1.StreamLogsRequest{}
	if len(frame.Payload) > 0 {
		if err := unmarshalOpts.Unmarshal(frame.Payload, req); err != nil {
			s.writeStreamError(frame.ID, status.Error(codes.InvalidArgument, err.Error()))
			return
		}
	}

	streamCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.streams[frame.ID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.streams, frame.ID)
		s.mu.Unlock()
		cancel()
	}()

	stream := &logStreamServer{ctx: streamCtx, sess: s, id: frame.ID}
	err := s.client.cfg.Server.StreamLogs(req, stream)
	if streamCtx.Err() != nil {
		// Panel cancelled (or the socket ended); it has already torn down its
		// reader, so no terminal frame is needed.
		return
	}
	if err != nil {
		s.writeStreamError(frame.ID, err)
		return
	}
	s.writeStreamEOF(frame.ID)
}

func (s *session) writeStreamEOF(id uint64) {
	_ = s.writeFrame(&uplinkws.Frame{Type: uplinkws.FrameStream, ID: id, EOF: true})
}

func (s *session) writeStreamError(id uint64, err error) {
	_ = s.writeFrame(&uplinkws.Frame{Type: uplinkws.FrameStream, ID: id, Error: toWireError(err)})
}

// cancelStream aborts an in-flight stream in response to a Panel FrameCancel.
func (s *session) cancelStream(id uint64) {
	s.mu.Lock()
	cancel := s.streams[id]
	delete(s.streams, id)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// logStreamServer adapts the WS socket to grpc.ServerStreamingServer[LogLine]
// so control.Server.StreamLogs can be invoked unchanged; each Send becomes a
// FrameStream item on the uplink socket.
type logStreamServer struct {
	ctx  context.Context
	sess *session
	id   uint64
}

func (l *logStreamServer) Send(line *agentv1.LogLine) error {
	payload, err := marshalOpts.Marshal(line)
	if err != nil {
		return err
	}
	return l.sess.writeFrame(&uplinkws.Frame{Type: uplinkws.FrameStream, ID: l.id, Payload: payload})
}

func (l *logStreamServer) Context() context.Context     { return l.ctx }
func (l *logStreamServer) SetHeader(metadata.MD) error  { return nil }
func (l *logStreamServer) SendHeader(metadata.MD) error { return nil }
func (l *logStreamServer) SetTrailer(metadata.MD)       {}
func (l *logStreamServer) RecvMsg(any) error            { return io.EOF }

func (l *logStreamServer) SendMsg(m any) error {
	if line, ok := m.(*agentv1.LogLine); ok {
		return l.Send(line)
	}
	return nil
}

// ensure the adapter satisfies the generated server-stream interface.
var _ agentv1.AgentControl_StreamLogsServer = (*logStreamServer)(nil)
