.PHONY: proto panel panel-bin agent web test e2e install-panel install-agent

# Default agent tags: QUIC (TUIC/Hy2) + uTLS (Reality/AnyTLS client fingerprints).
AGENT_TAGS ?= with_quic,with_utls

# Core version injected into panel/agent binaries (override for releases).
# git describe --tags --always yields vX.Y.Z on a tag and vX.Y.Z-N-gSHA when ahead.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# sing-box 版本唯一来源：agent/sing-box 子模块的 git tag（如 v1.14.3 → 1.14.3）。
# CI（.github/workflows/ci.yml、release.yml）用同一命令派生；三处勿再硬编码。
SINGBOX_VERSION ?= $(shell tag=$$(git -C agent/sing-box describe --tags 2>/dev/null) && echo "$${tag}" | sed 's/^v//' || echo unknown)

VERSION_LDFLAGS = \
	-X 'github.com/ladderairport/agent/internal/version.Version=$(VERSION)' \
	-X 'github.com/ladderairport/agent/internal/version.Commit=$(GIT_COMMIT)' \
	-X 'github.com/ladderairport/agent/internal/version.BuiltAt=$(BUILD_TIME)' \
	-X 'github.com/ladderairport/agent/internal/version.ClientType=linux' \
	-X 'github.com/ladderairport/panel/internal/version.Version=$(VERSION)' \
	-X 'github.com/ladderairport/panel/internal/version.Commit=$(GIT_COMMIT)' \
	-X 'github.com/ladderairport/panel/internal/version.BuiltAt=$(BUILD_TIME)'

# -checklinkname=0 lets Android link github.com/wlynxg/anet, which uses go:linkname
# into net.zoneCache. sing-box 1.14 also requires it together with the badlinkname tag.
AGENT_LDFLAGS ?= -checklinkname=0 -X 'github.com/sagernet/sing-box/constant.Version=$(SINGBOX_VERSION)' $(VERSION_LDFLAGS)
PANEL_LDFLAGS ?= -s -w $(VERSION_LDFLAGS)

proto:
	protoc -I proto \
	  --go_out=proto/gen/go --go_opt=paths=source_relative \
	  --go-grpc_out=proto/gen/go --go-grpc_opt=paths=source_relative \
	  proto/agent/v1/agent.proto

web:
	cd web && npm ci && npm run build
	rm -rf panel/web/dist
	mkdir -p panel/web/dist
	cp -r web/dist/. panel/web/dist/
	# keep dist non-empty for go:embed (placeholder if build produced nothing)
	@test -f panel/web/dist/index.html || echo '<!doctype html><title>LadderAirport</title>' > panel/web/dist/index.html

panel: web
	cd panel && go build -trimpath -ldflags "$(PANEL_LDFLAGS)" -o ../bin/panel ./cmd/panel

# Build panel with committed embed dist only (no npm). Useful for offline / CI-like installs.
panel-bin:
	cd panel && go build -trimpath -ldflags="$(PANEL_LDFLAGS)" -o ../bin/panel ./cmd/panel

agent:
	cd agent && go build -trimpath -tags "$(AGENT_TAGS)" -ldflags "$(AGENT_LDFLAGS)" -o ../bin/ladder-agent ./cmd/ladder-agent

# -race 需要 cgo：显式 CGO_ENABLED=1，避免外部环境 CGO_ENABLED=0 导致失败
test:
	cd pkg && go vet ./... && CGO_ENABLED=1 go test -race ./...
	cd panel && go vet ./... && CGO_ENABLED=1 go test -race ./...
	cd agent && go vet -tags "$(AGENT_TAGS)" ./... && CGO_ENABLED=1 go test -race -tags "$(AGENT_TAGS)" ./... -timeout 120s

# 端到端冒烟：先确保产物存在（panel 用已提交的 embed dist，无需 npm），再跑 e2e 脚本
e2e: panel-bin agent
	bash scripts/e2e-smoke.sh

# Local systemd install helpers (require root). Prefer curl|bash from Release on servers.
install-panel:
	sudo LADDER_FROM=local ./scripts/install-panel.sh

install-agent:
	@test -n "$(LADDER_PANEL)" -a -n "$(LADDER_NODE_ID)" -a -n "$(LADDER_ENROLL_TOKEN)" || \
		(echo "LADDER_PANEL, LADDER_NODE_ID and LADDER_ENROLL_TOKEN are required"; exit 1)
	@sudo env LADDER_FROM=local LADDER_PANEL="$(LADDER_PANEL)" LADDER_NODE_ID="$(LADDER_NODE_ID)" \
		LADDER_ENROLL_TOKEN="$(LADDER_ENROLL_TOKEN)" ./scripts/install-agent.sh
