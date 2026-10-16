package uplink

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	agentv1 "github.com/ladderairport/proto/gen/go/agent/v1"
)

// Command types mirror the Panel-side names in handlers_agent_commands.go.
const (
	cmdProbe        = "probe"
	cmdInterfaces   = "interfaces"
	cmdUpgrade      = "upgrade"
	cmdSysMetrics   = "sysmetrics"
	cmdBBRStatus    = "bbr-status"
	cmdBBRSet       = "bbr-set"
	cmdFRPSMappings = "frps-mappings"
	cmdFRPSStart    = "frps-start"
	cmdFRPSStop     = "frps-stop"
)

// pulledCommand is one queued operation delivered by Panel's long-poll.
type pulledCommand struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

type pollCommandsResponse struct {
	Commands []pulledCommand `json:"commands"`
}

type commandResultBody struct {
	OK     bool   `json:"ok"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// runCommandLoop continuously long-polls Panel for queued commands, executes
// each locally against the control surface, and posts the result back. On any
// transport error it backs off briefly before retrying so a flapping Panel
// connection does not spin.
func (c *Client) runCommandLoop(ctx context.Context) {
	if c.cfg.Control == nil {
		return
	}
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		cmds, err := c.pollCommands(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("uplink 拉取命令失败：%v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for i := range cmds {
			if ctx.Err() != nil {
				return
			}
			c.executeCommand(ctx, cmds[i])
		}
	}
}

func (c *Client) pollCommands(ctx context.Context) ([]pulledCommand, error) {
	wait := c.cfg.CommandWait
	endpoint := "/api/v1/agent/commands?node_id=" + url.QueryEscape(c.cfg.NodeID) +
		"&wait=" + url.QueryEscape(wait.String())
	// The HTTP client timeout must exceed the server wait budget; add slack.
	raw, err := c.getJSON(ctx, endpoint, wait+10*time.Second)
	if err != nil {
		return nil, err
	}
	var resp pollCommandsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("解析命令响应失败：%w", err)
	}
	return resp.Commands, nil
}

// executeCommand runs a single command and reports its terminal result. A
// dispatch/marshal error is reported as a failed result so the command reaches
// a terminal state instead of being redelivered forever.
func (c *Client) executeCommand(ctx context.Context, cmd pulledCommand) {
	result, execErr := c.dispatchCommand(ctx, cmd)
	body := commandResultBody{OK: execErr == nil}
	if execErr != nil {
		body.Error = execErr.Error()
	} else {
		body.Result = result
	}
	path := "/api/v1/agent/commands/" + url.PathEscape(cmd.ID) + "/result"
	if _, err := c.postJSON(ctx, path, body); err != nil {
		log.Printf("uplink 回报命令 %s 结果失败：%v", cmd.ID, err)
	}
}

// dispatchCommand maps a command type to the matching control-surface call and
// returns its JSON-encoded result. The JSON shapes mirror the Panel live-RPC
// handlers so the browser sees identical payloads for push and uplink nodes.
func (c *Client) dispatchCommand(ctx context.Context, cmd pulledCommand) (string, error) {
	ctrl := c.cfg.Control
	switch cmd.Type {
	case cmdProbe:
		resp, err := ctrl.Ping(ctx, &agentv1.PingRequest{})
		if err != nil {
			return "", err
		}
		return marshalResult(map[string]any{
			"ok":              true,
			"agent_version":   resp.GetAgentVersion(),
			"singbox_version": resp.GetSingboxVersion(),
			"core_version":    resp.GetCoreVersion(),
			"core_commit":     resp.GetCoreCommit(),
			"client_type":     resp.GetClientType(),
			"client_version":  resp.GetClientVersion(),
			"luci_version":    resp.GetLuciVersion(),
			"capabilities":    resp.GetCapabilities(),
		})
	case cmdInterfaces:
		resp, err := ctrl.ListInterfaces(ctx, &agentv1.ListInterfacesRequest{})
		if err != nil {
			return "", err
		}
		list := make([]map[string]any, 0, len(resp.GetInterfaces()))
		for _, iface := range resp.GetInterfaces() {
			addrs := iface.GetAddresses()
			if addrs == nil {
				addrs = []string{}
			}
			list = append(list, map[string]any{
				"name":          iface.GetName(),
				"addresses":     addrs,
				"up":            iface.GetUp(),
				"loopback":      iface.GetLoopback(),
				"mtu":           iface.GetMtu(),
				"hardware_addr": iface.GetHardwareAddr(),
			})
		}
		return marshalResult(map[string]any{"interfaces": list})
	case cmdUpgrade:
		var payload struct {
			Version     string `json:"version"`
			Repo        string `json:"repo"`
			DownloadURL string `json:"download_url"`
			SHA256      string `json:"sha256"`
		}
		if err := decodePayload(cmd.Payload, &payload); err != nil {
			return "", err
		}
		resp, err := ctrl.UpgradeAgent(ctx, &agentv1.UpgradeAgentRequest{
			Version:     payload.Version,
			Repo:        payload.Repo,
			DownloadUrl: payload.DownloadURL,
			Sha256:      payload.SHA256,
		})
		if err != nil {
			return "", err
		}
		return marshalResult(map[string]any{
			"ok":               resp.GetOk(),
			"message":          resp.GetMessage(),
			"version":          resp.GetVersion(),
			"staged_path":      resp.GetStagedPath(),
			"previous_version": resp.GetPreviousVersion(),
		})
	case cmdSysMetrics:
		resp, err := ctrl.GetNodeMetrics(ctx, &agentv1.GetNodeMetricsRequest{})
		if err != nil {
			return "", err
		}
		return marshalResult(map[string]any{
			"cpu_percent":        resp.GetCpuPercent(),
			"memory_total_bytes": resp.GetMemoryTotalBytes(),
			"memory_used_bytes":  resp.GetMemoryUsedBytes(),
			"disk_total_bytes":   resp.GetDiskTotalBytes(),
			"disk_used_bytes":    resp.GetDiskUsedBytes(),
			"uplink_bps":         resp.GetUplinkBps(),
			"downlink_bps":       resp.GetDownlinkBps(),
			"collected_at_unix":  resp.GetCollectedAtUnix(),
		})
	case cmdBBRStatus:
		resp, err := ctrl.GetBBRStatus(ctx, &agentv1.GetBBRStatusRequest{})
		if err != nil {
			return "", err
		}
		supported := resp.GetSupportedControls()
		if supported == nil {
			supported = []string{}
		}
		return marshalResult(map[string]any{
			"bbr_available":              resp.GetBbrAvailable(),
			"bbr_enabled":                resp.GetBbrEnabled(),
			"current_congestion_control": resp.GetCurrentCongestionControl(),
			"current_qdisc":              resp.GetCurrentQdisc(),
			"supported_controls":         supported,
		})
	case cmdBBRSet:
		var payload struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodePayload(cmd.Payload, &payload); err != nil {
			return "", err
		}
		resp, err := ctrl.SetBBR(ctx, &agentv1.SetBBRRequest{Enabled: payload.Enabled})
		if err != nil {
			return "", err
		}
		return marshalResult(map[string]any{
			"ok":      resp.GetOk(),
			"message": resp.GetMessage(),
		})
	case cmdFRPSMappings:
		resp, err := ctrl.GetFRPServerMappings(ctx, &agentv1.GetFRPServerMappingsRequest{})
		if err != nil {
			return "", err
		}
		return marshalProto(resp)
	case cmdFRPSStart:
		if _, err := ctrl.StartFRPServer(ctx, &agentv1.StartFRPServerRequest{}); err != nil {
			return "", err
		}
		return c.frpsStatusResult(ctx)
	case cmdFRPSStop:
		if _, err := ctrl.StopFRPServer(ctx, &agentv1.StopFRPServerRequest{}); err != nil {
			return "", err
		}
		return c.frpsStatusResult(ctx)
	default:
		return "", fmt.Errorf("未知命令类型 %q", cmd.Type)
	}
}

func (c *Client) frpsStatusResult(ctx context.Context) (string, error) {
	resp, err := c.cfg.Control.GetFRPServerStatus(ctx, &agentv1.GetFRPServerStatusRequest{})
	if err != nil {
		return "", err
	}
	return marshalResult(map[string]any{
		"state":           resp.GetState(),
		"config_hash":     resp.GetConfigHash(),
		"started_at_unix": resp.GetStartedAtUnix(),
		"last_error":      resp.GetLastError(),
		"frps_version":    resp.GetFrpsVersion(),
	})
}

func marshalResult(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func marshalProto(v any) (string, error) {
	// proto-generated messages carry JSON tags; a plain marshal keeps snake_case.
	return marshalResult(v)
}

func decodePayload(raw string, dest any) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), dest); err != nil {
		return fmt.Errorf("解析命令参数失败：%w", err)
	}
	return nil
}
