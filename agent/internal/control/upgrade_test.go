package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStageAgentUpgradeFromDirectURL(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("upgrade staging is linux-only")
	}
	payload := []byte("#!/bin/sh\necho fake-agent\n")
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/bin") {
			_, _ = w.Write(payload)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	res, err := StageAgentUpgrade(context.Background(), UpgradeRequest{
		Version:     "v9.9.9",
		DownloadURL: srv.URL + "/bin",
		SHA256:      hexSum,
		UpgradeDir:  dir,
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("StageAgentUpgrade: %v", err)
	}
	if res.Version != "v9.9.9" {
		t.Fatalf("version = %q", res.Version)
	}
	if _, err := os.Stat(res.StagedPath); err != nil {
		t.Fatalf("staged missing: %v", err)
	}
	ready := res.StagedPath + ".ready"
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("ready marker missing: %v", err)
	}
	got, err := os.ReadFile(res.StagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch")
	}
}

func TestStageAgentUpgradeSHAMismatch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("upgrade staging is linux-only")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data"))
	}))
	t.Cleanup(srv.Close)

	_, err := StageAgentUpgrade(context.Background(), UpgradeRequest{
		DownloadURL: srv.URL,
		SHA256:      strings.Repeat("0", 64),
		UpgradeDir:  t.TempDir(),
		HTTPClient:  srv.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "SHA256 校验不匹配") {
		t.Fatalf("expected sha mismatch, got %v", err)
	}
}

// A custom download URL without an explicit checksum must be rejected before
// any download happens (no unverified installs).
func TestStageAgentUpgradeRejectsUnverifiedCustomURL(t *testing.T) {
	_, err := StageAgentUpgrade(context.Background(), UpgradeRequest{
		DownloadURL: "https://example.com/ladder-agent",
		UpgradeDir:  t.TempDir(),
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestStageAgentUpgradeWritesChecksumFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("upgrade staging is linux-only")
	}
	payload := []byte("#!/bin/sh\necho fake-agent\n")
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	res, err := StageAgentUpgrade(context.Background(), UpgradeRequest{
		Version:     "v9.9.9",
		DownloadURL: srv.URL,
		SHA256:      hexSum,
		UpgradeDir:  t.TempDir(),
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("StageAgentUpgrade: %v", err)
	}
	raw, err := os.ReadFile(res.StagedPath + ".sha256")
	if err != nil {
		t.Fatalf("checksum file missing: %v", err)
	}
	// sha256sum -c compatible: "<hex>  <binary name>".
	want := fmt.Sprintf("%s  %s\n", hexSum, filepath.Base(res.StagedPath))
	if string(raw) != want {
		t.Fatalf("checksum file = %q, want %q", raw, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The "sums file missing" case must surface via the ErrReleaseSumsNotFound
// sentinel, and only that case may match it.
func TestReleaseArchSuffix(t *testing.T) {
	if got := releaseArchSuffixFor("arm"); got != "armhf" {
		t.Fatalf("arm = %q, want armhf", got)
	}
	if got := releaseArchSuffixFor("amd64"); got != "amd64" {
		t.Fatalf("amd64 = %q", got)
	}
	if got := releaseArchSuffixFor("arm64"); got != "arm64" {
		t.Fatalf("arm64 = %q", got)
	}
	if got := releaseArchSuffix(); got != releaseArchSuffixFor(runtime.GOARCH) {
		t.Fatalf("releaseArchSuffix() = %q, want %q", got, releaseArchSuffixFor(runtime.GOARCH))
	}
}

func TestVerifyAgainstReleaseSumsSentinel(t *testing.T) {
	notFound := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       http.NoBody,
			Header:     make(http.Header),
		}, nil
	})}
	err := verifyAgainstReleaseSums(context.Background(), notFound, "o/r", "v1.0.0", "asset", "deadbeef")
	if !errors.Is(err, ErrReleaseSumsNotFound) {
		t.Fatalf("404 should match ErrReleaseSumsNotFound, got %v", err)
	}

	mismatch := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("f", 64) + "  other-asset\n")),
			Header:     make(http.Header),
		}, nil
	})}
	err = verifyAgainstReleaseSums(context.Background(), mismatch, "o/r", "v1.0.0", "asset", "deadbeef")
	if err == nil || errors.Is(err, ErrReleaseSumsNotFound) {
		t.Fatalf("present-but-mismatched sums must not match sentinel, got %v", err)
	}
}
