#!/usr/bin/env bash
# 一键安装 / 升级 / 卸载 ladder-agent（systemd）。
# 默认从 GitHub Release 拉最新二进制。
#
# 安装（从 Panel 节点页面复制命令；以下变量均必需）:
#   sudo env LADDER_PANEL=https://panel.example.com LADDER_NODE_ID=... \
#     LADDER_ENROLL_TOKEN=... ./scripts/install-agent.sh
#
# 升级（只换二进制 + 刷新 unit + restart；保留 agent.env 与 TLS）:
#   curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-agent.sh \
#     | sudo env LADDER_ACTION=upgrade LADDER_VERSION=v0.3.1 bash
#
# 卸载（默认保留 conf/data；LADDER_PURGE=1 全清）:
#   curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-agent.sh \
#     | sudo env LADDER_ACTION=uninstall bash
#
# 本地构建安装:
#   sudo LADDER_FROM=local LADDER_PANEL=https://panel.example.com \
#     LADDER_NODE_ID=... LADDER_ENROLL_TOKEN=... ./scripts/install-agent.sh
#   sudo ./scripts/install-agent.sh upgrade
#   sudo ./scripts/install-agent.sh uninstall
#
set -euo pipefail

REPO="${LADDER_REPO:-LadderAirport/LadderAirport}"
API_BASE="${LADDER_GITHUB_API:-https://api.github.com}"
RELEASES_BASE="${LADDER_GITHUB_RELEASES:-https://github.com/${REPO}/releases}"

# 若从 curl|bash 运行，$0 不是仓库路径
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
ROOT=""
if [[ -n "${SCRIPT_DIR}" && -f "${SCRIPT_DIR}/../Makefile" ]]; then
  ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
fi

# --- action: LADDER_ACTION 或首个参数 install|upgrade|uninstall ---
# curl|bash 时 $1 常为空，请用 LADDER_ACTION=...
ACTION="${LADDER_ACTION:-}"
BIN_SRC=""
case "${1:-}" in
  install | upgrade | uninstall)
    ACTION="$1"
    shift || true
    BIN_SRC="${1:-}"
    ;;
  *)
    BIN_SRC="${1:-}"
    ;;
esac
ACTION="${ACTION:-install}"
PURGE="${LADDER_PURGE:-0}"

INSTALL_BIN="${INSTALL_BIN:-/usr/local/bin/ladder-agent}"
CONF_DIR="${CONF_DIR:-/etc/ladder-agent}"
DATA_DIR="${DATA_DIR:-/var/lib/ladder-agent}"
TLS_DIR="${TLS_DIR:-${CONF_DIR}/tls}"
SERVICE_DST="${SERVICE_DST:-/etc/systemd/system/ladder-agent.service}"
SERVICE_NAME="ladder-agent.service"
USER_NAME="${LADDER_USER:-ladder}"
GROUP_NAME="${LADDER_GROUP:-ladder}"
LISTEN="${LADDER_LISTEN:-0.0.0.0:50051}"
TOKEN="${LADDER_TOKEN:-}"
ENROLL_TOKEN="${LADDER_ENROLL_TOKEN:-}"
# release | local  （默认 release）
FROM="${LADDER_FROM:-release}"
VERSION="${LADDER_VERSION:-latest}" # latest 或 v0.2.0
TLS_EXTRA_SANS="${LADDER_TLS_EXTRA_SANS:-}" # 逗号分隔: DNS:foo,IP:1.2.3.4
# Panel PKI enrollment (set by Panel-generated install command)
PANEL_URL="${LADDER_PANEL:-}"          # e.g. https://panel.example.com
NODE_ID="${LADDER_NODE_ID:-}"
REPORT_ADDR="${LADDER_REPORT_ADDRESS:-}" # force reported address; else auto-detect
GRPC_PORT_HINT="${LADDER_GRPC_PORT:-}"
ALLOW_HTTP="${LADDER_ALLOW_HTTP:-0}"
# Distinguish "operator passed the variable" from the default, so an upgrade
# can read LADDER_UPLINK out of an existing agent.env.
if [[ -n "${LADDER_UPLINK+x}" ]]; then
  UPLINK_EXPLICIT=1
else
  UPLINK_EXPLICIT=0
fi
if [[ -n "${LADDER_UPLINK_SERVE_GRPC+x}" ]]; then
  UPLINK_SERVE_GRPC_EXPLICIT=1
else
  UPLINK_SERVE_GRPC_EXPLICIT=0
fi
UPLINK="${LADDER_UPLINK:-0}"
UPLINK_SERVE_GRPC="${LADDER_UPLINK_SERVE_GRPC:-0}"
# 公网 IP 探测端点（用于证书 SAN 与上报地址兜底）。默认 api.ipify.org；
# 显式设为空（LADDER_IP_ECHO_URL=）可关闭公网探测（例如离线/内网环境）。
IP_ECHO_URL="${LADDER_IP_ECHO_URL-https://api.ipify.org}"
TMPDIR_DL=""
ENV_FILE="${CONF_DIR}/agent.env"

cleanup() {
  if [[ -n "${TMPDIR_DL}" && -d "${TMPDIR_DL}" ]]; then
    rm -rf "${TMPDIR_DL}"
  fi
}
trap cleanup EXIT

die() { echo "错误：$*" >&2; exit 1; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "需要命令: $1"
}

if [[ "$(id -u)" -ne 0 ]]; then
  die "请使用 root 运行: sudo $0 $*"
fi

need_cmd systemctl
need_cmd install
need_cmd uname

case "${ACTION}" in
  install | upgrade | uninstall) ;;
  *) die "未知动作: ${ACTION}（支持 install|upgrade|uninstall，或 LADDER_ACTION=...）" ;;
esac

# --- arch ---
detect_arch() {
  local m
  m="$(uname -m)"
  case "${m}" in
    x86_64 | amd64) echo "amd64" ;;
    aarch64 | arm64) echo "arm64" ;;
    armv7l | armhf) echo "armhf" ;;
    *) die "不支持的架构: ${m}（需要 amd64、arm64 或 armhf）" ;;
  esac
}

# --- download from GitHub Release ---
# Progress/status MUST go to stderr; only the binary path is printed on stdout
# (callers capture via SRC="$(resolve_binary)").
download_release_binary() {
  need_cmd curl
  local arch asset tag url
  arch="$(detect_arch)"
  asset="ladder-agent-linux-${arch}"

  echo "==> 从 GitHub Release 获取二进制 (${REPO}, ${VERSION}, ${asset})" >&2

  if [[ "${VERSION}" == "latest" ]]; then
    local api_json
    api_json="$(curl -fsSL "${API_BASE}/repos/${REPO}/releases/latest")" || die "无法访问 releases/latest（仓库私有或无 Release？）"
    tag="$(echo "${api_json}" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
    url="$(echo "${api_json}" | tr ',' '\n' | sed -n "s/.*\"browser_download_url\"[[:space:]]*:[[:space:]]*\"\\([^\"]*${asset}\\)\"/\\1/p" | head -1)"
    if [[ -z "${url}" && -n "${tag}" ]]; then
      url="${RELEASES_BASE}/download/${tag}/${asset}"
    fi
  else
    tag="${VERSION}"
    url="${RELEASES_BASE}/download/${tag}/${asset}"
  fi

  [[ -n "${url}" ]] || die "未找到资源 ${asset}。请确认已发布 Release: ${RELEASES_BASE}"

  echo "    tag: ${tag:-?}" >&2
  echo "    url: ${url}" >&2

  TMPDIR_DL="$(mktemp -d /tmp/ladder-agent-dl.XXXXXX)"
  local dest="${TMPDIR_DL}/${asset}"
  curl -fL --retry 3 --retry-delay 1 -o "${dest}" "${url}" || die "下载失败: ${url}"
  [[ -f "${dest}" ]] || die "下载后文件不存在: ${dest}"

  local sums_url
  if [[ -n "${tag}" ]]; then
    sums_url="${RELEASES_BASE}/download/${tag}/SHA256SUMS.txt"
    if curl -fsSL -o "${TMPDIR_DL}/SHA256SUMS.txt" "${sums_url}" 2>/dev/null; then
      echo "==> 校验 SHA256" >&2
      if command -v sha256sum >/dev/null 2>&1; then
        (cd "${TMPDIR_DL}" && grep " ${asset}\$" SHA256SUMS.txt | sha256sum -c -) >&2 \
          || die "SHA256 校验失败"
      else
        echo "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!" >&2
        echo "WARNING: 未找到 sha256sum，无法校验 ${asset} 的完整性！" >&2
        echo "         二进制将被跳过校验直接安装，存在被篡改风险。" >&2
        echo "         请安装 coreutils 后重试。" >&2
        echo "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!" >&2
      fi
    else
      echo "    (无 SHA256SUMS.txt，跳过校验)" >&2
    fi
  fi

  chmod +x "${dest}"
  if ! head -c 4 "${dest}" | grep -q $'\x7fELF'; then
    if file "${dest}" 2>/dev/null | grep -qi 'ELF'; then
      :
    else
      echo "WARNING: 下载文件可能不是 Linux ELF 可执行文件" >&2
    fi
  fi
  # stdout: path only
  printf '%s\n' "${dest}"
}

resolve_binary() {
  if [[ -n "${BIN_SRC}" ]]; then
    [[ -f "${BIN_SRC}" ]] || die "文件不存在: ${BIN_SRC}"
    echo "==> 使用本地文件: ${BIN_SRC}" >&2
    printf '%s\n' "${BIN_SRC}"
    return
  fi

  if [[ "${FROM}" == "local" ]]; then
    if [[ -n "${ROOT}" && -x "${ROOT}/bin/ladder-agent" ]]; then
      echo "==> 使用仓库 bin/ladder-agent" >&2
      printf '%s\n' "${ROOT}/bin/ladder-agent"
      return
    fi
    [[ -n "${ROOT}" ]] || die "LADDER_FROM=local 需要在仓库内执行脚本"
    echo "==> 本地编译 agent" >&2
    if [[ ! -f "${ROOT}/agent/sing-box/go.mod" ]]; then
      git -C "${ROOT}" submodule update --init --recursive
    fi
    need_cmd go
    (cd "${ROOT}" && make agent) >&2
    printf '%s\n' "${ROOT}/bin/ladder-agent"
    return
  fi

  download_release_binary
}

# Collect SANs for the agent server cert (DNS + IPs Panel may dial).
build_san_list() {
  local -a sans=()
  local h ip primary

  sans+=("DNS:localhost")
  sans+=("IP:127.0.0.1")
  sans+=("IP:::1")

  h="$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)"
  if [[ -n "${h}" && "${h}" != "localhost" ]]; then
    sans+=("DNS:${h}")
  fi
  h="$(hostname -s 2>/dev/null || true)"
  if [[ -n "${h}" && "${h}" != "localhost" ]]; then
    sans+=("DNS:${h}")
  fi

  # Non-loopback IPv4s
  if command -v hostname >/dev/null 2>&1; then
    for ip in $(hostname -I 2>/dev/null || true); do
      case "${ip}" in
        127.*|::1) continue ;;
        *:*) sans+=("IP:${ip}") ;; # v6
        *) sans+=("IP:${ip}") ;;
      esac
    done
  fi

  # Optional public IP (best-effort; skip if offline or LADDER_IP_ECHO_URL= 为空)
  if [[ -n "${IP_ECHO_URL}" ]] && command -v curl >/dev/null 2>&1; then
    primary="$(curl -fsS --max-time 3 "${IP_ECHO_URL}" 2>/dev/null || true)"
    if [[ "${primary}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      sans+=("IP:${primary}")
    fi
  fi

  if [[ -n "${REPORT_ADDR}" ]]; then
    local report_san="${REPORT_ADDR%%%*}"
    if [[ "${report_san}" == *:* || "${report_san}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      sans+=("IP:${report_san}")
    else
      sans+=("DNS:${report_san}")
    fi
  fi

  if [[ -n "${TLS_EXTRA_SANS}" ]]; then
    local IFS=','
    local extra
    for extra in ${TLS_EXTRA_SANS}; do
      extra="$(echo "${extra}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
      [[ -n "${extra}" ]] || continue
      case "${extra}" in
        DNS:*|IP:*) sans+=("${extra}") ;;
        *:* ) sans+=("IP:${extra}") ;; # likely v6
        *.*)
          if [[ "${extra}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            sans+=("IP:${extra}")
          else
            sans+=("DNS:${extra}")
          fi
          ;;
        *) sans+=("DNS:${extra}") ;;
      esac
    done
  fi

  # Dedup preserving order
  local -a out=()
  local s seen
  for s in "${sans[@]}"; do
    seen=0
    for x in "${out[@]+"${out[@]}"}"; do
      if [[ "${x}" == "${s}" ]]; then seen=1; break; fi
    done
    if [[ "${seen}" -eq 0 ]]; then
      out+=("${s}")
    fi
  done

  local joined=""
  for s in "${out[@]}"; do
    if [[ -z "${joined}" ]]; then
      joined="${s}"
    else
      joined="${joined},${s}"
    fi
  done
  echo "${joined}"
}

is_truthy() {
  local value
  value="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  case "${value}" in
    1|true|yes|on) return 0 ;;
    *) return 1 ;;
  esac
}

# HTTP report + WebSocket uplink does not serve mTLS, so it skips management TLS
# unless the operator explicitly keeps the gRPC listener.
skip_management_tls() {
  is_truthy "${UPLINK}" && ! is_truthy "${UPLINK_SERVE_GRPC}"
}

load_saved_uplink_mode() {
  [[ -f "${ENV_FILE}" ]] || return 0
  local saved
  if [[ "${UPLINK_EXPLICIT}" -eq 0 ]]; then
    saved="$(grep -E '^LADDER_UPLINK=' "${ENV_FILE}" | head -1 | cut -d= -f2- || true)"
    [[ -n "${saved}" ]] && UPLINK="${saved}"
  fi
  if [[ "${UPLINK_SERVE_GRPC_EXPLICIT}" -eq 0 ]]; then
    saved="$(grep -E '^LADDER_UPLINK_SERVE_GRPC=' "${ENV_FILE}" | head -1 | cut -d= -f2- || true)"
    [[ -n "${saved}" ]] && UPLINK_SERVE_GRPC="${saved}"
  fi
}

# Exchange the one-time enrollment token for the long-lived control token.
# Uplink nodes call this instead of the certificate endpoint.
exchange_control_token() {
  need_cmd curl
  need_cmd python3
  local _saved_umask
  _saved_umask="$(umask)"
  umask 077
  mkdir -p "${CONF_DIR}"
  chmod 700 "${CONF_DIR}"
  local response_file="${CONF_DIR}/enroll-response.tmp"
  local payload_file="${CONF_DIR}/enroll-request.tmp"
  local token_file="${CONF_DIR}/control-token.tmp"

  echo "==> uplink 节点跳过管理面 TLS，只交换控制令牌"
  echo "    Panel=${PANEL_URL%/} Node=${NODE_ID}"
  PANEL_NODE_ID="${NODE_ID}" python3 - <<'PY' >"${payload_file}"
import json, os
print(json.dumps({"node_id": os.environ["PANEL_NODE_ID"]}))
PY

  local code
  code="$(curl -sS --max-time 30 --retry 3 -o "${response_file}" -w '%{http_code}' \
    -X POST "${PANEL_URL%/}/api/v1/agent/enroll" \
    -H "Authorization: Bearer ${ENROLL_TOKEN:-${ACTIVE_TOKEN:-${TOKEN}}}" \
    -H "Content-Type: application/json" \
    --data-binary "@${payload_file}")" || die "Panel 注册请求失败"
  if [[ "${code}" != "200" ]]; then
    die "Panel 注册失败 (HTTP ${code}): $(head -c 500 "${response_file}")"
  fi
  PANEL_RESPONSE="${response_file}" PANEL_TOKEN_FILE="${token_file}" python3 - <<'PY'
import json, os
with open(os.environ["PANEL_RESPONSE"], "r", encoding="utf-8") as f:
    data = json.load(f)
token = data.get("control_token", "")
if not token:
    raise SystemExit("Panel 响应缺少 control_token")
with open(os.environ["PANEL_TOKEN_FILE"], "w", encoding="utf-8") as f:
    f.write(token)
PY
  TOKEN="$(cat "${token_file}")"
  ACTIVE_TOKEN="${TOKEN}"
  rm -f "${response_file}" "${payload_file}" "${token_file}"
  umask "${_saved_umask}"
  [[ -n "${TOKEN}" ]] || die "Panel 未返回 Agent 控制令牌"
}

# Request a Panel-issued Agent certificate. The private key is generated on the
# Agent and never sent to Panel.
ensure_tls_material() {
  need_cmd openssl
  need_cmd curl
  need_cmd python3
  # 私钥/令牌/签发响应等敏感落盘文件全程 600：进入函数即收紧 umask，结束恢复
  local _saved_umask
  _saved_umask="$(umask)"
  umask 077
  mkdir -p "${TLS_DIR}"
  chmod 700 "${TLS_DIR}"
  local srv_key="${TLS_DIR}/server.key"
  local srv_crt="${TLS_DIR}/server.crt"
  local ca_crt="${TLS_DIR}/ca.crt"
  local srv_csr="${TLS_DIR}/server.csr"
  local req_conf="${TLS_DIR}/server-csr.conf"
  local response_file="${TLS_DIR}/issue-response.tmp"
  local payload_file="${TLS_DIR}/issue-request.tmp"
  local token_file="${TLS_DIR}/control-token.tmp"
  local san
  san="$(build_san_list)"

  echo "==> 由 Panel 管理 CA 签发 Agent 证书"
  echo "    Panel=${PANEL_URL%/} Node=${NODE_ID} SAN=${san}"
  if [[ ! -f "${srv_key}" ]]; then
    openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "${srv_key}"
  fi
  {
    echo "[req]"
    echo "prompt=no"
    echo "distinguished_name=dn"
    echo "req_extensions=req_ext"
    echo "[dn]"
    echo "CN=${NODE_ID}"
    echo "[req_ext]"
    echo "subjectAltName=${san}"
  } >"${req_conf}"
  openssl req -new -key "${srv_key}" -config "${req_conf}" -out "${srv_csr}"

  PANEL_NODE_ID="${NODE_ID}" \
    PANEL_ADDRESS="$(detect_report_address)" PANEL_PORT="${GRPC_PORT_HINT:-${LISTEN##*:}}" \
    PANEL_CSR="${srv_csr}" python3 - <<'PY' >"${payload_file}"
import json, os
with open(os.environ["PANEL_CSR"], "r", encoding="utf-8") as f:
    csr = f.read()
print(json.dumps({
    "node_id": os.environ["PANEL_NODE_ID"],
    "csr_pem": csr,
    "address": os.environ.get("PANEL_ADDRESS", ""),
    "grpc_port": int(os.environ.get("PANEL_PORT") or "50051"),
}))
PY

  local code
  code="$(curl -sS --max-time 30 --retry 3 -o "${response_file}" -w '%{http_code}' \
    -X POST "${PANEL_URL%/}/api/v1/pki/agent-certificates" \
    -H "Authorization: Bearer ${ENROLL_TOKEN:-${ACTIVE_TOKEN:-${TOKEN}}}" \
    -H "Content-Type: application/json" \
    --data-binary "@${payload_file}")" || die "Panel CA 请求失败"
  if [[ "${code}" != "201" ]]; then
    die "Panel CA 签发失败 (HTTP ${code}): $(head -c 500 "${response_file}")"
  fi
  PANEL_RESPONSE="${response_file}" PANEL_CERT="${srv_crt}" PANEL_CA="${ca_crt}" PANEL_TOKEN_FILE="${token_file}" python3 - <<'PY'
import json, os
with open(os.environ["PANEL_RESPONSE"], "r", encoding="utf-8") as f:
    data = json.load(f)
for key, path in (("cert_pem", os.environ["PANEL_CERT"]), ("ca_bundle_pem", os.environ["PANEL_CA"])):
    value = data.get(key, "")
    if not value:
        raise SystemExit("Panel 响应缺少字段 " + key)
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(value)
    os.replace(tmp, path)
token = data.get("control_token", "")
if token:
    with open(os.environ["PANEL_TOKEN_FILE"], "w", encoding="utf-8") as f:
        f.write(token)
PY
  if [[ -f "${token_file}" ]]; then
    TOKEN="$(cat "${token_file}")"
    ACTIVE_TOKEN="${TOKEN}"
  fi
  [[ -n "${TOKEN}" ]] || die "Panel 未返回 Agent 控制令牌"
  openssl x509 -in "${srv_crt}" -noout -checkend 3600 >/dev/null \
    || die "Panel 返回的 Agent 证书无效或即将过期"
  openssl verify -CAfile "${ca_crt}" "${srv_crt}" >/dev/null \
    || die "Panel 返回的 Agent 证书链校验失败"
  local key_pub cert_pub
  key_pub="$(openssl pkey -in "${srv_key}" -pubout 2>/dev/null)"
  cert_pub="$(openssl x509 -in "${srv_crt}" -pubkey -noout 2>/dev/null)"
  [[ "${key_pub}" == "${cert_pub}" ]] || die "Panel 返回证书与本地私钥不匹配"
  rm -f "${srv_csr}" "${req_conf}" "${response_file}" "${payload_file}" "${token_file}"
  chmod 600 "${srv_key}"
  chmod 644 "${srv_crt}" "${ca_crt}"
  chown -R "${USER_NAME}:${GROUP_NAME}" "${TLS_DIR}"
  chmod 700 "${TLS_DIR}"
  umask "${_saved_umask}"
}

# Pick an address Panel should dial (override with LADDER_REPORT_ADDRESS).
detect_report_address() {
  if [[ -n "${REPORT_ADDR}" ]]; then
    echo "${REPORT_ADDR}"
    return
  fi
  local ip pub
  # Prefer first non-loopback from hostname -I
  for ip in $(hostname -I 2>/dev/null || true); do
    case "${ip}" in
      127.*|::1) continue ;;
      *)
        echo "${ip}"
        return
        ;;
    esac
  done
  if [[ -n "${IP_ECHO_URL}" ]] && command -v curl >/dev/null 2>&1; then
    pub="$(curl -fsS --max-time 3 "${IP_ECHO_URL}" 2>/dev/null || true)"
    if [[ "${pub}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      echo "${pub}"
      return
    fi
  fi
  echo ""
}

ensure_user_and_dirs() {
  echo "==> 创建用户/组 ${USER_NAME}"
  getent group "${GROUP_NAME}" >/dev/null || groupadd --system "${GROUP_NAME}"
  if ! id -u "${USER_NAME}" >/dev/null 2>&1; then
    useradd --system --gid "${GROUP_NAME}" --home-dir "${DATA_DIR}" \
      --shell /usr/sbin/nologin --create-home "${USER_NAME}" 2>/dev/null \
      || useradd --system --gid "${GROUP_NAME}" --home-dir "${DATA_DIR}" \
        --shell /bin/false "${USER_NAME}"
  fi

  echo "==> 准备目录"
  mkdir -p "${CONF_DIR}" "${DATA_DIR}"
  mkdir -p "${DATA_DIR}/upgrade"
  chown -R "${USER_NAME}:${GROUP_NAME}" "${DATA_DIR}"
  chmod 750 "${DATA_DIR}"
  chmod 755 "${CONF_DIR}"
  # Agent stages binaries here; root helper applies them.
  chown "${USER_NAME}:${GROUP_NAME}" "${DATA_DIR}/upgrade"
  chmod 755 "${DATA_DIR}/upgrade"
}

# Install binary; keep previous as .bak when upgrading.
install_binary() {
  local src="$1"
  local backup="${INSTALL_BIN}.bak"
  if [[ -x "${INSTALL_BIN}" ]]; then
    echo "==> 备份旧二进制 → ${backup}"
    cp -a "${INSTALL_BIN}" "${backup}" || true
  fi
  echo "==> 安装二进制 → ${INSTALL_BIN}"
  install -m 0755 "${src}" "${INSTALL_BIN}"
  # 授予非 root 监听 1024 以下端口能力（与 unit 的 AmbientCapabilities 配合；
  # install 会重置 file capabilities，故每次安装/升级后都要重新 setcap）
  if command -v setcap >/dev/null 2>&1; then
    setcap cap_net_bind_service+ep "${INSTALL_BIN}" 2>/dev/null \
      || echo "WARNING: setcap cap_net_bind_service 失败，1024 以下端口将无法监听" >&2
  else
    echo "WARNING: 未找到 setcap，跳过 capability 授予（1024 以下端口将无法监听）" >&2
  fi
}

write_unit() {
  local unit_desc="LadderAirport Agent (Panel-managed mTLS)"
  if skip_management_tls; then
    unit_desc="LadderAirport Agent (uplink HTTP+WS)"
  fi
  echo "==> 写入 systemd: ${SERVICE_DST}"
  # uplink 不创建 TLS 目录。ReadWritePaths 指向不存在的路径时，systemd 建命名空间会失败（226/NAMESPACE）。
  local rw_paths="${DATA_DIR}"
  if [[ -d "${TLS_DIR}" ]]; then
    rw_paths="${rw_paths} ${TLS_DIR}"
  fi
  cat >"${SERVICE_DST}" <<EOF
[Unit]
Description=${unit_desc}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${USER_NAME}
Group=${GROUP_NAME}
WorkingDirectory=${DATA_DIR}
EnvironmentFile=${ENV_FILE}
# LADDER_TOKEN 由 EnvironmentFile 注入环境（agent 从环境变量回退读取），不放命令行
ExecStart=${INSTALL_BIN} -listen=\${LADDER_LISTEN} -data-dir=\${LADDER_DATA_DIR} -tls-cert=\${LADDER_TLS_CERT} -tls-key=\${LADDER_TLS_KEY} -tls-client-ca=\${LADDER_TLS_CLIENT_CA} -panel-url=\${LADDER_PANEL_URL} -node-id=\${LADDER_NODE_ID} -report-address=\${LADDER_REPORT_ADDRESS} -tls-sans=\${LADDER_TLS_EXTRA_SANS}
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576
NoNewPrivileges=true
# NoNewPrivileges=true 下 file capabilities 不生效；用 AmbientCapabilities 授予
# 非 root 监听 1024 以下端口能力（与二进制上的 setcap cap_net_bind_service+ep 并存）
AmbientCapabilities=CAP_NET_BIND_SERVICE
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${rw_paths}
ReadOnlyPaths=${CONF_DIR}
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
}

# Root-owned helper: watches staged binary and replaces INSTALL_BIN + restarts agent.
# Required because ladder-agent runs as unprivileged user with NoNewPrivileges.
write_upgrade_units() {
  local helper="/usr/local/lib/ladder-agent/apply-upgrade.sh"
  local helper_dir
  helper_dir="$(dirname "${helper}")"
  mkdir -p "${helper_dir}"

  echo "==> 写入远程升级助手: ${helper}"
  cat >"${helper}" <<'EOS'
#!/bin/bash
set -euo pipefail
INSTALL_BIN="${INSTALL_BIN:-/usr/local/bin/ladder-agent}"
UPGRADE_DIR="${UPGRADE_DIR:-/var/lib/ladder-agent/upgrade}"
SERVICE_NAME="${SERVICE_NAME:-ladder-agent.service}"
ARCH="$(uname -m)"
case "${ARCH}" in
  x86_64|amd64) ASSET="ladder-agent-linux-amd64" ;;
  aarch64|arm64) ASSET="ladder-agent-linux-arm64" ;;
  armv7l|armhf) ASSET="ladder-agent-linux-armhf" ;;
  *) echo "不支持的架构：${ARCH}" >&2; exit 1 ;;
esac
STAGED="${UPGRADE_DIR}/${ASSET}"
READY="${STAGED}.ready"
LOG_TAG="ladder-agent-upgrade"

if [[ ! -f "${READY}" ]]; then
  exit 0
fi
if [[ ! -f "${STAGED}" ]]; then
  logger -t "${LOG_TAG}" "存在升级就绪标记，但缺少待升级二进制：${STAGED}"
  rm -f "${READY}" || true
  exit 1
fi
if [[ ! -s "${STAGED}" ]]; then
  logger -t "${LOG_TAG}" "待升级二进制为空"
  rm -f "${READY}" || true
  exit 1
fi

# 若 agent 落盘了 <二进制名>.sha256（sha256sum -c 兼容格式），先校验再应用；
# 校验失败拒绝安装并保留现场（STAGED/READY 均不删，便于排查后人工处理）。
SUMS="${STAGED}.sha256"
if [[ -f "${SUMS}" ]]; then
  if ! command -v sha256sum >/dev/null 2>&1; then
    logger -t "${LOG_TAG}" "存在 ${SUMS} 但缺少 sha256sum，拒绝应用升级（保留现场）"
    exit 1
  fi
  if ! (cd "${UPGRADE_DIR}" && sha256sum -c "${ASSET}.sha256" >/dev/null 2>&1); then
    logger -t "${LOG_TAG}" "SHA256 校验失败，拒绝应用升级：${STAGED}（保留现场）"
    exit 1
  fi
  logger -t "${LOG_TAG}" "SHA256 校验通过：${STAGED}"
fi

logger -t "${LOG_TAG}" "正在替换升级二进制 ${STAGED} -> ${INSTALL_BIN}"
if [[ -x "${INSTALL_BIN}" ]]; then
  cp -a "${INSTALL_BIN}" "${INSTALL_BIN}.bak" || true
fi
install -m 0755 "${STAGED}" "${INSTALL_BIN}"
# install 会重置 file capabilities；重新授予，保证升级后仍可监听 1024 以下端口
if command -v setcap >/dev/null 2>&1; then
  setcap cap_net_bind_service+ep "${INSTALL_BIN}" 2>/dev/null \
    || logger -t "${LOG_TAG}" "WARNING: setcap cap_net_bind_service 失败，1024 以下端口将无法监听"
else
  logger -t "${LOG_TAG}" "WARNING: 未找到 setcap，跳过 capability 授予"
fi
rm -f "${READY}"
# Keep staged copy briefly for debug; remove partials
rm -f "${STAGED}.partial" || true
systemctl restart "${SERVICE_NAME}"
logger -t "${LOG_TAG}" "升级已应用，${SERVICE_NAME} 已重启"
EOS
  chmod 0755 "${helper}"

  local unit_dir="/etc/systemd/system"
  echo "==> 写入 systemd path/service: ladder-agent-upgrade.*"
  cat >"${unit_dir}/ladder-agent-upgrade.path" <<EOF
[Unit]
Description=Watch LadderAirport agent upgrade staging dir

[Path]
PathExists=${DATA_DIR}/upgrade/ladder-agent-linux-amd64.ready
PathExists=${DATA_DIR}/upgrade/ladder-agent-linux-arm64.ready
PathExists=${DATA_DIR}/upgrade/ladder-agent-linux-armhf.ready
Unit=ladder-agent-upgrade.service

[Install]
WantedBy=multi-user.target
EOF

  cat >"${unit_dir}/ladder-agent-upgrade.service" <<EOF
[Unit]
Description=Apply staged LadderAirport agent binary
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=${helper}
Nice=0
EOF
}

# Root-owned helper: applies BBR enable/disable requests staged by the agent.
# The agent runs unprivileged and cannot change sysctl; it writes
# DATA_DIR/bbr.request (enable|disable) and ladder-agent-bbr.path triggers the
# oneshot service below as root whenever the file appears or changes.
write_bbr_units() {
  local helper="/usr/local/lib/ladder-agent/apply-bbr.sh"
  local helper_dir
  helper_dir="$(dirname "${helper}")"
  mkdir -p "${helper_dir}"

  echo "==> 写入 BBR 助手: ${helper}"
  # DATA_DIR 在写入时按实际值展开；运行时可用 BBR_REQUEST/BBR_CONF 覆盖（便于测试）
  cat >"${helper}" <<EOF
#!/bin/bash
set -euo pipefail
REQUEST="\${BBR_REQUEST:-${DATA_DIR}/bbr.request}"
CONF="\${BBR_CONF:-/etc/sysctl.d/99-ladder-bbr.conf}"
LOG_TAG="ladder-agent-bbr"

if [[ ! -f "\${REQUEST}" ]]; then
  exit 0
fi
# 请求内容为 enable 或 disable（忽略首尾空白）；其它内容记录警告并丢弃
content="\$(tr -d '[:space:]' < "\${REQUEST}")"
case "\${content}" in
  enable)
    sysctl -w net.core.default_qdisc=fq net.ipv4.tcp_congestion_control=bbr
    printf '%s\n' 'net.core.default_qdisc=fq' 'net.ipv4.tcp_congestion_control=bbr' >"\${CONF}"
    logger -t "\${LOG_TAG}" "已启用 BBR（fq + bbr），持久化到 \${CONF}"
    ;;
  disable)
    sysctl -w net.ipv4.tcp_congestion_control=cubic
    rm -f "\${CONF}"
    logger -t "\${LOG_TAG}" "已恢复 cubic 拥塞控制，删除 \${CONF}"
    ;;
  *)
    logger -t "\${LOG_TAG}" "WARNING: 非法 BBR 请求内容 \"\${content}\"，已丢弃"
    ;;
esac
rm -f "\${REQUEST}"
EOF
  chmod 0755 "${helper}"

  local unit_dir="/etc/systemd/system"
  echo "==> 写入 systemd path/service: ladder-agent-bbr.*"
  cat >"${unit_dir}/ladder-agent-bbr.path" <<EOF
[Unit]
Description=Watch LadderAirport agent BBR request file

[Path]
# PathExistsModified= 只在较新的 systemd 里存在；拆开后旧版本也能监视文件出现和改写。
PathExists=${DATA_DIR}/bbr.request
PathModified=${DATA_DIR}/bbr.request
Unit=ladder-agent-bbr.service

[Install]
WantedBy=multi-user.target
EOF

  cat >"${unit_dir}/ladder-agent-bbr.service" <<EOF
[Unit]
Description=Apply LadderAirport agent BBR request (sysctl)

[Service]
Type=oneshot
ExecStart=${helper}
EOF
}

enable_and_restart() {
  write_upgrade_units
  write_bbr_units
  systemctl daemon-reload
  systemctl enable "${SERVICE_NAME}"
  systemctl enable ladder-agent-upgrade.path
  systemctl restart ladder-agent-upgrade.path || systemctl start ladder-agent-upgrade.path || true
  # 注意：enable + start 的是 .path 监视单元，不是 oneshot .service
  systemctl enable ladder-agent-bbr.path
  systemctl restart ladder-agent-bbr.path || systemctl start ladder-agent-bbr.path || true
  systemctl restart "${SERVICE_NAME}"
  sleep 0.8
  systemctl --no-pager --full status "${SERVICE_NAME}" || true
}

# Load TLS cert paths from existing agent.env (upgrade path).
load_tls_from_env() {
  TLS_CERT_PATH=""
  TLS_KEY_PATH=""
  TLS_CLIENT_CA_PATH=""
  if [[ -f "${ENV_FILE}" ]]; then
    # shellcheck disable=SC1090
    # Prefer grep over source to avoid executing unexpected content
    local cert key
    cert="$(grep -E '^LADDER_TLS_CERT=' "${ENV_FILE}" | head -1 | cut -d= -f2- || true)"
    key="$(grep -E '^LADDER_TLS_KEY=' "${ENV_FILE}" | head -1 | cut -d= -f2- || true)"
    if [[ -n "${cert}" && -n "${key}" && -f "${cert}" && -f "${key}" ]]; then
      TLS_CERT_PATH="${cert}"
      TLS_KEY_PATH="${key}"
    fi
    local client_ca
    client_ca="$(grep -E '^LADDER_TLS_CLIENT_CA=' "${ENV_FILE}" | head -1 | cut -d= -f2- || true)"
    if [[ -n "${client_ca}" && -f "${client_ca}" ]]; then
      TLS_CLIENT_CA_PATH="${client_ca}"
    fi
    local saved_panel saved_node saved_report saved_sans
    saved_panel="$(grep -E '^LADDER_PANEL_URL=' "${ENV_FILE}" | head -1 | cut -d= -f2- || true)"
    saved_node="$(grep -E '^LADDER_NODE_ID=' "${ENV_FILE}" | head -1 | cut -d= -f2- || true)"
    saved_report="$(grep -E '^LADDER_REPORT_ADDRESS=' "${ENV_FILE}" | head -1 | cut -d= -f2- || true)"
    saved_sans="$(grep -E '^LADDER_TLS_EXTRA_SANS=' "${ENV_FILE}" | head -1 | cut -d= -f2- || true)"
    [[ -n "${PANEL_URL}" ]] || PANEL_URL="${saved_panel}"
    [[ -n "${NODE_ID}" ]] || NODE_ID="${saved_node}"
    [[ -n "${REPORT_ADDR}" ]] || REPORT_ADDR="${saved_report}"
    [[ -n "${TLS_EXTRA_SANS}" ]] || TLS_EXTRA_SANS="${saved_sans}"
  fi
}

require_installed() {
  if [[ ! -f "${INSTALL_BIN}" && ! -f "${SERVICE_DST}" && ! -f "${ENV_FILE}" ]]; then
    die "未检测到已安装的 Agent（${INSTALL_BIN} / ${SERVICE_DST} / ${ENV_FILE}）。请先 install。"
  fi
}

# ---------- install ----------
do_install() {
  load_saved_uplink_mode
  [[ -n "${PANEL_URL}" ]] || die "缺少 LADDER_PANEL"
  [[ -n "${NODE_ID}" ]] || die "缺少 LADDER_NODE_ID"
  if [[ "${PANEL_URL}" != https://* && "${ALLOW_HTTP}" != "1" ]]; then
    die "LADDER_PANEL 必须使用 HTTPS；仅隔离测试环境可设置 LADDER_ALLOW_HTTP=1"
  fi
  if [[ -f "${ENV_FILE}" || -f "${SERVICE_DST}" ]]; then
    load_tls_from_env
    if [[ -f "${TLS_DIR}/ca.key" ]]; then
      die "检测到不兼容的旧 Agent；请先用 LADDER_ACTION=uninstall LADDER_PURGE=1 全清卸载，再在 Panel 新建节点并执行新的安装命令"
    fi
    if ! skip_management_tls; then
      if [[ -z "${TLS_CERT_PATH}" || -z "${TLS_KEY_PATH}" || -z "${TLS_CLIENT_CA_PATH}" ]]; then
        die "检测到不兼容的旧 Agent；请先用 LADDER_ACTION=uninstall LADDER_PURGE=1 全清卸载，再在 Panel 新建节点并执行新的安装命令"
      fi
    fi
  fi
  if [[ -z "${ENROLL_TOKEN}" ]]; then
    if [[ -f "${TLS_DIR}/ca.key" ]]; then
      die "检测到不兼容的旧 Agent；请先全清卸载，再在 Panel 新建节点并执行新的安装命令"
    fi
    if ! skip_management_tls; then
      if [[ -z "${TLS_CERT_PATH}" || -z "${TLS_KEY_PATH}" || -z "${TLS_CLIENT_CA_PATH}" ]]; then
        die "检测到不兼容的旧 Agent；请先全清卸载，再在 Panel 新建节点并执行新的安装命令"
      fi
    fi
    if [[ -z "${TOKEN}" && -f "${ENV_FILE}" ]]; then
      TOKEN="$(grep -E '^LADDER_TOKEN=' "${ENV_FILE}" | head -1 | cut -d= -f2- || true)"
    fi
  fi
  [[ -n "${TOKEN}" || -n "${ENROLL_TOKEN}" ]] || die "缺少 LADDER_ENROLL_TOKEN 或已有 LADDER_TOKEN"
  if [[ -n "${ENROLL_TOKEN}" ]]; then
    echo "==> 使用 Panel 一次性注册令牌（成功后换取 Agent 控制令牌）"
  fi

  ensure_user_and_dirs

  local src
  src="$(resolve_binary)"
  install_binary "${src}"

  if skip_management_tls; then
    if [[ -n "${ENROLL_TOKEN}" || -z "${TOKEN}" ]]; then
      exchange_control_token
    fi
    TLS_CERT_PATH=""
    TLS_KEY_PATH=""
    TLS_CLIENT_CA_PATH=""
  else
    ensure_tls_material
    TLS_CERT_PATH="${TLS_DIR}/server.crt"
    TLS_KEY_PATH="${TLS_DIR}/server.key"
    TLS_CLIENT_CA_PATH="${TLS_DIR}/ca.crt"
  fi
  rm -f "${TLS_DIR}/ca.key" "${TLS_DIR}/ca.srl" "${CONF_DIR}/panel-import.txt"

  echo "==> 配置 ${ENV_FILE}"
  PANEL_ENV_SOURCE="${ENV_FILE}" PANEL_ENV_OUTPUT="${ENV_FILE}.tmp" \
  PANEL_TOKEN="${TOKEN}" PANEL_LISTEN="${LISTEN}" PANEL_DATA_DIR="${DATA_DIR}" \
  PANEL_CERT="${TLS_CERT_PATH}" PANEL_KEY="${TLS_KEY_PATH}" PANEL_CA="${TLS_CLIENT_CA_PATH}" \
  PANEL_URL_VALUE="${PANEL_URL%/}" PANEL_NODE_VALUE="${NODE_ID}" \
  PANEL_REPORT_VALUE="${REPORT_ADDR}" PANEL_SANS_VALUE="${TLS_EXTRA_SANS}" \
  PANEL_UPLINK="${UPLINK}" PANEL_UPLINK_SERVE_GRPC="${UPLINK_SERVE_GRPC}" \
    python3 - <<'PY'
import os
source = os.environ["PANEL_ENV_SOURCE"]
output = os.environ["PANEL_ENV_OUTPUT"]
updates = {
    "LADDER_LISTEN": os.environ["PANEL_LISTEN"],
    "LADDER_TOKEN": os.environ["PANEL_TOKEN"],
    "LADDER_DATA_DIR": os.environ["PANEL_DATA_DIR"],
    "LADDER_TLS_CERT": os.environ["PANEL_CERT"],
    "LADDER_TLS_KEY": os.environ["PANEL_KEY"],
    "LADDER_TLS_CLIENT_CA": os.environ["PANEL_CA"],
    "LADDER_PANEL_URL": os.environ["PANEL_URL_VALUE"],
    "LADDER_NODE_ID": os.environ["PANEL_NODE_VALUE"],
    "LADDER_REPORT_ADDRESS": os.environ.get("PANEL_REPORT_VALUE", ""),
    "LADDER_TLS_EXTRA_SANS": os.environ.get("PANEL_SANS_VALUE", ""),
    "LADDER_UPLINK": os.environ.get("PANEL_UPLINK", "0"),
    "LADDER_UPLINK_SERVE_GRPC": os.environ.get("PANEL_UPLINK_SERVE_GRPC", "0"),
}
seen, lines = set(), []
if os.path.exists(source):
    with open(source, "r", encoding="utf-8") as f:
        for raw in f:
            key = raw.split("=", 1)[0] if "=" in raw and not raw.lstrip().startswith("#") else ""
            if key in updates:
                if key not in seen:
                    lines.append(f"{key}={updates[key]}\n")
                    seen.add(key)
            else:
                lines.append(raw)
for key, value in updates.items():
    if key not in seen:
        lines.append(f"{key}={value}\n")
with open(output, "w", encoding="utf-8") as f:
    f.writelines(lines)
PY
  install -m 0640 -o root -g "${GROUP_NAME}" "${ENV_FILE}.tmp" "${ENV_FILE}"
  rm -f "${ENV_FILE}.tmp"

  write_unit
  enable_and_restart

  echo
  echo "======== 安装完成 ========"
  echo "  动作:    install"
  echo "  二进制:  ${INSTALL_BIN}"
  echo "  配置:    ${ENV_FILE}"
  echo "  数据:    ${DATA_DIR}"
  echo "  服务:    ${SERVICE_NAME} (已 enable + start)"
  echo "  BBR:     ladder-agent-bbr.path (已 enable + start；Panel 节点详情「系统状态」页签开关)"
  echo "  来源:    FROM=${FROM} VERSION=${VERSION}"
  if skip_management_tls; then
    echo "  TLS:     未初始化（HTTP 上报 + WebSocket）"
  else
    echo "  TLS:     Panel CA + strict mTLS"
  fi
  echo "  Panel:   ${PANEL_URL%/}"
  echo "  请在 Panel 刷新节点 → 探测"
  echo
  echo "运维: systemctl status|restart ladder-agent ; journalctl -u ladder-agent -f"
  echo "升级: curl -fsSL .../install-agent.sh | sudo env LADDER_ACTION=upgrade [LADDER_VERSION=vX.Y.Z] bash"
  echo "卸载: curl -fsSL .../install-agent.sh | sudo env LADDER_ACTION=uninstall bash"
  if ! skip_management_tls; then
    echo "证书会由 Agent 自动续签；不要删除 ${TLS_DIR}/server.key"
  fi
}

# ---------- upgrade ----------
# Replace binary + refresh unit + restart. Never touch token/TLS/enroll.
do_upgrade() {
  require_installed
  if [[ ! -f "${ENV_FILE}" ]]; then
    die "缺少 ${ENV_FILE}"
  fi
  load_saved_uplink_mode
  load_tls_from_env
  [[ ! -f "${TLS_DIR}/ca.key" ]] || die "检测到旧节点 CA 私钥；请先全清卸载，再重新安装"
  if [[ -z "${PANEL_URL}" || -z "${NODE_ID}" ]]; then
    die "缺少 Panel URL 或节点 ID；请先用 LADDER_ACTION=uninstall LADDER_PURGE=1 全清卸载，再重新安装"
  fi
  if ! skip_management_tls; then
    if [[ -z "${TLS_CERT_PATH}" || -z "${TLS_KEY_PATH}" || -z "${TLS_CLIENT_CA_PATH}" ]]; then
      die "检测到不兼容的旧 Agent TLS；请先用 LADDER_ACTION=uninstall LADDER_PURGE=1 全清卸载，再重新安装"
    fi
  fi

  if skip_management_tls; then
    echo "==> 升级 ladder-agent（uplink，不要求管理面 TLS）"
  else
    echo "==> 升级 ladder-agent（保留 Panel PKI 身份）"
  fi
  ensure_user_and_dirs

  local src
  src="$(resolve_binary)"
  install_binary "${src}"

  write_unit
  enable_and_restart

  echo
  echo "======== 升级完成 ========"
  echo "  动作:    upgrade"
  echo "  二进制:  ${INSTALL_BIN}"
  if [[ -x "${INSTALL_BIN}.bak" ]]; then
    echo "  备份:    ${INSTALL_BIN}.bak （回滚: sudo mv ${INSTALL_BIN}.bak ${INSTALL_BIN} && systemctl restart ladder-agent）"
  fi
  echo "  配置:    ${ENV_FILE}（未改 Token）"
  echo "  服务:    ${SERVICE_NAME} (已 restart)"
  echo "  来源:    FROM=${FROM} VERSION=${VERSION}"
  if skip_management_tls; then
    echo "  TLS:     未要求（HTTP 上报 + WebSocket）"
  else
    echo "  TLS:     Panel CA + strict mTLS"
  fi
  echo
  echo "运维: systemctl status ladder-agent ; journalctl -u ladder-agent -f"
  echo "在 Panel 刷新/探测节点以确认 agent_version"
  echo "远程升级: Panel 节点详情点「远程升级」（需 ladder-agent-upgrade.path 已 enable）"
  echo "BBR 开关: Panel 节点详情「系统状态」页签（需 ladder-agent-bbr.path 已 enable）"
}

# ---------- uninstall ----------
do_uninstall() {
  echo "==> 卸载 ladder-agent"

  if systemctl list-unit-files "${SERVICE_NAME}" &>/dev/null || [[ -f "${SERVICE_DST}" ]]; then
    systemctl stop "${SERVICE_NAME}" 2>/dev/null || true
    systemctl disable "${SERVICE_NAME}" 2>/dev/null || true
  fi
  systemctl stop ladder-agent-upgrade.path 2>/dev/null || true
  systemctl disable ladder-agent-upgrade.path 2>/dev/null || true
  rm -f /etc/systemd/system/ladder-agent-upgrade.path /etc/systemd/system/ladder-agent-upgrade.service
  rm -f /usr/local/lib/ladder-agent/apply-upgrade.sh
  systemctl stop ladder-agent-bbr.path 2>/dev/null || true
  systemctl disable ladder-agent-bbr.path 2>/dev/null || true
  rm -f /etc/systemd/system/ladder-agent-bbr.path /etc/systemd/system/ladder-agent-bbr.service
  rm -f /usr/local/lib/ladder-agent/apply-bbr.sh
  if [[ -f "${SERVICE_DST}" ]]; then
    rm -f "${SERVICE_DST}"
    echo "  已删除 unit: ${SERVICE_DST}"
  fi
  systemctl daemon-reload 2>/dev/null || true
  systemctl reset-failed "${SERVICE_NAME}" 2>/dev/null || true

  if [[ -f "${INSTALL_BIN}" ]]; then
    rm -f "${INSTALL_BIN}"
    echo "  已删除二进制: ${INSTALL_BIN}"
  fi
  if [[ -f "${INSTALL_BIN}.bak" ]]; then
    rm -f "${INSTALL_BIN}.bak"
    echo "  已删除备份: ${INSTALL_BIN}.bak"
  fi

  case "${PURGE}" in
    1|true|TRUE|yes|YES|on|ON)
      echo "==> LADDER_PURGE=1：删除配置与数据"
      if [[ -d "${CONF_DIR}" ]]; then
        rm -rf "${CONF_DIR}"
        echo "  已删除: ${CONF_DIR}"
      fi
      if [[ -d "${DATA_DIR}" ]]; then
        rm -rf "${DATA_DIR}"
        echo "  已删除: ${DATA_DIR}"
      fi
      if id -u "${USER_NAME}" >/dev/null 2>&1; then
        userdel "${USER_NAME}" 2>/dev/null || true
        echo "  已尝试删除用户: ${USER_NAME}"
      fi
      if getent group "${GROUP_NAME}" >/dev/null 2>&1; then
        groupdel "${GROUP_NAME}" 2>/dev/null || true
        echo "  已尝试删除组: ${GROUP_NAME}"
      fi
      if [[ -f /etc/sysctl.d/99-ladder-bbr.conf ]]; then
        rm -f /etc/sysctl.d/99-ladder-bbr.conf
        echo "  已删除: /etc/sysctl.d/99-ladder-bbr.conf（当前内核拥塞控制重启后恢复默认）"
      fi
      ;;
    *)
      echo "==> 已保留配置与数据（需要全清请加 LADDER_PURGE=1）:"
      [[ -d "${CONF_DIR}" ]] && echo "  conf: ${CONF_DIR}"
      [[ -d "${DATA_DIR}" ]] && echo "  data: ${DATA_DIR}"
      [[ -f /etc/sysctl.d/99-ladder-bbr.conf ]] && echo "  sysctl: /etc/sysctl.d/99-ladder-bbr.conf"
      ;;
  esac

  echo
  echo "======== 卸载完成 ========"
  echo "  动作: uninstall"
  case "${PURGE}" in
    1|true|TRUE|yes|YES|on|ON) echo "  模式: purge（配置/数据已删）" ;;
    *) echo "  模式: 保留 conf/data；全清: LADDER_ACTION=uninstall LADDER_PURGE=1" ;;
  esac
  echo "  若节点仍在 Panel 登记，请在 Panel 中删除该节点记录"
}

# ---------- dispatch ----------
echo "==> ladder-agent 脚本动作: ${ACTION}"
case "${ACTION}" in
  install) do_install ;;
  upgrade) do_upgrade ;;
  uninstall) do_uninstall ;;
esac
