package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ladderairport/agent/internal/control"
	"github.com/ladderairport/agent/internal/frpsruntime"
	"github.com/ladderairport/agent/internal/managementpki"
	"github.com/ladderairport/agent/internal/panelhttp"
	"github.com/ladderairport/agent/internal/protocolcert"
	"github.com/ladderairport/agent/internal/uplink"
	"github.com/ladderairport/agent/internal/uplinkws"
	"github.com/ladderairport/agent/internal/version"
	"github.com/ladderairport/pkg/auth"
	agentv1 "github.com/ladderairport/proto/gen/go/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func main() {
	listen := flag.String("listen", ":50051", "gRPC 监听地址")
	token := flag.String("token", "", "AgentControl 共享 Bearer 令牌（也可用环境变量 LADDER_TOKEN）")
	tlsCert := flag.String("tls-cert", "", "Panel 签发的 TLS 证书文件（必填）")
	tlsKey := flag.String("tls-key", "", "Agent TLS 私钥文件（必填）")
	tlsClientCA := flag.String("tls-client-ca", "", "Panel 管理 CA 证书包（必填）")
	panelURL := flag.String("panel-url", "", "证书续签使用的 Panel 基础 URL（必填）")
	nodeID := flag.String("node-id", "", "证书身份对应的 Panel 节点 ID（必填）")
	reportAddress := flag.String("report-address", "", "证书续签时上报给 Panel 的地址")
	tlsSANs := flag.String("tls-sans", "", "续签时保留的逗号分隔 DNS/IP SAN")
	dataDir := flag.String("data-dir", "", "配置和状态缓存目录（默认 ./data）")
	uplinkOn := flag.Bool("uplink", false, "向 Panel HTTP 上报并拉取配置（也可用环境变量 LADDER_UPLINK=1）")
	uplinkReport := flag.Duration("uplink-report-interval", 15*time.Second, "uplink 上报间隔")
	uplinkConfig := flag.Duration("uplink-config-interval", 60*time.Second, "uplink 配置拉取间隔")
	uplinkServeGRPC := flag.Bool("uplink-serve-grpc", false, "uplink 模式下仍监听 gRPC 控制端口以保留 push 能力（默认关闭；push 模式始终监听。也可用环境变量 LADDER_UPLINK_SERVE_GRPC=1）")
	uplinkWS := flag.Bool("uplink-ws", true, "uplink 模式下建立 WebSocket 实时通道，提供与 push/gRPC 完全对齐的即时操作（默认开启；HTTP 上报/配置拉取/命令队列作为降级回退保留。也可用环境变量 LADDER_UPLINK_WS=0 关闭）")
	showVersion := flag.Bool("version", false, "显示版本后退出")
	flag.Parse()

	if *showVersion {
		id := version.Report(version.Version)
		fmt.Printf("ladder-agent %s", id.CoreVersion)
		if id.CoreCommit != "" {
			fmt.Printf(" (%s)", id.CoreCommit)
		}
		if id.ClientType != "linux" || id.ClientVersion != id.CoreVersion || id.LuciVersion != "" {
			fmt.Printf(" client=%s/%s", id.ClientType, id.ClientVersion)
			if id.LuciVersion != "" {
				fmt.Printf(" luci=%s", id.LuciVersion)
			}
		}
		fmt.Println()
		return
	}

	if *token == "" {
		*token = os.Getenv("LADDER_TOKEN")
	}
	if !*uplinkOn {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("LADDER_UPLINK"))) {
		case "1", "true", "yes", "on":
			*uplinkOn = true
		}
	}
	if !*uplinkServeGRPC {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("LADDER_UPLINK_SERVE_GRPC"))) {
		case "1", "true", "yes", "on":
			*uplinkServeGRPC = true
		}
	}
	// The WS uplink defaults on; allow LADDER_UPLINK_WS=0 to force the HTTP-only
	// fallback path (report + config-sync + command-queue) without a live socket.
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LADDER_UPLINK_WS"))) {
	case "0", "false", "no", "off":
		*uplinkWS = false
	}
	// serveGRPC decides whether the mTLS gRPC control server is built and listened.
	// push nodes always serve; uplink nodes skip it unless explicitly opted back in.
	serveGRPC := !*uplinkOn || *uplinkServeGRPC
	if *token == "" || *token == "changeme" {
		log.Fatal("必须提供 -token 或环境变量 LADDER_TOKEN（且不允许使用弱默认值 changeme）")
	}
	if *panelURL == "" || *nodeID == "" {
		log.Fatal("必须提供 -panel-url 和 -node-id")
	}
	// Uplink nodes that only report over HTTP and take commands over the
	// WebSocket do not serve mTLS. They skip management TLS entirely unless
	// certificate files are still present from an older install.
	hasTLS := *tlsCert != "" || *tlsKey != "" || *tlsClientCA != ""
	if serveGRPC || hasTLS {
		if *tlsCert == "" || *tlsKey == "" || *tlsClientCA == "" {
			log.Fatal("监听 gRPC 或续签已有证书时，必须同时提供 -tls-cert、-tls-key 和 -tls-client-ca")
		}
	}
	if err := managementpki.ParsePanelURL(*panelURL); err != nil {
		log.Fatal(err)
	}
	if *dataDir == "" {
		*dataDir = "./data"
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("创建数据目录失败：%v", err)
	}

	rt := control.NewBoxRuntime(*dataDir)
	logs := control.NewLogBuf(0)
	// Mirror process logs into the ring buffer so StreamLogs can serve them.
	log.SetOutput(io.MultiWriter(os.Stderr, logs.Writer("info")))
	singboxVer := control.SingboxVersion()
	frpsVer := frpsruntime.Version()
	agentVersion := version.Version
	log.Printf(
		"运行模式=内置代理实例 Agent版本=%s sing-box版本=%s frps版本=%s 数据目录=%q",
		agentVersion, singboxVer, frpsVer, *dataDir,
	)

	srv := control.NewServer(rt, agentVersion, singboxVer, logs)
	srv.SetDataDir(*dataDir)
	resolver := control.NewPublicAddressResolver()
	if echoURLs := control.ParseIPEchoURLs(os.Getenv("LADDER_IP_ECHO_URLS")); len(echoURLs) > 0 {
		resolver.SetEchoURLs(echoURLs)
		log.Printf("公网探测源=LADDER_IP_ECHO_URLS 自定义 %d 个（覆盖默认源列表）", len(echoURLs))
	}
	srv.SetPublicAddressResolver(resolver)
	protocolCerts, err := protocolcert.New(filepath.Join(*dataDir, "protocol-certs"))
	if err != nil {
		log.Fatalf("初始化协议证书存储失败：%v", err)
	}
	srv.SetProtocolCertificateManager(protocolCerts)
	frps := frpsruntime.New(filepath.Join(*dataDir, "frps"))
	if err := frps.Restore(context.Background()); err != nil {
		log.Printf("恢复 FRPS 缓存配置失败：%v", err)
	}
	srv.SetFRPServerRuntime(frps)

	opts := []grpc.ServerOption{
		grpc.UnaryInterceptor(auth.UnaryServerInterceptor(*token)),
		grpc.StreamInterceptor(auth.StreamServerInterceptor(*token)),
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	host, portText, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatalf("解析监听地址 %q 失败：%v", *listen, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		log.Fatalf("解析监听端口 %q 失败：%v", portText, err)
	}
	if *reportAddress != "" {
		host = *reportAddress
	}
	panelHTTP := panelhttp.NewClient()
	var certManager *managementpki.Manager
	if serveGRPC || hasTLS {
		certManager, err = managementpki.New(managementpki.Config{
			PanelURL:   *panelURL,
			NodeID:     *nodeID,
			Token:      *token,
			CertPath:   *tlsCert,
			KeyPath:    *tlsKey,
			CAPath:     *tlsClientCA,
			Address:    host,
			GRPCPort:   port,
			SANs:       strings.Split(*tlsSANs, ","),
			HTTPClient: panelHTTP,
		})
		if err != nil {
			log.Fatalf("加载管理面 TLS 失败：%v", err)
		}
	}
	if serveGRPC {
		tlsConfig := &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: certManager.GetCertificate,
		}
		// Cache the parsed client CA pool; rebuilt only when the CA file's mtime
		// changes (renewal rewrites it) instead of on every handshake.
		caPoolCache := managementpki.NewClientCAPoolCache(*tlsClientCA)
		pool, err := caPoolCache.Pool()
		if err != nil {
			log.Fatalf("加载 Panel 客户端 CA 失败：%v", err)
		}
		tlsConfig.ClientCAs = pool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		tlsConfig.VerifyPeerCertificate = managementpki.VerifyPanelIdentity
		tlsConfig.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
			currentPool, err := caPoolCache.Pool()
			if err != nil {
				return nil, err
			}
			current := tlsConfig.Clone()
			current.GetConfigForClient = nil
			current.ClientCAs = currentPool
			return current, nil
		}
		log.Printf("mTLS=强制 证书=%s 客户端CA=%s", *tlsCert, *tlsClientCA)
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsConfig)))
	} else if certManager != nil {
		log.Printf("uplink=不监听 gRPC；仍续签已有管理证书（如需保留 push 能力设 LADDER_UPLINK_SERVE_GRPC=1）")
	} else {
		log.Printf("uplink=HTTP 上报 + WebSocket（不初始化管理面 TLS，不监听 gRPC）")
	}
	if certManager != nil {
		go certManager.Run(runCtx)
	}
	if *uplinkOn {
		uplinkClient, err := uplink.New(uplink.Config{
			PanelURL:    *panelURL,
			NodeID:      *nodeID,
			Token:       *token,
			ReportEvery: *uplinkReport,
			ConfigEvery: *uplinkConfig,
			HTTPClient:  panelHTTP,
			Control:     srv,
		})
		if err != nil {
			log.Fatalf("初始化 HTTP uplink 失败：%v", err)
		}
		go uplinkClient.Run(runCtx)
		log.Printf("uplink=HTTP 上报间隔=%s 配置拉取间隔=%s", *uplinkReport, *uplinkConfig)

		if *uplinkWS {
			// The WS uplink is the live channel: Panel drives the full AgentControl
			// surface (probe, logs, protocol certs, DNS public probe, FRPS, …) over
			// one socket at parity with push/gRPC. The HTTP paths above remain as a
			// degraded fallback when the socket is unavailable.
			wsClient, err := uplinkws.New(uplinkws.Config{
				PanelURL:    *panelURL,
				NodeID:      *nodeID,
				Token:       *token,
				ReportEvery: *uplinkReport,
				HTTPClient:  panelHTTP,
				Server:      srv,
			})
			if err != nil {
				log.Fatalf("初始化 WS uplink 失败：%v", err)
			}
			go wsClient.Run(runCtx)
			log.Printf("uplink=WS 实时通道已启用（与 push/gRPC 完全对齐；HTTP 路径降级回退）")
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if !serveGRPC {
		// uplink-only: no gRPC listener. Stay alive on the uplink client + renewal
		// goroutines until a termination signal arrives.
		sig := <-sigCh
		log.Printf("收到信号 %v，正在停止", sig)
		cancelRun()
		_ = frps.Stop(context.Background())
		_ = rt.Stop(context.Background())
		fmt.Fprintln(os.Stderr, "bye")
		return
	}

	gs := grpc.NewServer(opts...)
	agentv1.RegisterAgentControlServer(gs, srv)

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("监听 %s 失败：%v", *listen, err)
	}
	log.Printf("AgentControl 正在监听 %s", lis.Addr())

	errCh := make(chan error, 1)
	go func() {
		errCh <- gs.Serve(lis)
	}()

	select {
	case sig := <-sigCh:
		log.Printf("收到信号 %v，正在停止", sig)
		cancelRun()
		gs.GracefulStop()
		_ = frps.Stop(context.Background())
		_ = rt.Stop(context.Background())
	case err := <-errCh:
		if err != nil {
			log.Fatalf("Agent 服务运行失败：%v", err)
		}
	}
	fmt.Fprintln(os.Stderr, "bye")
}
