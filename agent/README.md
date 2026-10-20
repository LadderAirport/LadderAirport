# LadderAirport Agent

In-process **sing-box + optional FRPS** agent: gRPC `AgentControl` with independent `BoxRuntime` and `FRPServerRuntime` lifecycle adapters.

## Upstream pin

| Item | Value |
|------|--------|
| Upstream | [SagerNet/sing-box](https://github.com/SagerNet/sing-box) |
| Pinned tag | **v1.14.3** |
| Submodule path | `agent/sing-box` |
| Go module | `github.com/sagernet/sing-box` |

FRPS follows the same source pinning model:

| Item | Value |
|---|---|
| Upstream | [fatedier/frp](https://github.com/fatedier/frp) |
| Pinned tag | `v0.69.0` |
| Submodule path | `agent/frp` |
| Go module | `github.com/fatedier/frp` |

`agent/go.mod` uses:

```go
require github.com/sagernet/sing-box v1.14.3
replace github.com/sagernet/sing-box => ./sing-box
```

**Prefer submodule + `replace`.** A plain `go get` module dependency is acceptable only when the submodule clone is too heavy for the environment; the submodule pointer is the source of truth for 二开.

### Submodule checkout

```bash
git submodule update --init --recursive
cd agent/sing-box && git checkout v1.14.3
```

If `git submodule add` fails (network), shallow clone then wire replace:

```bash
git clone --depth 1 --branch v1.14.3 https://github.com/SagerNet/sing-box.git agent/sing-box
```

## Runtime

Agent always uses **in-process sing-box** (`control.BoxRuntime`). FRPS is also linked into the Agent and remains stopped until Panel applies an enabled FRPS configuration.

The runtimes are independent: applying or stopping FRPS does not restart sing-box. The latest FRPS configuration is stored as `frps/frps-current.json` under the Agent data directory with mode `0600`, restored at Agent startup, and controlled through the `frps-v1` gRPC capability.

Log line on start: `运行模式=内置代理实例 Agent版本=... sing-box版本=... frps版本=... 数据目录=...`.

## Build

From repo root (recommended — applies default tags + version ldflags):

```bash
make agent   # → bin/ladder-agent
```

Or:

```bash
cd agent
go build -tags "with_quic,with_utls" \
  -ldflags "-checklinkname=0 -X 'github.com/sagernet/sing-box/constant.Version=1.14.3'" \
  -o ../bin/ladder-agent ./cmd/ladder-agent
```

`-checklinkname=0` is required for `GOOS=android`. `github.com/wlynxg/anet` linknames `net.zoneCache`, and the Go linker rejects that without the flag. Linux builds link either way. sing-box's own libbox build passes the same flag, plus the `badlinkname` and `tfogo_checklinkname0` tags.

Optional full feature tags (match upstream Makefile extras):

```bash
cd agent
go build -tags "with_gvisor,with_quic,with_wireguard,with_utls,with_clash_api" \
  -ldflags "-checklinkname=0 -X 'github.com/sagernet/sing-box/constant.Version=1.14.3'" \
  -o ../bin/ladder-agent ./cmd/ladder-agent
```

### Protocol build notes

| Protocols | Tags / pin |
|-----------|------------|
| Shadowsocks, Trojan, VLESS, VMess | default registries |
| TUIC, Hysteria2 | **`with_quic`** (default in `make agent` / CI) |
| AnyTLS | sing-box **≥ 1.12** (pin v1.14.3) |
| Reality client fingerprint paths | **`with_utls`** (default) |

## Tests

```bash
make test
# or
cd agent && go test -tags "with_quic,with_utls" ./... -timeout 120s
# faster parse-only smoke (skips real box Start):
cd agent && go test -tags "with_quic,with_utls" ./internal/control/ -short
```

## BoxRuntime behaviour

**Single-instance lifecycle** (never two sing-box cores listening at once):

`Apply` algorithm (stop-then-start; brief disconnect OK):

1. If already **running** with the **same** `config_json` + `config_hash` → **no-op**.
2. Parse JSON into `option.Options` (fail here keeps the old instance if any).
3. **Stop/close** the current instance completely (ports released).
4. Create and `Start` the new box.
5. On start failure: best-effort **restore** previous config; set `LastError`.
6. On success: update hash / `startedAt`, write `dataDir/current.json` when set.

All Apply/Start/Stop share one mutex — concurrent Panel RPCs queue, never dual-start.

`Start` is a no-op if already running; otherwise re-applies the last good config. `Stop` closes the instance (config kept for later Start).

### Metrics

| Field | Source |
|-------|--------|
| `Connections` | Active routed connections via in-process `ConnectionTracker` |
| `UplinkBytes` / `DownlinkBytes` | Cumulative bytes (client→node / node→client); survives hot-reload |
| `MemoryRSSBytes` | Linux: `/proc/self/statm` RSS; other platforms fall back to `runtime.MemStats.Sys` |
| `CPUPercent` | Linux: `/proc/self/stat` sample between polls; first sample is 0 |

## Upgrade policy

1. Bump submodule to a new **stable** tag (`v1.14.x`, currently v1.14.3).
2. Update the `agent/go.mod` require version + comment and the `SingboxVersion` fallback in `runtime_box.go`; Makefile and CI derive `SINGBOX_VERSION` from the submodule tag automatically.
3. Fix glue under `agent/internal` only if APIs break.
4. Run `make test` and a short Apply/Start smoke.

Do not edit upstream files under `agent/sing-box/` unless deliberately forking; keep 二开 glue in `agent/internal` and `agent/cmd`.
