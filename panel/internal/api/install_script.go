package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/ladderairport/panel/internal/store"
)

const releaseDownloadBaseURL = "https://github.com/LadderAirport/LadderAirport/releases"
const defaultInstallScriptURL = releaseDownloadBaseURL + "/latest/download/install-agent.sh"

func releaseScriptURL(asset, version string) string {
	version = strings.TrimSpace(version)
	if version == "" || version == "latest" {
		return releaseDownloadBaseURL + "/latest/download/" + asset
	}
	return releaseDownloadBaseURL + "/download/" + url.PathEscape(version) + "/" + asset
}

func randomAgentToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// installCommandOpts configures the curl|bash one-liner.
type installCommandOpts struct {
	ScriptURL       string
	EnrollmentToken string
	AgentVersion    string
	PanelBaseURL    string
	NodeID          string
	GRPCPort        int
	ReportAddress   string
	Listen          string // optional LADDER_LISTEN override
	Uplink          bool
}

// buildInstallCommand produces a strict Panel-PKI installation command.
func buildInstallCommand(opts installCommandOpts) string {
	scriptURL := opts.ScriptURL
	if scriptURL == "" {
		scriptURL = releaseScriptURL("install-agent.sh", opts.AgentVersion)
	}
	var b strings.Builder
	b.WriteString("curl -fsSL ")
	b.WriteString(shellSingleQuote(scriptURL))
	b.WriteString(" | sudo env")
	b.WriteString(" LADDER_ENROLL_TOKEN=")
	b.WriteString(shellSingleQuote(opts.EnrollmentToken))
	if opts.AgentVersion != "" && opts.AgentVersion != "latest" {
		b.WriteString(" LADDER_VERSION=")
		b.WriteString(shellSingleQuote(opts.AgentVersion))
	}
	panel := strings.TrimRight(strings.TrimSpace(opts.PanelBaseURL), "/")
	if panel != "" {
		b.WriteString(" LADDER_PANEL=")
		b.WriteString(shellSingleQuote(panel))
		if opts.NodeID != "" {
			b.WriteString(" LADDER_NODE_ID=")
			b.WriteString(shellSingleQuote(opts.NodeID))
		}
		if opts.GRPCPort > 0 {
			b.WriteString(" LADDER_GRPC_PORT=")
			b.WriteString(fmt.Sprintf("%d", opts.GRPCPort))
		}
		if opts.ReportAddress != "" {
			b.WriteString(" LADDER_REPORT_ADDRESS=")
			b.WriteString(shellSingleQuote(opts.ReportAddress))
		}
	}
	if opts.Listen != "" {
		b.WriteString(" LADDER_LISTEN=")
		b.WriteString(shellSingleQuote(opts.Listen))
	}
	if opts.Uplink {
		b.WriteString(" LADDER_UPLINK=1")
	}
	b.WriteString(" bash")
	return b.String()
}

// buildUpgradeCommand produces a curl|bash one-liner that only replaces the binary
// (LADDER_ACTION=upgrade). Token/TLS/env are left untouched on the node.
func buildUpgradeCommand(opts installCommandOpts) string {
	scriptURL := opts.ScriptURL
	if scriptURL == "" {
		scriptURL = releaseScriptURL("install-agent.sh", opts.AgentVersion)
	}
	var b strings.Builder
	b.WriteString("curl -fsSL ")
	b.WriteString(shellSingleQuote(scriptURL))
	b.WriteString(" | sudo env LADDER_ACTION=upgrade")
	if opts.AgentVersion != "" && opts.AgentVersion != "latest" {
		b.WriteString(" LADDER_VERSION=")
		b.WriteString(shellSingleQuote(opts.AgentVersion))
	}
	b.WriteString(" bash")
	return b.String()
}

// buildUninstallCommand produces a curl|bash one-liner to stop the agent service
// and remove unit/binary. Purge removes conf/data as well.
func buildUninstallCommand(opts installCommandOpts, purge bool) string {
	scriptURL := opts.ScriptURL
	if scriptURL == "" {
		scriptURL = defaultInstallScriptURL
	}
	var b strings.Builder
	b.WriteString("curl -fsSL ")
	b.WriteString(shellSingleQuote(scriptURL))
	b.WriteString(" | sudo env LADDER_ACTION=uninstall")
	if purge {
		b.WriteString(" LADDER_PURGE=1")
	}
	b.WriteString(" bash")
	return b.String()
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func panelBaseFromSettings(publicBaseURL string) string {
	return strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
}

func nodeAwaitingRegistration(n store.Node, enrolled bool) bool {
	if n.ControlMode == store.ControlModeUplink {
		return n.PKICertSerial == "" && !enrolled
	}
	return n.PKICertSerial == ""
}

func installSteps(address string, grpcPort int, uplink bool) []string {
	var steps []string
	if uplink {
		steps = []string{
			"在目标服务器（Linux amd64/arm64/armhf）以 root 执行上方一键安装命令。",
			"uplink 节点不生成管理面私钥，也不向 Panel 申请 TLS 证书。安装只交换一次性注册令牌。",
			"安装后 Agent 用 HTTP 上报状态，并用 WebSocket 长连接接收配置和即时操作。",
			"回到 Panel 刷新节点列表，确认节点在线。",
		}
	} else {
		steps = []string{
			"在目标服务器（Linux amd64/arm64/armhf）以 root 执行上方一键安装命令。",
			"安装脚本会在节点本地生成私钥与 CSR，由 Panel 管理 CA 签发 30 天证书，并强制启用 mTLS。",
			"安装时会调用 Panel 证书接口完成身份注册；私钥始终留在节点，证书到期前由 Agent 自动续签。",
			"回到 Panel 刷新节点列表，确认管理证书已绑定后点「探测」。",
		}
	}
	if uplink {
		if strings.TrimSpace(address) == "" {
			steps = append(steps, "控制面地址可以留空。客户端入口与节点出口不同时，再在节点详情填写公网地址。")
		}
	} else if strings.TrimSpace(address) != "" {
		steps = append(steps, fmt.Sprintf("控制面地址已预填时注册不会改写；端口转发请确认 gRPC 端口为外部映射端口（当前 %d）。", grpcPort))
	} else {
		steps = append(steps, "NAT/端口转发：在节点详情填写 Panel 可达的控制面地址与映射端口；客户端入口不同时再填「公网地址」。")
	}
	steps = append(steps, "节点在线后即可关联入站并下发配置。")
	return steps
}
