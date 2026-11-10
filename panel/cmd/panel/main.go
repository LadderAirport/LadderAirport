package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	acmeservice "github.com/ladderairport/panel/internal/acme"
	"github.com/ladderairport/panel/internal/api"
	"github.com/ladderairport/panel/internal/automation"
	"github.com/ladderairport/panel/internal/batch"
	"github.com/ladderairport/panel/internal/certmanager"
	"github.com/ladderairport/panel/internal/dnsprovider"
	"github.com/ladderairport/panel/internal/dnsproviders"
	"github.com/ladderairport/panel/internal/dnsreconcile"
	"github.com/ladderairport/panel/internal/pki"
	"github.com/ladderairport/panel/internal/secretstore"
	"github.com/ladderairport/panel/internal/store"
	"github.com/ladderairport/panel/internal/subscription"
	"github.com/ladderairport/panel/internal/uplinkhub"
	"github.com/ladderairport/panel/internal/version"
)

func main() {
	dbPath := flag.String("db", "./data/panel.db", "SQLite 数据库路径")
	listen := flag.String("listen", "", "HTTP 监听地址（默认读取 settings.listen_addr 或使用 :8080）")
	sessionSecret := flag.String("session-secret", "", "JWT 会话 HMAC 密钥（为空时依次尝试 LADDER_SESSION_SECRET、持久化文件，最后随机生成）")
	bootstrap := flag.Bool("bootstrap", true, "启动时向所有已注册节点下发配置并启动 sing-box")
	bootstrapTimeout := flag.Duration("bootstrap-timeout", 3*time.Minute, "首次启动下发的超时时间")
	bootstrapRetry := flag.Bool("bootstrap-retry", true, "定期重试尚未在线或运行节点的下发与启动")
	bootstrapRetryInterval := flag.Duration("bootstrap-retry-interval", 30*time.Second, "启动重试间隔")
	showVersion := flag.Bool("version", false, "显示版本后退出")
	pkiDir := flag.String("pki-dir", "", "管理 PKI 目录（默认：<数据库目录>/pki）")
	pkiRotateIntermediate := flag.Bool("pki-rotate-intermediate", false, "轮换在线中间 CA 和 Panel 客户端证书后退出")
	credentialsKeyFile := flag.String(
		"credentials-key-file",
		"",
		"Panel 凭据主密钥文件（默认：<数据库目录>/secrets/credentials.key）",
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("ladder-panel %s", version.Version)
		if version.Commit != "" && version.Commit != "unknown" {
			fmt.Printf(" (%s)", version.Commit)
		}
		fmt.Println()
		return
	}

	if dir := filepath.Dir(*dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("创建数据库目录失败：%v", err)
		}
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("打开数据存储失败：%v", err)
	}
	defer func() { _ = st.Close() }()

	keyFile := *credentialsKeyFile
	if keyFile == "" {
		keyFile = filepath.Join(filepath.Dir(*dbPath), "secrets", "credentials.key")
	}
	credentialsKey, err := secretstore.LoadOrCreateKey(secretstore.KeyOptions{
		EnvironmentValue: os.Getenv("LADDER_CREDENTIALS_KEY"),
		FilePath:         keyFile,
	})
	if err != nil {
		log.Fatalf("打开 DNS/ACME 凭据主密钥失败：%v", err)
	}
	secrets, err := secretstore.New(credentialsKey)
	if err != nil {
		log.Fatalf("初始化 DNS/ACME 凭据保险箱失败：%v", err)
	}
	log.Printf("DNS/ACME 凭据保险箱已启用（密钥来源=%s）", credentialKeySource(keyFile))
	dnsRegistry := dnsprovider.NewRegistry()
	if err := dnsproviders.RegisterBuiltins(dnsRegistry); err != nil {
		log.Fatalf("注册 DNS 供应商失败：%v", err)
	}

	dir := *pkiDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(*dbPath), "pki")
	}
	ca, err := pki.Open(dir)
	if err != nil {
		log.Fatalf("打开管理 PKI 失败：%v", err)
	}
	if *pkiRotateIntermediate {
		if err := ca.RotateIntermediate(time.Now()); err != nil {
			log.Fatalf("轮换管理中间 CA 失败：%v", err)
		}
		log.Printf("管理中间 CA 已轮换，请将 %s 移回离线存储", filepath.Join(dir, "offline", "root-ca.key"))
		return
	}
	log.Printf("管理 PKI 已启用（目录=%s，中间 CA 到期时间=%s）", dir, time.Unix(ca.Status().IntermediateNotAfterUnix, 0).Format(time.RFC3339))
	ca.AuditLog = st.AddPKIAudit
	go ca.Run(context.Background())

	if err := api.EnsureAdminPassword(st); err != nil {
		log.Fatalf("初始化管理员密码失败：%v", err)
	}

	settings, err := st.GetSettings()
	if err != nil {
		log.Fatalf("读取系统设置失败：%v", err)
	}

	secret := []byte(*sessionSecret)
	secretSource := "-session-secret"
	if len(secret) == 0 {
		if env := os.Getenv("LADDER_SESSION_SECRET"); env != "" {
			secret = []byte(env)
			secretSource = "LADDER_SESSION_SECRET"
		}
	}
	if len(secret) == 0 {
		// Persist next to the PKI directory so sessions survive restarts.
		secretFile := filepath.Join(filepath.Dir(dir), "session.secret")
		secret, err = loadOrCreateSessionSecret(secretFile)
		if err != nil {
			log.Printf("读取会话密钥文件失败：%v", err)
		} else {
			secretSource = secretFile
		}
	}
	if len(secret) == 0 {
		secret, err = randomSecret(32)
		if err != nil {
			log.Fatalf("生成会话密钥失败：%v", err)
		}
		log.Printf("未设置会话密钥，已生成临时密钥（重启后现有会话将失效）")
	} else {
		log.Printf("会话密钥来源=%s", secretSource)
	}

	runner := batch.NewRunner(st, func() string {
		cur, err := st.GetSettings()
		if err != nil {
			return settings.DefaultAgentToken
		}
		return cur.DefaultAgentToken
	})
	runner.PKI = ca
	if settings.GRPCTimeoutSec > 0 {
		runner.Timeout.Store(int64(time.Duration(settings.GRPCTimeoutSec) * time.Second))
	}
	if settings.MaxConcurrency > 0 {
		runner.MaxConcurrency.Store(int64(settings.MaxConcurrency))
	}
	dnsService := &dnsreconcile.Service{
		Store:     st,
		Secrets:   secrets,
		Providers: dnsRegistry,
		Timeout:   runner.OperationTimeout(),
		DefaultToken: func() string {
			cur, err := st.GetSettings()
			if err != nil {
				return settings.DefaultAgentToken
			}
			return cur.DefaultAgentToken
		},
		DialAgent: func(ctx context.Context, node store.Node, token string) (dnsreconcile.Agent, error) {
			return runner.DialClient(ctx, node, token)
		},
	}
	acmeService := &acmeservice.Service{Secrets: secrets}
	certificateService := &certmanager.Service{
		Store: st, Secrets: secrets, Providers: dnsRegistry, ACME: acmeService,
		Timeout: 10 * time.Minute, ConfigBuilder: runner.ConfigBuilder,
		Coordinator: runner.Coordinator,
		DefaultToken: func() string {
			cur, err := st.GetSettings()
			if err != nil {
				return settings.DefaultAgentToken
			}
			return cur.DefaultAgentToken
		},
		DialAgent: func(ctx context.Context, node store.Node, token string) (certmanager.Agent, error) {
			return runner.DialClient(ctx, node, token)
		},
	}
	automationRunner := &automation.Runner{
		Store: st, DNS: dnsService, Certificates: certificateService,
		Owner: automationOwner(),
	}

	agg := subscription.NewAggregator(st)
	uplinkHub := uplinkhub.NewHub()
	// Wire the live WS uplink into every service that must reach a connected
	// uplink node in real time, at parity with the push/gRPC control plane.
	certificateService.UplinkClient = func(nodeID string) (certmanager.Agent, bool) {
		return uplinkHub.Client(nodeID)
	}
	dnsService.UplinkClient = func(nodeID string) (dnsreconcile.Agent, bool) {
		return uplinkHub.Client(nodeID)
	}
	srv := &api.Server{
		Store:        st,
		Runner:       runner,
		Secret:       secret,
		Aggregator:   agg,
		PKI:          ca,
		Secrets:      secrets,
		DNSProviders: dnsRegistry,
		ACME:         acmeService,
		Uplink:       uplinkHub,
	}

	addr := *listen
	if addr == "" {
		addr = settings.ListenAddr
	}
	if addr == "" {
		addr = ":8080"
	}

	// Auto push configs + start agents (background; does not block HTTP).
	if *bootstrap {
		go func() {
			// Small delay so ListenAndServe is up and agents have a moment if co-started.
			time.Sleep(500 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), *bootstrapTimeout)
			defer cancel()
			log.Printf("启动下发：开始（超时=%s）", *bootstrapTimeout)
			if err := runner.BootstrapAll(ctx); err != nil {
				log.Printf("启动下发：完成但发生错误：%v", err)
			} else {
				log.Printf("启动下发：完成")
			}
		}()
	} else {
		log.Printf("启动下发：已禁用（-bootstrap=false）")
	}

	// Keep retrying nodes that come online later (agent started after panel, etc.).
	if *bootstrap && *bootstrapRetry {
		go func() {
			log.Printf("启动重试：已启用（间隔=%s）", *bootstrapRetryInterval)
			runner.RunBootstrapRetryLoop(context.Background(), *bootstrapRetryInterval)
		}()
	} else if *bootstrap {
		log.Printf("启动重试：已禁用（-bootstrap-retry=false）")
	}

	// Background refresh of external subscription sources.
	go func() {
		log.Printf("外部源后台刷新已启用（间隔=%s）", subscription.BackgroundTick)
		agg.RunBackground(context.Background(), subscription.BackgroundTick)
	}()
	go automationRunner.Run(context.Background())

	// Retention cleanup: once at startup, then hourly.
	go func() {
		prune := func() {
			summary, err := st.PruneExpiredData(time.Now())
			if err != nil {
				log.Printf("数据清理失败：%v", err)
				return
			}
			if summary.Total() > 0 {
				log.Printf("数据清理完成：任务=%d，快照=%d，PKI 审计=%d，自动化审计=%d，注册令牌=%d",
					summary.Tasks, summary.Snapshots, summary.PKIAuditLogs,
					summary.AutomationAuditLogs, summary.EnrollmentTokens)
			}
		}
		prune()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			prune()
		}
	}()

	log.Printf("Panel 正在监听 %s（数据库=%s，版本=%s）", addr, *dbPath, version.Version)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       3 * time.Minute,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("启动监听失败：%v", err)
	}
}

func automationOwner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

func credentialKeySource(file string) string {
	if os.Getenv("LADDER_CREDENTIALS_KEY") != "" {
		return "LADDER_CREDENTIALS_KEY"
	}
	return file
}

func randomSecret(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// loadOrCreateSessionSecret reads a hex-encoded session secret from path,
// generating a fresh 32-byte secret (mode 0600) when the file is missing.
func loadOrCreateSessionSecret(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		secret, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(secret) == 0 {
			return nil, fmt.Errorf("会话密钥文件 %s 内容无效", path)
		}
		return secret, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	secret, err := randomSecret(32)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		// Another process won the creation race.
		return loadOrCreateSessionSecret(path)
	}
	if err != nil {
		return nil, err
	}
	if _, err := file.WriteString(hex.EncodeToString(secret) + "\n"); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return secret, nil
}
