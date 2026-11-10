package uplinkhub

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ladderairport/pkg/uplinkws"
	agentv1 "github.com/ladderairport/proto/gen/go/agent/v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// marshalOpts keeps the JSON payload shape identical to gRPC's protojson so the
// Agent unmarshals the same concrete request/response messages regardless of
// transport. EmitUnpopulated matches server-streaming defaults closely enough
// while UseProtoNames keeps snake_case field names stable.
var marshalOpts = protojson.MarshalOptions{UseProtoNames: false}
var unmarshalOpts = protojson.UnmarshalOptions{DiscardUnknown: true}

// Client is a WS-backed AgentControl client. It satisfies the same method set
// as *nodeclient.Client (NodeLive, NodeFRPS, NodeFRPSMappings, NodeSysMetrics,
// NodeBBR, certmanager.Agent, dnsreconcile.Agent,
// batch.NodeRPC), so Panel live handlers route to a connected uplink node
// exactly as they would to a push node's gRPC control plane.
type Client struct {
	conn *Conn
}

func newClient(conn *Conn) *Client {
	return &Client{conn: conn}
}

// Close is a no-op: the socket lifetime is owned by the Hub/Serve loop, not by
// individual RPC callers. It exists so *Client satisfies the Close() interface
// contract shared with the gRPC client.
func (c *Client) Close() error { return nil }

// unary marshals req, issues the RPC over the socket, and unmarshals the reply
// into resp. Both req and resp must be concrete agent.v1 messages.
func (c *Client) unary(ctx context.Context, method string, req, resp proto.Message) error {
	payload, err := marshalOpts.Marshal(req)
	if err != nil {
		return fmt.Errorf("编码 %s 请求失败：%w", method, err)
	}
	raw, err := c.conn.call(ctx, method, json.RawMessage(payload))
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	if err := unmarshalOpts.Unmarshal(raw, resp); err != nil {
		return fmt.Errorf("解析 %s 响应失败：%w", method, err)
	}
	return nil
}

func (c *Client) Ping(ctx context.Context) (*agentv1.PingResponse, error) {
	resp := &agentv1.PingResponse{}
	if err := c.unary(ctx, uplinkws.MethodPing, &agentv1.PingRequest{}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) ApplyConfig(ctx context.Context, configJSON, hash string, replace bool) (*agentv1.ApplyConfigResponse, error) {
	resp := &agentv1.ApplyConfigResponse{}
	req := &agentv1.ApplyConfigRequest{ConfigJson: configJSON, ConfigHash: hash, Replace: replace}
	if err := c.unary(ctx, uplinkws.MethodApplyConfig, req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) Start(ctx context.Context) (*agentv1.StartResponse, error) {
	resp := &agentv1.StartResponse{}
	if err := c.unary(ctx, uplinkws.MethodStart, &agentv1.StartRequest{}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) Stop(ctx context.Context) (*agentv1.StopResponse, error) {
	resp := &agentv1.StopResponse{}
	if err := c.unary(ctx, uplinkws.MethodStop, &agentv1.StopRequest{}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) GetStatus(ctx context.Context) (*agentv1.GetStatusResponse, error) {
	resp := &agentv1.GetStatusResponse{}
	if err := c.unary(ctx, uplinkws.MethodGetStatus, &agentv1.GetStatusRequest{}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) GetMetrics(ctx context.Context) (*agentv1.GetMetricsResponse, error) {
	resp := &agentv1.GetMetricsResponse{}
	if err := c.unary(ctx, uplinkws.MethodGetMetrics, &agentv1.GetMetricsRequest{}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) ApplyFRPServerConfig(ctx context.Context, config *agentv1.FRPServerConfig, hash string) (*agentv1.ApplyFRPServerConfigResponse, error) {
	resp := &agentv1.ApplyFRPServerConfigResponse{}
	req := &agentv1.ApplyFRPServerConfigRequest{Config: config, ConfigHash: hash}
	if err := c.unary(ctx, uplinkws.MethodApplyFRPServerConfig, req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) StartFRPServer(ctx context.Context) (*agentv1.StartFRPServerResponse, error) {
	resp := &agentv1.StartFRPServerResponse{}
	if err := c.unary(ctx, uplinkws.MethodStartFRPServer, &agentv1.StartFRPServerRequest{}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) StopFRPServer(ctx context.Context) (*agentv1.StopFRPServerResponse, error) {
	resp := &agentv1.StopFRPServerResponse{}
	if err := c.unary(ctx, uplinkws.MethodStopFRPServer, &agentv1.StopFRPServerRequest{}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) GetFRPServerStatus(ctx context.Context) (*agentv1.GetFRPServerStatusResponse, error) {
	resp := &agentv1.GetFRPServerStatusResponse{}
	if err := c.unary(ctx, uplinkws.MethodGetFRPServerStatus, &agentv1.GetFRPServerStatusRequest{}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) GetFRPServerMappings(ctx context.Context) (*agentv1.GetFRPServerMappingsResponse, error) {
	resp := &agentv1.GetFRPServerMappingsResponse{}
	if err := c.unary(ctx, uplinkws.MethodGetFRPServerMappings, &agentv1.GetFRPServerMappingsRequest{}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) ProbeOutbound(ctx context.Context, outboundTag, targetURL string) (*agentv1.ProbeOutboundResponse, error) {
	resp := &agentv1.ProbeOutboundResponse{}
	req := &agentv1.ProbeOutboundRequest{OutboundTag: outboundTag, Url: targetURL}
	if err := c.unary(ctx, uplinkws.MethodProbeOutbound, req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) ListInterfaces(ctx context.Context) (*agentv1.ListInterfacesResponse, error) {
	resp := &agentv1.ListInterfacesResponse{}
	if err := c.unary(ctx, uplinkws.MethodListInterfaces, &agentv1.ListInterfacesRequest{}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) GetPublicAddresses(ctx context.Context, ipv4, ipv6 bool) (*agentv1.GetPublicAddressesResponse, error) {
	resp := &agentv1.GetPublicAddressesResponse{}
	req := &agentv1.GetPublicAddressesRequest{Ipv4: ipv4, Ipv6: ipv6}
	if err := c.unary(ctx, uplinkws.MethodGetPublicAddresses, req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) PrepareProtocolCertificate(ctx context.Context, certificateID, generationID string, dnsNames []string) (*agentv1.PrepareProtocolCertificateResponse, error) {
	resp := &agentv1.PrepareProtocolCertificateResponse{}
	req := &agentv1.PrepareProtocolCertificateRequest{CertificateId: certificateID, GenerationId: generationID, DnsNames: dnsNames}
	if err := c.unary(ctx, uplinkws.MethodPrepareProtocolCertificate, req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) InstallProtocolCertificate(ctx context.Context, certificateID, generationID, keyID, certificatePEM string, dnsNames []string) (*agentv1.InstallProtocolCertificateResponse, error) {
	resp := &agentv1.InstallProtocolCertificateResponse{}
	req := &agentv1.InstallProtocolCertificateRequest{
		CertificateId: certificateID, GenerationId: generationID, KeyId: keyID,
		CertificatePem: certificatePEM, DnsNames: dnsNames,
	}
	if err := c.unary(ctx, uplinkws.MethodInstallProtocolCertificate, req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) GetProtocolCertificateStatus(ctx context.Context, certificateID, generationID string) (*agentv1.GetProtocolCertificateStatusResponse, error) {
	resp := &agentv1.GetProtocolCertificateStatusResponse{}
	req := &agentv1.GetProtocolCertificateStatusRequest{CertificateId: certificateID, GenerationId: generationID}
	if err := c.unary(ctx, uplinkws.MethodGetProtocolCertificateStatus, req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) DeleteProtocolCertificateGeneration(ctx context.Context, certificateID, generationID string) (*agentv1.DeleteProtocolCertificateGenerationResponse, error) {
	resp := &agentv1.DeleteProtocolCertificateGenerationResponse{}
	req := &agentv1.DeleteProtocolCertificateGenerationRequest{CertificateId: certificateID, GenerationId: generationID}
	if err := c.unary(ctx, uplinkws.MethodDeleteProtocolCertificateGeneration, req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) UpgradeAgent(ctx context.Context, version, repo, downloadURL, sha256 string) (*agentv1.UpgradeAgentResponse, error) {
	resp := &agentv1.UpgradeAgentResponse{}
	req := &agentv1.UpgradeAgentRequest{Version: version, Repo: repo, DownloadUrl: downloadURL, Sha256: sha256}
	if err := c.unary(ctx, uplinkws.MethodUpgradeAgent, req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) GetNodeMetrics(ctx context.Context) (*agentv1.GetNodeMetricsResponse, error) {
	resp := &agentv1.GetNodeMetricsResponse{}
	if err := c.unary(ctx, uplinkws.MethodGetNodeMetrics, &agentv1.GetNodeMetricsRequest{}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) GetBBRStatus(ctx context.Context) (*agentv1.GetBBRStatusResponse, error) {
	resp := &agentv1.GetBBRStatusResponse{}
	if err := c.unary(ctx, uplinkws.MethodGetBBRStatus, &agentv1.GetBBRStatusRequest{}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) SetBBR(ctx context.Context, enabled bool) (*agentv1.SetBBRResponse, error) {
	resp := &agentv1.SetBBRResponse{}
	if err := c.unary(ctx, uplinkws.MethodSetBBR, &agentv1.SetBBRRequest{Enabled: enabled}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// StreamLogs opens a server-streaming log subscription over the socket. The
// returned value satisfies agentv1.AgentControl_StreamLogsClient so callers
// (handleNodeLogs) consume it identically to a gRPC stream.
func (c *Client) StreamLogs(ctx context.Context, level string, tail int32) (agentv1.AgentControl_StreamLogsClient, error) {
	req := &agentv1.StreamLogsRequest{Level: level, Tail: tail}
	payload, err := marshalOpts.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("编码 StreamLogs 请求失败：%w", err)
	}
	reader, err := c.conn.openStream(ctx, uplinkws.MethodStreamLogs, json.RawMessage(payload))
	if err != nil {
		return nil, err
	}
	return &logStream{ctx: ctx, reader: reader}, nil
}

// logStream adapts a streamReader to grpc.ServerStreamingClient[LogLine] so the
// SSE forwarder in handleNodeLogs works transport-agnostically.
type logStream struct {
	ctx    context.Context
	reader *streamReader
}

func (s *logStream) Recv() (*agentv1.LogLine, error) {
	raw, err := s.reader.recv()
	if err != nil {
		return nil, err
	}
	line := &agentv1.LogLine{}
	if err := unmarshalOpts.Unmarshal(raw, line); err != nil {
		return nil, fmt.Errorf("解析日志行失败：%w", err)
	}
	return line, nil
}

// The remaining methods satisfy grpc.ClientStream. Only Context and RecvMsg are
// meaningfully exercised; the rest are inert for a server-streaming consumer.
func (s *logStream) Header() (metadata.MD, error) { return nil, nil }
func (s *logStream) Trailer() metadata.MD         { return nil }
func (s *logStream) CloseSend() error             { s.reader.close(); return nil }
func (s *logStream) Context() context.Context     { return s.ctx }
func (s *logStream) SendMsg(any) error            { return nil }

func (s *logStream) RecvMsg(m any) error {
	line, err := s.Recv()
	if err != nil {
		return err
	}
	if dst, ok := m.(*agentv1.LogLine); ok {
		proto.Merge(dst, line)
	}
	return nil
}
