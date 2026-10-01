package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// DefaultUpgradeDir is where the unprivileged agent stages a new binary.
	// A root-owned systemd path unit watches this directory and applies the upgrade.
	DefaultUpgradeDir = "/var/lib/ladder-agent/upgrade"

	defaultUpgradeRepo = "LadderAirport/LadderAirport"
	githubAPIBase      = "https://api.github.com"
	githubReleaseBase  = "https://github.com"

	// maxUpgradeDownloadBytes caps the staged binary size.
	maxUpgradeDownloadBytes = 256 << 20
)

// ErrReleaseSumsNotFound marks the "release has no SHA256SUMS.txt" case so the
// caller can soft-fail on it with errors.Is instead of matching message text.
var ErrReleaseSumsNotFound = errors.New("未找到校验文件")

// UpgradeRequest is the agent-side upgrade plan.
type UpgradeRequest struct {
	Version     string // tag or "latest"/empty
	Repo        string // owner/repo
	DownloadURL string // optional direct URL
	SHA256      string // optional hex digest
	// UpgradeDir overrides DefaultUpgradeDir (tests).
	UpgradeDir string
	// HTTPClient optional.
	HTTPClient *http.Client
}

// UpgradeResult is returned after the binary is staged for the helper.
type UpgradeResult struct {
	Version     string
	StagedPath  string
	DownloadURL string
	Message     string
}

// StageAgentUpgrade downloads the target binary into the upgrade staging dir and
// writes a .ready marker so the root helper applies it. This process never
// replaces its own executable (no root / NoNewPrivileges).
func StageAgentUpgrade(ctx context.Context, req UpgradeRequest) (*UpgradeResult, error) {
	// A custom download URL bypasses release-sum verification, so it must
	// carry an explicit checksum; never install unverified binaries.
	if strings.TrimSpace(req.DownloadURL) != "" && strings.TrimSpace(req.SHA256) == "" {
		return nil, status.Error(codes.FailedPrecondition, "自定义下载地址必须同时提供 SHA256 校验值，拒绝无校验安装")
	}
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("远程升级仅支持 Linux，当前系统为 %s", runtime.GOOS)
	}
	dir := strings.TrimSpace(req.UpgradeDir)
	if dir == "" {
		dir = DefaultUpgradeDir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建升级目录失败：%w", err)
	}

	client := req.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Minute}
	}

	version := strings.TrimSpace(req.Version)
	if version == "" {
		version = "latest"
	}
	repo := strings.TrimSpace(req.Repo)
	if repo == "" {
		repo = defaultUpgradeRepo
	}

	url := strings.TrimSpace(req.DownloadURL)
	resolvedVersion := version
	if url == "" {
		u, tag, err := resolveReleaseURL(ctx, client, repo, version)
		if err != nil {
			return nil, err
		}
		url = u
		if tag != "" {
			resolvedVersion = tag
		}
	}

	asset := fmt.Sprintf("ladder-agent-linux-%s", releaseArchSuffix())
	tmpPath := filepath.Join(dir, asset+".partial")
	finalPath := filepath.Join(dir, asset)
	readyPath := finalPath + ".ready"
	metaPath := finalPath + ".json"
	sumsPath := finalPath + ".sha256"

	// Clean previous staging artifacts for this asset.
	_ = os.Remove(tmpPath)
	_ = os.Remove(readyPath)
	_ = os.Remove(sumsPath)

	if err := downloadFile(ctx, client, url, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}

	sum, err := fileSHA256(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if want := strings.TrimSpace(strings.ToLower(req.SHA256)); want != "" {
		if sum != want {
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("SHA256 校验不匹配：实际 %s，预期 %s", sum, want)
		}
	} else if resolvedVersion != "" && resolvedVersion != "latest" {
		// Best-effort: verify against SHA256SUMS.txt when present.
		if err := verifyAgainstReleaseSums(ctx, client, repo, resolvedVersion, asset, sum); err != nil {
			// Soft-fail only when sums file missing; hard-fail on mismatch.
			if !errors.Is(err, ErrReleaseSumsNotFound) {
				_ = os.Remove(tmpPath)
				return nil, err
			}
		}
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("设置待升级二进制权限失败：%w", err)
	}
	// Atomic-ish rename into place.
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("暂存升级二进制失败：%w", err)
	}

	// sha256sum -c compatible checksum file for the root upgrade helper.
	sumsLine := fmt.Sprintf("%s  %s\n", sum, asset)
	if err := os.WriteFile(sumsPath, []byte(sumsLine), 0o644); err != nil {
		return nil, fmt.Errorf("写入升级校验文件失败：%w", err)
	}

	meta := map[string]any{
		"version":      resolvedVersion,
		"download_url": url,
		"sha256":       sum,
		"staged_at":    time.Now().UTC().Format(time.RFC3339),
		"asset":        asset,
	}
	if b, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(metaPath, b, 0o644)
	}

	// Marker file triggers the systemd path unit (PathExists).
	marker := fmt.Sprintf("version=%s\nsha256=%s\nsource=%s\n", resolvedVersion, sum, url)
	if err := os.WriteFile(readyPath, []byte(marker), 0o644); err != nil {
		return nil, fmt.Errorf("写入升级就绪标记失败：%w", err)
	}

	return &UpgradeResult{
		Version:     resolvedVersion,
		StagedPath:  finalPath,
		DownloadURL: url,
		Message:     "升级文件已暂存，正在等待升级助手替换并重启",
	}, nil
}

// releaseArchSuffix is the filename suffix of official Linux release assets
// (ladder-agent-linux-<suffix>). runtime.GOARCH reports "arm" for the
// GOARM=7 hard-float build, which is published as linux-armhf.
func releaseArchSuffix() string {
	return releaseArchSuffixFor(runtime.GOARCH)
}

func releaseArchSuffixFor(goarch string) string {
	if goarch == "arm" {
		return "armhf"
	}
	return goarch
}

func resolveReleaseURL(ctx context.Context, client *http.Client, repo, version string) (url, tag string, err error) {
	asset := fmt.Sprintf("ladder-agent-linux-%s", releaseArchSuffix())
	version = strings.TrimSpace(version)
	if version == "" || version == "latest" {
		apiURL := fmt.Sprintf("%s/repos/%s/releases/latest", githubAPIBase, repo)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return "", "", err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "LadderAirport-Agent")
		resp, err := client.Do(req)
		if err != nil {
			return "", "", fmt.Errorf("获取最新 Release 失败：%w", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if err != nil {
			return "", "", err
		}
		if resp.StatusCode != http.StatusOK {
			return "", "", fmt.Errorf("获取 GitHub 最新 Release 失败：HTTP %d", resp.StatusCode)
		}
		var parsed struct {
			TagName string `json:"tag_name"`
			Assets  []struct {
				Name               string `json:"name"`
				BrowserDownloadURL string `json:"browser_download_url"`
			} `json:"assets"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return "", "", err
		}
		tag = strings.TrimSpace(parsed.TagName)
		for _, a := range parsed.Assets {
			if a.Name == asset && a.BrowserDownloadURL != "" {
				return a.BrowserDownloadURL, tag, nil
			}
		}
		if tag == "" {
			return "", "", fmt.Errorf("最新 Release 缺少 tag_name")
		}
		return fmt.Sprintf("%s/%s/releases/download/%s/%s", githubReleaseBase, repo, tag, asset), tag, nil
	}

	tag = version
	return fmt.Sprintf("%s/%s/releases/download/%s/%s", githubReleaseBase, repo, tag, asset), tag, nil
}

func downloadFile(ctx context.Context, client *http.Client, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "LadderAirport-Agent")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载 %s 失败：%w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 失败：HTTP %d", url, resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	written, err := io.Copy(f, io.LimitReader(resp.Body, maxUpgradeDownloadBytes))
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("写入下载文件失败：%w", err)
	}
	if written == maxUpgradeDownloadBytes {
		_ = f.Close()
		return fmt.Errorf("下载文件达到 %d 字节上限，疑似被截断", maxUpgradeDownloadBytes)
	}
	return f.Close()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyAgainstReleaseSums(ctx context.Context, client *http.Client, repo, tag, asset, got string) error {
	sumsURL := fmt.Sprintf("%s/%s/releases/download/%s/SHA256SUMS.txt", githubReleaseBase, repo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "LadderAirport-Agent")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w：%v", ErrReleaseSumsNotFound, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrReleaseSumsNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("获取校验文件失败：HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// formats: "<hex>  <name>" or "<hex> *<name>"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name == asset {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("校验文件中没有 %s 的记录", asset)
	}
	if want != got {
		return fmt.Errorf("SHA256 与 Release 校验值不匹配：实际 %s，预期 %s", got, want)
	}
	return nil
}
