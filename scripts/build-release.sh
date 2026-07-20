#!/bin/sh

set -eu

VERSION=${1:-}
OUT_DIR=${2:-dist}

[ -n "$VERSION" ] || {
    printf '%s\n' "usage: $0 VERSION [OUTPUT_DIRECTORY]" >&2
    exit 1
}

case "$VERSION" in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    *)
        printf '%s\n' "version must look like v1.0.0" >&2
        exit 1
        ;;
esac

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mkdir -p "$OUT_DIR"
OUT_DIR=$(CDPATH= cd -- "$OUT_DIR" && pwd)

if [ -n "$(find "$OUT_DIR" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
    printf '%s\n' "output directory is not empty: $OUT_DIR" >&2
    exit 1
fi

TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/h248gw-release.XXXXXX")
cleanup() {
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT HUP INT TERM

cd "$ROOT_DIR"

for arch in amd64 arm64; do
    asset="h248-sip-gateway-linux-${arch}"
    printf '%s\n' "building ${asset}"
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
        go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o "${OUT_DIR}/${asset}" ./cmd/gateway

    bundle="${TMP_DIR}/h248-sip-gateway-${VERSION}-linux-${arch}"
    mkdir -p "$bundle"
    mkdir -p "$bundle/docs"
    mkdir -p "$bundle/deploy/asterisk-lab"
    mkdir -p "$bundle/deploy/huawei-ar6121e"
    mkdir -p "$bundle/deploy/spes"
    cp "${OUT_DIR}/${asset}" "${bundle}/h248-sip-gateway"
    cp scripts/install.sh "${bundle}/install.sh"
    cp gateway.example.yaml "${bundle}/gateway.example.yaml"
    cp deploy/systemd/h248-sip-gateway.service "${bundle}/h248-sip-gateway.service"
    cp LICENSE "${bundle}/LICENSE"
    cp README.md "${bundle}/README.md"
    cp README.zh-CN.md "${bundle}/README.zh-CN.md"
    cp docs/H248-SIP-INTERWORKING.md "${bundle}/H248-SIP-INTERWORKING.md"
    cp docs/H248-SIP-INTERWORKING.zh-CN.md \
        "${bundle}/H248-SIP-INTERWORKING.zh-CN.md"
    cp docs/DEPLOYMENT.md "${bundle}/DEPLOYMENT.md"
    cp docs/DEPLOYMENT.zh-CN.md "${bundle}/DEPLOYMENT.zh-CN.md"
    cp docs/SIP-PBX-INTEROP.md "${bundle}/SIP-PBX-INTEROP.md"
    cp docs/SIP-PBX-INTEROP.zh-CN.md "${bundle}/SIP-PBX-INTEROP.zh-CN.md"
    cp docs/*.md "${bundle}/docs/"
    cp deploy/asterisk-lab/README*.md "${bundle}/deploy/asterisk-lab/"
    cp deploy/huawei-ar6121e/README*.md \
        "${bundle}/deploy/huawei-ar6121e/"
    cp deploy/spes/README*.md "${bundle}/deploy/spes/"
    (
        cd "$bundle"
        if command -v sha256sum >/dev/null 2>&1; then
            sha256sum h248-sip-gateway > checksums.txt
        else
            shasum -a 256 h248-sip-gateway > checksums.txt
        fi
    )
    tar -C "$TMP_DIR" -czf "${OUT_DIR}/h248-sip-gateway-${VERSION}-linux-${arch}.tar.gz" \
        "h248-sip-gateway-${VERSION}-linux-${arch}"
done

cp scripts/install.sh "${OUT_DIR}/install.sh"
cp gateway.example.yaml "${OUT_DIR}/gateway.example.yaml"
cp deploy/systemd/h248-sip-gateway.service "${OUT_DIR}/h248-sip-gateway.service"
cp LICENSE "${OUT_DIR}/LICENSE"

(
    cd "$OUT_DIR"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum h248-sip-gateway-linux-amd64 h248-sip-gateway-linux-arm64 \
            h248-sip-gateway-"${VERSION}"-linux-amd64.tar.gz \
            h248-sip-gateway-"${VERSION}"-linux-arm64.tar.gz \
            install.sh gateway.example.yaml h248-sip-gateway.service LICENSE > checksums.txt
    else
        shasum -a 256 h248-sip-gateway-linux-amd64 h248-sip-gateway-linux-arm64 \
            h248-sip-gateway-"${VERSION}"-linux-amd64.tar.gz \
            h248-sip-gateway-"${VERSION}"-linux-arm64.tar.gz \
            install.sh gateway.example.yaml h248-sip-gateway.service LICENSE > checksums.txt
    fi
)

printf '%s\n' "release assets written to ${OUT_DIR}"
