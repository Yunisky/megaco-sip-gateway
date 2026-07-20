#!/bin/sh

set -eu

PROJECT="h248-sip-gateway"
REPOSITORY_URL="https://github.com/Yunisky/megaco-sip-gateway"
SERVICE_NAME="h248-sip-gateway.service"
SERVICE_ACCOUNT="h248gw"
INSTALL_BIN="/usr/local/bin/h248-sip-gateway"
CONFIG_DIR="/etc/h248-sip-gateway"
INSTALL_CONFIG="${CONFIG_DIR}/gateway.yaml"
INSTALL_UNIT="/etc/systemd/system/${SERVICE_NAME}"
BACKUP_ROOT="/var/lib/h248-sip-gateway/backups"

VERSION="latest"
BINARY_SOURCE=""
CONFIG_SOURCE=""
UNIT_SOURCE=""
START_SERVICE=1
TMP_DIR=""
CHECKSUM_SOURCE=""

usage() {
    cat <<'EOF'
Install or upgrade h248-sip-gateway on a systemd Linux host.

Usage:
  sudo ./install.sh --config ./gateway.yaml [options]
  sudo ./install.sh --version v1.0.0 [options]   # reuse installed config

Options:
  --config FILE    Validate and install this gateway configuration. Existing
                   configurations are backed up before replacement.
  --binary FILE    Install a local gateway binary instead of auto-detecting or
                   downloading a release asset.
  --unit FILE      Install a local systemd unit instead of auto-detecting or
                   downloading the release unit.
  --version TAG    Release tag to download, for example v1.0.0. The default is
                   the latest GitHub release.
  --no-start       Install and validate files, but do not enable or restart the
                   service.
  -h, --help       Show this help.

The script deliberately does not change interfaces, VRFs, routes, DNS, or
firewall rules. Prepare those host-specific items before starting the service.
EOF
}

log() {
    printf '%s\n' "[$PROJECT] $*"
}

die() {
    printf '%s\n' "[$PROJECT] ERROR: $*" >&2
    exit 1
}

cleanup() {
    if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
        rm -rf "$TMP_DIR"
    fi
}

trap cleanup EXIT HUP INT TERM

while [ "$#" -gt 0 ]; do
    case "$1" in
        --config)
            [ "$#" -ge 2 ] || die "--config requires a file"
            CONFIG_SOURCE=$2
            shift 2
            ;;
        --binary)
            [ "$#" -ge 2 ] || die "--binary requires a file"
            BINARY_SOURCE=$2
            shift 2
            ;;
        --unit)
            [ "$#" -ge 2 ] || die "--unit requires a file"
            UNIT_SOURCE=$2
            shift 2
            ;;
        --version)
            [ "$#" -ge 2 ] || die "--version requires a tag"
            VERSION=$2
            shift 2
            ;;
        --no-start)
            START_SERVICE=0
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            die "unknown option: $1"
            ;;
    esac
done

[ "$(id -u)" -eq 0 ] || die "run this script as root (for example with sudo)"
[ "$(uname -s)" = "Linux" ] || die "only Linux is supported"
command -v systemctl >/dev/null 2>&1 || die "systemd/systemctl is required"
command -v install >/dev/null 2>&1 || die "the install utility is required"
command -v awk >/dev/null 2>&1 || die "awk is required"
command -v getent >/dev/null 2>&1 || die "getent is required"
command -v groupadd >/dev/null 2>&1 || die "groupadd is required"
command -v useradd >/dev/null 2>&1 || die "useradd is required"

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

case "$(uname -m)" in
    x86_64|amd64)
        RELEASE_ARCH="amd64"
        ;;
    aarch64|arm64)
        RELEASE_ARCH="arm64"
        ;;
    *)
        die "unsupported architecture: $(uname -m); use --binary with a compatible build"
        ;;
esac

if [ "$VERSION" = "latest" ]; then
    RELEASE_BASE="${REPOSITORY_URL}/releases/latest/download"
else
    RELEASE_BASE="${REPOSITORY_URL}/releases/download/${VERSION}"
fi
RELEASE_BINARY="h248-sip-gateway-linux-${RELEASE_ARCH}"

ensure_tmp_dir() {
    if [ -z "$TMP_DIR" ]; then
        TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/h248gw-install.XXXXXX")
        chmod 700 "$TMP_DIR"
    fi
}

download() {
    source_url=$1
    destination=$2
    if command -v curl >/dev/null 2>&1; then
        curl --fail --location --silent --show-error "$source_url" --output "$destination"
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$source_url" -O "$destination"
    else
        die "curl or wget is required to download release assets"
    fi
}

ensure_release_checksums() {
    if [ -z "$CHECKSUM_SOURCE" ]; then
        ensure_tmp_dir
        CHECKSUM_SOURCE="${TMP_DIR}/checksums.txt"
        download "${RELEASE_BASE}/checksums.txt" "$CHECKSUM_SOURCE"
    fi
}

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        die "sha256sum or shasum is required"
    fi
}

verify_checksum() {
    checked_file=$1
    checksum_name=$2
    checksum_file=$3
    expected=$(awk -v name="$checksum_name" '$2 == name || $2 == "*" name {print $1; exit}' "$checksum_file")
    [ -n "$expected" ] || die "no checksum for ${checksum_name} in ${checksum_file}"
    actual=$(sha256_file "$checked_file")
    [ "$actual" = "$expected" ] || die "SHA-256 mismatch for ${checksum_name}"
    log "verified SHA-256 for ${checksum_name}"
}

if [ -z "$BINARY_SOURCE" ]; then
    for candidate in \
        "${SCRIPT_DIR}/h248-sip-gateway" \
        "${SCRIPT_DIR}/${RELEASE_BINARY}" \
        "${SCRIPT_DIR}/../${RELEASE_BINARY}"
    do
        if [ -f "$candidate" ]; then
            BINARY_SOURCE=$candidate
            break
        fi
    done
fi

if [ -z "$BINARY_SOURCE" ]; then
    ensure_tmp_dir
    BINARY_SOURCE="${TMP_DIR}/${RELEASE_BINARY}"
    log "downloading ${RELEASE_BINARY} from ${RELEASE_BASE}"
    download "${RELEASE_BASE}/${RELEASE_BINARY}" "$BINARY_SOURCE"
    ensure_release_checksums
    verify_checksum "$BINARY_SOURCE" "$RELEASE_BINARY" "$CHECKSUM_SOURCE"
elif [ -f "${SCRIPT_DIR}/checksums.txt" ]; then
    checksum_name=$(basename "$BINARY_SOURCE")
    if awk -v name="$checksum_name" '$2 == name || $2 == "*" name {found=1} END {exit !found}' "${SCRIPT_DIR}/checksums.txt"; then
        verify_checksum "$BINARY_SOURCE" "$checksum_name" "${SCRIPT_DIR}/checksums.txt"
    else
        log "no bundle checksum entry for ${checksum_name}; validating executable and configuration instead"
    fi
fi

[ -f "$BINARY_SOURCE" ] || die "binary not found: ${BINARY_SOURCE}"
ensure_tmp_dir
STAGED_BINARY="${TMP_DIR}/h248-sip-gateway-candidate"
install -m 0755 "$BINARY_SOURCE" "$STAGED_BINARY"
BINARY_SOURCE=$STAGED_BINARY

if [ -z "$UNIT_SOURCE" ]; then
    for candidate in \
        "${SCRIPT_DIR}/h248-sip-gateway.service" \
        "${SCRIPT_DIR}/../deploy/systemd/h248-sip-gateway.service"
    do
        if [ -f "$candidate" ]; then
            UNIT_SOURCE=$candidate
            break
        fi
    done
fi
if [ -z "$UNIT_SOURCE" ]; then
    ensure_tmp_dir
    UNIT_SOURCE="${TMP_DIR}/h248-sip-gateway.service"
    log "downloading systemd unit from ${RELEASE_BASE}"
    download "${RELEASE_BASE}/h248-sip-gateway.service" "$UNIT_SOURCE"
    ensure_release_checksums
    verify_checksum "$UNIT_SOURCE" h248-sip-gateway.service "$CHECKSUM_SOURCE"
fi
[ -f "$UNIT_SOURCE" ] || die "systemd unit not found: ${UNIT_SOURCE}"

if [ -n "$CONFIG_SOURCE" ]; then
    [ -f "$CONFIG_SOURCE" ] || die "configuration not found: ${CONFIG_SOURCE}"
elif [ -f "$INSTALL_CONFIG" ]; then
    CONFIG_SOURCE=$INSTALL_CONFIG
elif [ -f "${SCRIPT_DIR}/gateway.yaml" ]; then
    CONFIG_SOURCE="${SCRIPT_DIR}/gateway.yaml"
else
    die "first installation requires --config FILE; start from gateway.example.yaml"
fi

log "validating configuration with candidate binary"
"$BINARY_SOURCE" -config "$CONFIG_SOURCE" -check-config

if ! getent group "$SERVICE_ACCOUNT" >/dev/null 2>&1; then
    groupadd --system "$SERVICE_ACCOUNT"
fi
if ! id "$SERVICE_ACCOUNT" >/dev/null 2>&1; then
    NOLOGIN_SHELL=$(command -v nologin || true)
    [ -n "$NOLOGIN_SHELL" ] || NOLOGIN_SHELL=/sbin/nologin
    useradd --system --gid "$SERVICE_ACCOUNT" --home-dir /nonexistent --shell "$NOLOGIN_SHELL" "$SERVICE_ACCOUNT"
fi

STAMP=$(date -u +%Y%m%dT%H%M%SZ)
BACKUP_DIR="${BACKUP_ROOT}/${STAMP}"
install -d -o root -g root -m 0700 "$BACKUP_DIR"

backup_if_present() {
    original=$1
    backup_name=$2
    if [ -e "$original" ]; then
        cp -p "$original" "${BACKUP_DIR}/${backup_name}"
    fi
}

backup_if_present "$INSTALL_BIN" h248-sip-gateway
backup_if_present "$INSTALL_CONFIG" gateway.yaml
backup_if_present "$INSTALL_UNIT" h248-sip-gateway.service

install -d -o root -g "$SERVICE_ACCOUNT" -m 0750 "$CONFIG_DIR"
install -o root -g root -m 0755 "$BINARY_SOURCE" "${INSTALL_BIN}.new"
install -o root -g "$SERVICE_ACCOUNT" -m 0640 "$CONFIG_SOURCE" "${INSTALL_CONFIG}.new"
install -o root -g root -m 0644 "$UNIT_SOURCE" "${INSTALL_UNIT}.new"

"${INSTALL_BIN}.new" -config "${INSTALL_CONFIG}.new" -check-config

mv -f "${INSTALL_BIN}.new" "$INSTALL_BIN"
mv -f "${INSTALL_CONFIG}.new" "$INSTALL_CONFIG"
mv -f "${INSTALL_UNIT}.new" "$INSTALL_UNIT"
systemctl daemon-reload

log "installed version: $($INSTALL_BIN -version)"
log "backup directory: ${BACKUP_DIR}"

if [ "$START_SERVICE" -eq 0 ]; then
    log "installation complete; service start skipped by --no-start"
    log "start later with: systemctl enable --now ${SERVICE_NAME}"
    exit 0
fi

systemctl enable "$SERVICE_NAME" >/dev/null
if ! systemctl restart "$SERVICE_NAME"; then
    systemctl --no-pager --full status "$SERVICE_NAME" || true
    log "service start failed; files are preserved and rollback copies are in ${BACKUP_DIR}"
    exit 1
fi

if ! systemctl is-active --quiet "$SERVICE_NAME"; then
    systemctl --no-pager --full status "$SERVICE_NAME" || true
    die "service did not become active; rollback copies are in ${BACKUP_DIR}"
fi

log "deployment successful"
systemctl --no-pager --full status "$SERVICE_NAME" | sed -n '1,12p'
log "follow logs with: journalctl -u ${SERVICE_NAME} -f"
