#!/usr/bin/env bash
# install-service.sh — install test-agent as a systemd service.
#
# Usage:
#   sudo ./install-service.sh [install] [binary] [config]   # default action
#   sudo ./install-service.sh uninstall
#
# install:
#   binary  path to the test-agent binary   (default: ./test-agent)
#   config  path to config.yaml             (default: ./config.yaml)
#
# Layout after install:
#   binary  /usr/local/bin/test-agent
#   config  /etc/agent/config.yaml
#   unit    /etc/systemd/system/test-agent.service
#   WorkingDirectory is /etc/agent, so relative paths in config.yaml
#   (token file, signing keys, mq spool, ./agent.log) persist across reboots.
set -euo pipefail

SERVICE_NAME="test-agent"
BIN_DST="/usr/local/bin/test-agent"
CONF_DIR="/etc/agent"
CONF_DST="${CONF_DIR}/config.yaml"
UNIT_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

log()  { printf '[install-service] %s\n' "$*"; }
fail() { printf '[install-service] ERROR: %s\n' "$*" >&2; exit 1; }

need_root() {
    [ "$(id -u)" -eq 0 ] || fail "please run as root (sudo $0 ...)"
}

need_systemd() {
    command -v systemctl >/dev/null 2>&1 || fail "systemctl not found; this script requires systemd"
}

# Read a scalar from the yaml config: config_value <file> <section> <key>
config_value() {
    awk -v section="$2" -v key="$3" '
        $0 ~ "^"section":" { in_section=1; next }
        /^[a-z_]+:/        { in_section=0 }
        in_section && $1 == key":" { print $2; exit }
    ' "$1" | tr -d '"'
}

do_install() {
    local bin_src="${1:-./test-agent}"
    local conf_src="${2:-./config.yaml}"

    [ -f "$bin_src" ]  || fail "binary not found: $bin_src"
    [ -f "$conf_src" ] || fail "config not found: $conf_src"

    # Stop a running instance first: a running binary cannot be overwritten
    # (text file busy), and the upgrade should pick up the new unit anyway.
    if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
        log "stopping running ${SERVICE_NAME} for upgrade"
        systemctl stop "$SERVICE_NAME"
    fi

    log "installing binary -> ${BIN_DST}"
    install -m 0755 "$bin_src" "$BIN_DST"

    mkdir -p "$CONF_DIR"
    chmod 0700 "$CONF_DIR"  # signing keys and token file live here
    if [ -f "$CONF_DST" ] && ! cmp -s "$conf_src" "$CONF_DST"; then
        local backup="${CONF_DST}.bak.$(date +%Y%m%d%H%M%S)"
        log "existing config differs, backing up -> ${backup}"
        cp -a "$CONF_DST" "$backup"
    fi
    log "installing config -> ${CONF_DST}"
    install -m 0600 "$conf_src" "$CONF_DST"

    log "writing systemd unit -> ${UNIT_FILE}"
    cat > "$UNIT_FILE" <<EOF
[Unit]
Description=test-agent remote execution agent
After=network.target

[Service]
Type=simple
ExecStart=${BIN_DST} --config ${CONF_DST}
WorkingDirectory=${CONF_DIR}
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    log "enabling and starting ${SERVICE_NAME}"
    systemctl enable --now "$SERVICE_NAME"

    sleep 1
    systemctl is-active --quiet "$SERVICE_NAME" \
        || fail "service failed to start; check: journalctl -u ${SERVICE_NAME} -n 50"

    # Best-effort health check on the configured port.
    local port
    port="$(config_value "$CONF_DST" server port)"
    if [ -n "${port:-}" ] && command -v curl >/dev/null 2>&1; then
        if curl -sf "http://127.0.0.1:${port}/api/v1/health" >/dev/null; then
            log "health check passed on port ${port}"
        else
            log "WARN: service is active but health check failed on port ${port}"
        fi
    fi

    # Print the API token so the operator can hand it to the controller
    # without hunting for the token file.
    local token_file
    token_file="$(config_value "$CONF_DST" auth token_file)"
    token_file="${token_file:-.api_token}"
    case "$token_file" in
        /*) : ;;                          # absolute path
        *)  token_file="${CONF_DIR}/${token_file}" ;;  # relative to WorkingDirectory
    esac

    echo
    log "done. service status:"
    systemctl --no-pager --full status "$SERVICE_NAME" | head -n 5 || true
    echo
    if [ -f "$token_file" ]; then
        printf '============================================================\n'
        printf '  API Token : %s\n' "$(cat "$token_file")"
        printf '  Token file: %s\n' "$token_file"
        printf '============================================================\n'
    else
        log "token file ${token_file} not present yet; it will appear after first token init"
    fi
    log "follow logs with: journalctl -u ${SERVICE_NAME} -f"
    log "the token rotates daily; retrieve the current one anytime with:"
    log "  sudo journalctl -u ${SERVICE_NAME} --since today | grep -A3 'Token rotated' | tail -4"
    log "  or: sudo cat ${token_file}"
}

do_uninstall() {
    if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
        systemctl stop "$SERVICE_NAME"
    fi
    if systemctl is-enabled --quiet "$SERVICE_NAME" 2>/dev/null; then
        systemctl disable "$SERVICE_NAME"
    fi
    rm -f "$UNIT_FILE"
    systemctl daemon-reload
    log "service ${SERVICE_NAME} uninstalled"
    log "binary ${BIN_DST} and config ${CONF_DST} were left in place; remove manually if desired"
}

main() {
    local action="${1:-install}"
    case "$action" in
        install)
            need_root; need_systemd
            do_install "${2:-}" "${3:-}"
            ;;
        uninstall)
            need_root; need_systemd
            do_uninstall
            ;;
        -h|--help|help)
            sed -n '2,20p' "$0"
            ;;
        *)
            fail "unknown action: $action (expected install|uninstall)"
            ;;
    esac
}

main "$@"
