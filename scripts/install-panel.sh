#!/usr/bin/env bash
# 一键安装 / 升级 / 卸载 LadderAirport Panel（systemd）。
# 默认从 GitHub Release 拉最新二进制。
#
# 安装（推荐）:
#   curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-panel.sh \
#     | sudo bash
#
# 生产推荐（固定 session secret + 监听）:
#   curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-panel.sh \
#     | sudo env LADDER_SESSION_SECRET='长随机串' LADDER_LISTEN=':8080' bash
#
# 升级（只换二进制 + 刷新 unit + restart；保留 panel.env 与 SQLite）:
#   curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-panel.sh \
#     | sudo env LADDER_ACTION=upgrade LADDER_VERSION=v0.3.1 bash
#
# 卸载（默认保留 conf/data；LADDER_PURGE=1 全清，会删 panel.db）:
#   curl -fsSL https://raw.githubusercontent.com/LadderAirport/LadderAirport/main/scripts/install-panel.sh \
#     | sudo env LADDER_ACTION=uninstall bash
#
# 其它:
#   sudo LADDER_SESSION_SECRET='...' ./scripts/install-panel.sh /path/to/panel
#   sudo LADDER_FROM=local LADDER_SESSION_SECRET='...' ./scripts/install-panel.sh
#   sudo ./scripts/install-panel.sh upgrade
#   sudo ./scripts/install-panel.sh uninstall
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

INSTALL_BIN="${INSTALL_BIN:-/usr/local/bin/ladder-panel}"
CONF_DIR="${CONF_DIR:-/etc/ladder-panel}"
DATA_DIR="${DATA_DIR:-/var/lib/ladder-panel}"
SERVICE_DST="${SERVICE_DST:-/etc/systemd/system/ladder-panel.service}"
SERVICE_NAME="ladder-panel.service"
USER_NAME="${LADDER_USER:-ladder-panel}"
GROUP_NAME="${LADDER_GROUP:-ladder-panel}"
LISTEN="${LADDER_LISTEN:-:8080}"
SESSION_SECRET="${LADDER_SESSION_SECRET:-}"
ADMIN_PASSWORD="${LADDER_ADMIN_PASSWORD:-}"
DB_PATH="${LADDER_DB:-${DATA_DIR}/panel.db}"
BOOTSTRAP="${LADDER_BOOTSTRAP:-true}"
BOOTSTRAP_TIMEOUT="${LADDER_BOOTSTRAP_TIMEOUT:-3m}"
BOOTSTRAP_RETRY="${LADDER_BOOTSTRAP_RETRY:-true}"
BOOTSTRAP_RETRY_INTERVAL="${LADDER_BOOTSTRAP_RETRY_INTERVAL:-30s}"
# release | local  （默认 release）
FROM="${LADDER_FROM:-release}"
VERSION="${LADDER_VERSION:-latest}" # latest 或 v0.3.1
TMPDIR_DL=""
ENV_FILE="${CONF_DIR}/panel.env"

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

# --- session secret helpers ---
gen_secret() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
  fi
}

# Only generate when caller did not pass one; actual write happens later and
# never overwrites an existing panel.env secret.
ensure_session_secret() {
  if [[ -z "${SESSION_SECRET}" ]]; then
    SESSION_SECRET="$(gen_secret)"
    echo "==> 已生成 LADDER_SESSION_SECRET（将写入 panel.env，请妥善备份）"
  fi
}

# Append key=value to panel.env only when missing (never overwrite existing).
ensure_env_key() {
  local key="$1" val="$2"
  if ! grep -q "^${key}=" "${ENV_FILE}" 2>/dev/null; then
    echo "${key}=${val}" >>"${ENV_FILE}"
    echo "    已追加 ${key}"
  fi
}

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
  asset="panel-linux-${arch}"

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

  TMPDIR_DL="$(mktemp -d /tmp/ladder-panel-dl.XXXXXX)"
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
    # Prefer already-built binary names used by Makefile / install path
    if [[ -n "${ROOT}" && -x "${ROOT}/bin/panel" ]]; then
      echo "==> 使用仓库 bin/panel" >&2
      printf '%s\n' "${ROOT}/bin/panel"
      return
    fi
    if [[ -n "${ROOT}" && -x "${ROOT}/bin/ladder-panel" ]]; then
      echo "==> 使用仓库 bin/ladder-panel" >&2
      printf '%s\n' "${ROOT}/bin/ladder-panel"
      return
    fi
    [[ -n "${ROOT}" ]] || die "LADDER_FROM=local 需要在仓库内执行脚本"
    echo "==> 本地编译 panel（使用已提交的 panel/web/dist，无需 npm）" >&2
    need_cmd go
    # Offline-friendly: skip npm web rebuild; dist is committed for go:embed
    (cd "${ROOT}/panel" && go build -trimpath -ldflags="-s -w" -o ../bin/panel ./cmd/panel) >&2
    printf '%s\n' "${ROOT}/bin/panel"
    return
  fi

  download_release_binary
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
  chown -R "${USER_NAME}:${GROUP_NAME}" "${DATA_DIR}"
  chmod 750 "${DATA_DIR}"
  chmod 755 "${CONF_DIR}"
}

install_binary() {
  local src="$1"
  local backup="${INSTALL_BIN}.bak"
  if [[ -x "${INSTALL_BIN}" ]]; then
    echo "==> 备份旧二进制 → ${backup}"
    cp -a "${INSTALL_BIN}" "${backup}" || true
  fi
  echo "==> 安装二进制 → ${INSTALL_BIN}"
  install -m 0755 "${src}" "${INSTALL_BIN}"
}

write_unit() {
  echo "==> 写入 systemd: ${SERVICE_DST}"
  # Single-line ExecStart; systemd expands ${LADDER_*} from EnvironmentFile.
  # LADDER_SESSION_SECRET 只经 EnvironmentFile 注入环境（panel 从环境变量回退读取），
  # 不出现在命令行，避免 ps/proc 泄露。
  cat >"${SERVICE_DST}" <<EOF
[Unit]
Description=LadderAirport Panel (control plane)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${USER_NAME}
Group=${GROUP_NAME}
WorkingDirectory=${DATA_DIR}
EnvironmentFile=${ENV_FILE}
ExecStart=${INSTALL_BIN} -listen \${LADDER_LISTEN} -db \${LADDER_DB} -bootstrap=\${LADDER_BOOTSTRAP} -bootstrap-timeout \${LADDER_BOOTSTRAP_TIMEOUT} -bootstrap-retry=\${LADDER_BOOTSTRAP_RETRY} -bootstrap-retry-interval \${LADDER_BOOTSTRAP_RETRY_INTERVAL}
Restart=on-failure
RestartSec=3
LimitNOFILE=65536
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${DATA_DIR}
ReadOnlyPaths=${CONF_DIR}
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
}

enable_and_restart() {
  systemctl daemon-reload
  systemctl enable "${SERVICE_NAME}"
  systemctl restart "${SERVICE_NAME}"
  sleep 0.8
  systemctl --no-pager --full status "${SERVICE_NAME}" || true
}

require_installed() {
  if [[ ! -f "${INSTALL_BIN}" && ! -f "${SERVICE_DST}" && ! -f "${ENV_FILE}" ]]; then
    die "未检测到已安装的 Panel（${INSTALL_BIN} / ${SERVICE_DST} / ${ENV_FILE}）。请先 install。"
  fi
}

read_active_env() {
  ACTIVE_LISTEN=""
  ACTIVE_DB=""
  if [[ -f "${ENV_FILE}" ]]; then
    ACTIVE_LISTEN="$(grep -E '^LADDER_LISTEN=' "${ENV_FILE}" | head -1 | cut -d= -f2- || true)"
    ACTIVE_DB="$(grep -E '^LADDER_DB=' "${ENV_FILE}" | head -1 | cut -d= -f2- || true)"
  fi
  [[ -n "${ACTIVE_LISTEN}" ]] || ACTIVE_LISTEN="${LISTEN}"
  [[ -n "${ACTIVE_DB}" ]] || ACTIVE_DB="${DB_PATH}"
}

http_hint_from_listen() {
  local listen="$1"
  local hint="http://127.0.0.1:8080"
  case "${listen}" in
    :*) hint="http://127.0.0.1${listen}" ;;
    0.0.0.0:*) hint="http://127.0.0.1:${listen##*:}" ;;
    \[::\]:*) hint="http://127.0.0.1:${listen##*:}" ;;
    *)
      if [[ "${listen}" == *:* ]]; then
        hint="http://${listen}"
      fi
      ;;
  esac
  echo "${hint}"
}

# ---------- install ----------
do_install() {
  ensure_user_and_dirs

  local src
  src="$(resolve_binary)"
  install_binary "${src}"

  echo "==> 配置 ${ENV_FILE}"
  if [[ -f "${ENV_FILE}" ]]; then
    echo "    已存在，不覆盖（改 listen/secret/db 请手动编辑后 restart）"
    # Backfill missing keys only (never overwrite existing secret)
    ensure_env_key "LADDER_LISTEN" "${LISTEN}"
    ensure_env_key "LADDER_DB" "${DB_PATH}"
    if ! grep -q '^LADDER_SESSION_SECRET=' "${ENV_FILE}" 2>/dev/null; then
      ensure_session_secret
      ensure_env_key "LADDER_SESSION_SECRET" "${SESSION_SECRET}"
    fi
    ensure_env_key "LADDER_BOOTSTRAP" "${BOOTSTRAP}"
    ensure_env_key "LADDER_BOOTSTRAP_TIMEOUT" "${BOOTSTRAP_TIMEOUT}"
    ensure_env_key "LADDER_BOOTSTRAP_RETRY" "${BOOTSTRAP_RETRY}"
    ensure_env_key "LADDER_BOOTSTRAP_RETRY_INTERVAL" "${BOOTSTRAP_RETRY_INTERVAL}"
    # 透传初始管理员密码（仅首次初始化生效；已有键不覆盖）
    if [[ -n "${ADMIN_PASSWORD}" ]]; then
      ensure_env_key "LADDER_ADMIN_PASSWORD" "${ADMIN_PASSWORD}"
    fi
    # Show non-secret keys for operator feedback
    grep -E '^LADDER_LISTEN=|^LADDER_DB=|^LADDER_BOOTSTRAP' "${ENV_FILE}" || true
    if grep -q '^LADDER_SESSION_SECRET=' "${ENV_FILE}" 2>/dev/null; then
      echo "    LADDER_SESSION_SECRET=(已配置，未显示)"
    fi
  else
    ensure_session_secret
    {
      echo "# /etc/ladder-panel/panel.env"
      echo "LADDER_LISTEN=${LISTEN}"
      echo "LADDER_DB=${DB_PATH}"
      echo "LADDER_SESSION_SECRET=${SESSION_SECRET}"
      echo "LADDER_BOOTSTRAP=${BOOTSTRAP}"
      echo "LADDER_BOOTSTRAP_TIMEOUT=${BOOTSTRAP_TIMEOUT}"
      echo "LADDER_BOOTSTRAP_RETRY=${BOOTSTRAP_RETRY}"
      echo "LADDER_BOOTSTRAP_RETRY_INTERVAL=${BOOTSTRAP_RETRY_INTERVAL}"
      if [[ -n "${ADMIN_PASSWORD}" ]]; then
        echo "LADDER_ADMIN_PASSWORD=${ADMIN_PASSWORD}"
      fi
    } >"${ENV_FILE}"
    chmod 640 "${ENV_FILE}"
    chown root:"${GROUP_NAME}" "${ENV_FILE}"
  fi

  read_active_env
  write_unit
  enable_and_restart

  local http_hint
  http_hint="$(http_hint_from_listen "${ACTIVE_LISTEN}")"

  echo
  echo "======== Panel 安装完成 ========"
  echo "  动作:    install"
  echo "  二进制:  ${INSTALL_BIN}"
  echo "  配置:    ${ENV_FILE}"
  echo "  数据:    ${DATA_DIR}"
  echo "  数据库:  ${ACTIVE_DB}"
  echo "  监听:    ${ACTIVE_LISTEN}"
  echo "  服务:    ${SERVICE_NAME} (已 enable + start)"
  echo "  来源:    FROM=${FROM} VERSION=${VERSION}"
  echo
  echo "  浏览器:  ${http_hint}"
  echo "  初始管理员密码: 随机生成，见 journalctl -u ladder-panel（仅打印一次；可用 LADDER_ADMIN_PASSWORD 预置）"
  echo
  echo "建议下一步:"
  echo "  1. 在「设置」填写 Public Base URL（用于订阅链接与节点一键安装命令）"
  echo "  2. 登录后修改管理员密码，并备份 session secret、SQLite 与 ${DATA_DIR}/pki"
  echo "  3. 安全备份后将 ${DATA_DIR}/pki/offline/root-ca.key 转移到离线介质"
  echo "  4. 按需用 Nginx/Caddy 反代 HTTPS 到 ${ACTIVE_LISTEN}"
  echo "  5. 安装节点: 见 deploy/README-agent.md 或 Panel「添加节点并生成安装命令」"
  echo
  echo "运维: systemctl status|restart ladder-panel ; journalctl -u ladder-panel -f"
  echo "升级: curl -fsSL .../install-panel.sh | sudo env LADDER_ACTION=upgrade [LADDER_VERSION=vX.Y.Z] bash"
  echo "卸载: curl -fsSL .../install-panel.sh | sudo env LADDER_ACTION=uninstall bash"
  echo "备份: 复制 ${ACTIVE_DB}、${ENV_FILE} 与 ${DATA_DIR}/pki（根私钥应另存离线）"
}

# ---------- upgrade ----------
do_upgrade() {
  require_installed
  echo "==> 升级 ladder-panel（保留 ${ENV_FILE} 与 SQLite）"

  ensure_user_and_dirs

  local src
  src="$(resolve_binary)"
  install_binary "${src}"

  if [[ ! -f "${ENV_FILE}" ]]; then
    die "缺少 ${ENV_FILE}。无法安全升级（无 session secret）。请改用 install 或手动恢复 env。"
  fi

  # Backfill missing keys only — never overwrite secret
  ensure_env_key "LADDER_LISTEN" "${LISTEN}"
  ensure_env_key "LADDER_DB" "${DB_PATH}"
  ensure_env_key "LADDER_BOOTSTRAP" "${BOOTSTRAP}"
  ensure_env_key "LADDER_BOOTSTRAP_TIMEOUT" "${BOOTSTRAP_TIMEOUT}"
  ensure_env_key "LADDER_BOOTSTRAP_RETRY" "${BOOTSTRAP_RETRY}"
  ensure_env_key "LADDER_BOOTSTRAP_RETRY_INTERVAL" "${BOOTSTRAP_RETRY_INTERVAL}"
  if ! grep -q '^LADDER_SESSION_SECRET=' "${ENV_FILE}" 2>/dev/null; then
    ensure_session_secret
    ensure_env_key "LADDER_SESSION_SECRET" "${SESSION_SECRET}"
  fi

  read_active_env
  write_unit
  enable_and_restart

  local http_hint
  http_hint="$(http_hint_from_listen "${ACTIVE_LISTEN}")"

  echo
  echo "======== Panel 升级完成 ========"
  echo "  动作:    upgrade"
  echo "  二进制:  ${INSTALL_BIN}"
  if [[ -x "${INSTALL_BIN}.bak" ]]; then
    echo "  备份:    ${INSTALL_BIN}.bak （回滚: sudo mv ${INSTALL_BIN}.bak ${INSTALL_BIN} && systemctl restart ladder-panel）"
  fi
  echo "  配置:    ${ENV_FILE}（未改 session secret）"
  echo "  数据库:  ${ACTIVE_DB}"
  echo "  监听:    ${ACTIVE_LISTEN}"
  echo "  服务:    ${SERVICE_NAME} (已 restart)"
  echo "  来源:    FROM=${FROM} VERSION=${VERSION}"
  echo "  浏览器:  ${http_hint}"
  echo
  echo "运维: systemctl status ladder-panel ; journalctl -u ladder-panel -f"
}

# ---------- uninstall ----------
do_uninstall() {
  echo "==> 卸载 ladder-panel"

  read_active_env

  if systemctl list-unit-files "${SERVICE_NAME}" &>/dev/null || [[ -f "${SERVICE_DST}" ]]; then
    systemctl stop "${SERVICE_NAME}" 2>/dev/null || true
    systemctl disable "${SERVICE_NAME}" 2>/dev/null || true
  fi
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
      echo "==> LADDER_PURGE=1：删除配置与数据（含 SQLite）"
      if [[ -d "${CONF_DIR}" ]]; then
        rm -rf "${CONF_DIR}"
        echo "  已删除: ${CONF_DIR}"
      fi
      if [[ -d "${DATA_DIR}" ]]; then
        rm -rf "${DATA_DIR}"
        echo "  已删除: ${DATA_DIR}"
      fi
      # Also remove DB (and SQLite WAL/SHM sidecars) if it lived outside DATA_DIR
      if [[ -n "${ACTIVE_DB}" ]]; then
        local _dbf
        for _dbf in "${ACTIVE_DB}" "${ACTIVE_DB}-wal" "${ACTIVE_DB}-shm"; do
          if [[ -f "${_dbf}" ]]; then
            rm -f "${_dbf}"
            echo "  已删除: ${_dbf}"
          fi
        done
      fi
      if id -u "${USER_NAME}" >/dev/null 2>&1; then
        userdel "${USER_NAME}" 2>/dev/null || true
        echo "  已尝试删除用户: ${USER_NAME}"
      fi
      if getent group "${GROUP_NAME}" >/dev/null 2>&1; then
        groupdel "${GROUP_NAME}" 2>/dev/null || true
        echo "  已尝试删除组: ${GROUP_NAME}"
      fi
      ;;
    *)
      echo "==> 已保留配置与数据（需要全清请加 LADDER_PURGE=1；purge 会删除 panel.db）:"
      [[ -d "${CONF_DIR}" ]] && echo "  conf: ${CONF_DIR}"
      [[ -d "${DATA_DIR}" ]] && echo "  data: ${DATA_DIR}"
      [[ -n "${ACTIVE_DB}" && -f "${ACTIVE_DB}" ]] && echo "  db:   ${ACTIVE_DB}"
      echo "  备份建议: sudo cp -a ${ACTIVE_DB:-${DATA_DIR}/panel.db} /root/panel.db.bak"
      ;;
  esac

  echo
  echo "======== 卸载完成 ========"
  echo "  动作: uninstall"
  case "${PURGE}" in
    1|true|TRUE|yes|YES|on|ON) echo "  模式: purge（配置/数据/数据库已删）" ;;
    *) echo "  模式: 保留 conf/data；全清: LADDER_ACTION=uninstall LADDER_PURGE=1" ;;
  esac
}

# ---------- dispatch ----------
echo "==> ladder-panel 脚本动作: ${ACTION}"
case "${ACTION}" in
  install) do_install ;;
  upgrade) do_upgrade ;;
  uninstall) do_uninstall ;;
esac
