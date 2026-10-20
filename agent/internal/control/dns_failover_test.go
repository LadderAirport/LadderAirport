package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ladderairport/pkg/nodedns"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	sjson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"

	mdns "github.com/miekg/dns"
)

// TestLiveDNSFailover starts sing-box on a generated 1.14 DNS block and queries
// the running router. Earlier rows are skipped only for transport errors,
// timeouts, SERVFAIL, and empty NOERROR. NXDOMAIN is returned as-is.
func TestLiveDNSFailover(t *testing.T) {
	const want = "203.0.113.10"
	t.Run("servfail", func(t *testing.T) {
		assertFailover(t, "servfail", "a", want, false)
	})
	t.Run("empty", func(t *testing.T) {
		assertFailover(t, "empty", "a", want, false)
	})
	t.Run("blackhole", func(t *testing.T) {
		assertFailover(t, "blackhole", "a", want, false)
	})
	t.Run("nxdomain", func(t *testing.T) {
		assertFailover(t, "nxdomain", "a", "", true)
	})
	t.Run("first answers", func(t *testing.T) {
		assertFailover(t, "a", "a", want, false)
	})
}

func assertFailover(t *testing.T, firstMode, secondMode, want string, wantNX bool) {
	t.Helper()
	first := startScriptedDNS(t, firstMode, want)
	second := startScriptedDNS(t, secondMode, want)
	final := startScriptedDNS(t, "a", "203.0.113.99")
	router, stop := startGeneratedBox(t, dnsDocument(t, []nodedns.Server{
		{Name: "one", URL: "udp://" + first.addr},
		{Name: "two", URL: "udp://" + second.addr},
		{Name: "three", URL: "udp://" + final.addr},
	}))
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	started := time.Now()
	if wantNX {
		msg := new(mdns.Msg)
		msg.SetQuestion("nx.example.", mdns.TypeA)
		resp, err := router.Exchange(ctx, msg, adapter.DNSQueryOptions{})
		if err != nil {
			t.Fatalf("exchange: %v", err)
		}
		if resp.Rcode != mdns.RcodeNameError {
			t.Fatalf("rcode %s, want NXDOMAIN", mdns.RcodeToString[resp.Rcode])
		}
		if second.hits() != 0 || final.hits() != 0 {
			t.Fatalf("NXDOMAIN queried later rows: second=%d final=%d", second.hits(), final.hits())
		}
		return
	}
	addrs, err := router.Lookup(ctx, "example.com", adapter.DNSQueryOptions{})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(addrs) != 1 || addrs[0].String() != want {
		t.Fatalf("addresses %v, want %s", addrs, want)
	}
	if first.hits() == 0 {
		t.Fatal("first server was not queried")
	}
	if firstMode == "a" {
		if second.hits() != 0 || final.hits() != 0 {
			t.Fatalf("later rows queried after a good answer: second=%d final=%d", second.hits(), final.hits())
		}
		return
	}
	if second.hits() == 0 {
		t.Fatal("second server was not queried after the first failed")
	}
	if final.hits() != 0 {
		t.Fatalf("final server queried after the second answered: %d", final.hits())
	}
	if firstMode == "blackhole" && time.Since(started) < time.Second {
		t.Fatalf("blackhole returned in %s, timeout was not applied", time.Since(started))
	}
}

func dnsDocument(t *testing.T, servers []nodedns.Server) map[string]any {
	t.Helper()
	cfg := nodedns.Default()
	cfg.Strategy = nodedns.StrategyIPv4Only
	cfg.Servers = servers
	emitted, err := cfg.Emit(nodedns.EmitOptions{Failover: true})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"log":       map[string]any{"level": "error"},
		"dns":       emitted.DNS,
		"inbounds":  []any{},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"final": "direct"},
	}
}

func startGeneratedBox(t *testing.T, document map[string]any) (adapter.DNSRouter, func()) {
	t.Helper()
	raw, err := stdjsonMarshal(document)
	if err != nil {
		t.Fatal(err)
	}
	ctx := include.Context(context.Background())
	opts, err := sjson.UnmarshalExtendedContext[option.Options](ctx, raw)
	if err != nil {
		t.Fatalf("parse generated config: %v", err)
	}
	instance, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		t.Fatalf("box.New: %v", err)
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		t.Fatalf("box.Start: %v", err)
	}
	router := service.FromContext[adapter.DNSRouter](ctx)
	if router == nil {
		_ = instance.Close()
		t.Fatal("dns router missing")
	}
	return router, func() { _ = instance.Close() }
}

type scriptedDNS struct {
	addr string
	mode string
	ip   string
	n    sync.Mutex
	seen int
	stop func()
}

func (s *scriptedDNS) hits() int {
	s.n.Lock()
	defer s.n.Unlock()
	return s.seen
}

func startScriptedDNS(t *testing.T, mode, ip string) *scriptedDNS {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	srv := &scriptedDNS{addr: conn.LocalAddr().String(), mode: mode, ip: ip, stop: func() { _ = conn.Close() }}
	go srv.serve(conn)
	t.Cleanup(srv.stop)
	return srv
}

func (s *scriptedDNS) serve(conn *net.UDPConn) {
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		s.n.Lock()
		s.seen++
		mode := s.mode
		s.n.Unlock()
		if mode == "blackhole" {
			continue
		}
		var req mdns.Msg
		if err := req.Unpack(buf[:n]); err != nil || len(req.Question) == 0 {
			continue
		}
		resp := new(mdns.Msg)
		resp.SetReply(&req)
		switch mode {
		case "servfail":
			resp.Rcode = mdns.RcodeServerFailure
		case "empty":
			resp.Rcode = mdns.RcodeSuccess
		case "nxdomain":
			resp.Rcode = mdns.RcodeNameError
		default:
			resp.Rcode = mdns.RcodeSuccess
			resp.Answer = []mdns.RR{&mdns.A{
				Hdr: mdns.RR_Header{Name: req.Question[0].Name, Rrtype: mdns.TypeA, Class: mdns.ClassINET, Ttl: 30},
				A:   net.ParseIP(s.ip).To4(),
			}}
		}
		packed, err := resp.Pack()
		if err != nil {
			continue
		}
		_, _ = conn.WriteToUDP(packed, from)
	}
}

func stdjsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// TestGeneratedInboundsStart builds one node-shaped config containing every
// inbound the panel emits, plus a 1.14 DNS block, and starts it in-process.
func TestGeneratedInboundsStart(t *testing.T) {
	certPEM, keyPEM := testCertPEM(t)
	var realityKey [32]byte
	if _, err := rand.Read(realityKey[:]); err != nil {
		t.Fatal(err)
	}
	priv := base64.RawURLEncoding.EncodeToString(realityKey[:])
	tlsBlock := map[string]any{
		"enabled":     true,
		"server_name": "node.example",
		"certificate": []string{certPEM},
		"key":         []string{keyPEM},
	}
	userUUID := "11111111-1111-4111-8111-111111111111"
	inbounds := []any{
		map[string]any{"type": "shadowsocks", "tag": "ss", "listen": "127.0.0.1", "listen_port": 18101, "method": "aes-256-gcm", "password": "secret"},
		map[string]any{"type": "vmess", "tag": "vmess", "listen": "127.0.0.1", "listen_port": 18102, "users": []any{map[string]any{"name": "default", "uuid": userUUID, "alterId": 0}}},
		map[string]any{"type": "vless", "tag": "vless", "listen": "127.0.0.1", "listen_port": 18103, "users": []any{map[string]any{"name": "default", "uuid": userUUID}}},
		map[string]any{"type": "vless", "tag": "vless-reality", "listen": "127.0.0.1", "listen_port": 18104, "users": []any{map[string]any{"name": "default", "uuid": userUUID, "flow": "xtls-rprx-vision"}}, "tls": map[string]any{
			"enabled": true, "server_name": "www.microsoft.com",
			"reality": map[string]any{
				"enabled": true,
				"handshake": map[string]any{
					"server": "1.1.1.1", "server_port": 443,
				},
				"private_key": priv,
				"short_id":    []string{"0123456789abcdef"},
			},
		}},
		map[string]any{"type": "trojan", "tag": "trojan", "listen": "127.0.0.1", "listen_port": 18105, "users": []any{map[string]any{"name": "default", "password": "secret"}}, "tls": tlsBlock},
		map[string]any{"type": "hysteria2", "tag": "hy2", "listen": "127.0.0.1", "listen_port": 18106, "users": []any{map[string]any{"name": "default", "password": "secret"}}, "tls": tlsBlock},
		map[string]any{"type": "tuic", "tag": "tuic", "listen": "127.0.0.1", "listen_port": 18107, "users": []any{map[string]any{"name": "default", "uuid": userUUID, "password": "secret"}}, "tls": tlsBlock, "congestion_control": "bbr"},
		map[string]any{"type": "anytls", "tag": "anytls", "listen": "127.0.0.1", "listen_port": 18108, "users": []any{map[string]any{"name": "default", "password": "secret"}}, "tls": tlsBlock},
		map[string]any{"type": "http", "tag": "http", "listen": "127.0.0.1", "listen_port": 18109, "users": []any{map[string]any{"username": "user", "password": "secret"}}},
		map[string]any{"type": "socks", "tag": "socks", "listen": "127.0.0.1", "listen_port": 18110, "users": []any{map[string]any{"username": "user", "password": "secret"}}},
		map[string]any{"type": "mixed", "tag": "mixed", "listen": "127.0.0.1", "listen_port": 18111, "users": []any{map[string]any{"username": "user", "password": "secret"}}},
	}
	// A 1.12-shaped inbound still carries sniff fields. 1.14 accepts them and ignores override.
	inbounds[0].(map[string]any)["sniff"] = true
	inbounds[0].(map[string]any)["sniff_override_destination"] = true
	inbounds[0].(map[string]any)["sniff_timeout"] = "300ms"
	cfg := nodedns.Default()
	cfg.Strategy = nodedns.StrategyIPv4Only
	emitted, err := cfg.Emit(nodedns.EmitOptions{Failover: true, BindInterface: ""})
	if err != nil {
		t.Fatal(err)
	}
	// Chain outbound uses a hostname, so domain_resolver must be present or 1.14
	// refuses the dialer once more than one DNS server exists.
	document := map[string]any{
		"log":      map[string]any{"level": "error"},
		"dns":      emitted.DNS,
		"inbounds": inbounds,
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
			map[string]any{
				"type": "shadowsocks", "tag": "chain-next", "server": "next.example", "server_port": 443,
				"method": "aes-128-gcm", "password": "pw", "domain_resolver": emitted.ChainResolver,
			},
		},
		"route": map[string]any{
			"final": "direct",
			"rules": []any{
				map[string]any{"action": "sniff", "timeout": "300ms"},
				map[string]any{"inbound": []string{"ss"}, "action": "route", "outbound": "chain-next"},
				map[string]any{"domain_suffix": []string{"example.com"}, "action": "route", "outbound": "direct"},
				map[string]any{"action": "resolve", "strategy": "ipv4_only"},
				map[string]any{"ip_cidr": []string{"203.0.113.0/24"}, "action": "reject"},
			},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewBoxRuntime(t.TempDir())
	if err := runtime.Apply(context.Background(), string(raw), "inbounds"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Stop(context.Background()) })
	if st := runtime.Status(context.Background()); st.State != StateRunning {
		t.Fatalf("state %s: %s", st.State, st.LastError)
	}
	prepared := runtime.ConfigJSON()
	for _, field := range []string{`"sniff":`, "sniff_override_destination", "sniff_timeout"} {
		if strings.Contains(prepared, field) {
			t.Fatalf("legacy inbound field %s was not stripped", field)
		}
	}
}

func testCertPEM(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "node.example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"node.example"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}
