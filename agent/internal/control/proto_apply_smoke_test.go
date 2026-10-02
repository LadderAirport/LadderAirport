package control_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/ladderairport/agent/internal/control"
)

func selfSignedPEMs(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "smoke"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	kb, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
	return certPEM, keyPEM
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestApplyPanelProtocols verifies generated protocol shapes can Start on a
// default-tagged Agent (with_quic,with_utls) against sing-box ≥1.12.
func TestApplyPanelProtocols(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	cert, key := selfSignedPEMs(t)
	rt := control.NewBoxRuntime(t.TempDir())
	defer rt.Stop(context.Background())

	cases := []struct {
		name string
		cfg  map[string]any
	}{
		{
			name: "vmess",
			cfg: map[string]any{
				"log": map[string]any{"level": "error", "disabled": true},
				"inbounds": []map[string]any{{
					"type": "vmess", "tag": "in-vm", "listen": "127.0.0.1", "listen_port": 28001,
					"users": []map[string]any{{
						"name": "default", "uuid": "bf000d23-0752-40b4-affe-68f7707a9661", "alterId": 0,
					}},
				}},
				"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
			},
		},
		{
			name: "tuic",
			cfg: map[string]any{
				"log": map[string]any{"level": "error", "disabled": true},
				"inbounds": []map[string]any{{
					"type": "tuic", "tag": "in-tuic", "listen": "127.0.0.1", "listen_port": 28002,
					"users": []map[string]any{{
						"name": "default", "uuid": "2dd61d93-75d8-4da4-ac0e-6aece7eac365", "password": "p",
					}},
					"congestion_control": "cubic",
					"tls": map[string]any{
						"enabled": true, "certificate": []string{cert}, "key": []string{key},
					},
				}},
				"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
			},
		},
		{
			name: "anytls",
			cfg: map[string]any{
				"log": map[string]any{"level": "error", "disabled": true},
				"inbounds": []map[string]any{{
					"type": "anytls", "tag": "in-any", "listen": "127.0.0.1", "listen_port": 28003,
					"users": []map[string]any{{"name": "default", "password": "p"}},
					"tls": map[string]any{
						"enabled": true, "certificate": []string{cert}, "key": []string{key},
					},
				}},
				"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
			},
		},
		{
			name: "shadowsocks-2022-aes128",
			cfg: map[string]any{
				"log": map[string]any{"level": "error", "disabled": true},
				"inbounds": []map[string]any{{
					"type": "shadowsocks", "tag": "in-ss2022-128", "listen": "127.0.0.1", "listen_port": 28004,
					"network": "tcp", "method": "2022-blake3-aes-128-gcm",
					"password": "AAECAwQFBgcICQoLDA0ODw==",
				}},
				"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
			},
		},
		{
			name: "shadowsocks-2022-aes256",
			cfg: map[string]any{
				"log": map[string]any{"level": "error", "disabled": true},
				"inbounds": []map[string]any{{
					"type": "shadowsocks", "tag": "in-ss2022-256", "listen": "127.0.0.1", "listen_port": 28005,
					"network": "tcp", "method": "2022-blake3-aes-256-gcm",
					"password": "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
				}},
				"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
			},
		},
		{
			name: "shadowsocks-2022-chacha20",
			cfg: map[string]any{
				"log": map[string]any{"level": "error", "disabled": true},
				"inbounds": []map[string]any{{
					"type": "shadowsocks", "tag": "in-ss2022-chacha", "listen": "127.0.0.1", "listen_port": 28006,
					"network": "tcp", "method": "2022-blake3-chacha20-poly1305",
					"password": "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
				}},
				"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
			},
		},
		{
			name: "mixed-auto",
			cfg: map[string]any{
				"log": map[string]any{"level": "error", "disabled": true},
				"inbounds": []map[string]any{{
					"type": "mixed", "tag": "in-auto", "listen": "127.0.0.1", "listen_port": 28007,
					"users": []map[string]any{{"username": "carol", "password": "p"}},
				}},
				"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgJSON := mustJSON(t, tc.cfg)
			if err := rt.Apply(context.Background(), cfgJSON, tc.name); err != nil {
				t.Fatalf("apply %s: %v", tc.name, err)
			}
			st := rt.Status(context.Background())
			if st.State != control.StateRunning {
				t.Fatalf("%s state=%s last=%s", tc.name, st.State, st.LastError)
			}
		})
	}
}
