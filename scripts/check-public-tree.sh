#!/bin/sh

set -eu

fail() {
    printf '%s\n' "public-tree check failed: $*" >&2
    exit 1
}

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || fail "run inside the Git repository"

forbidden_files=$(git ls-files | grep -E '(^|/)(gateway|gateway\.local)\.yaml$|backupsettings.*\.conf$|\.(pcap|pcapng|key|pem|password|log)$' || true)
[ -z "$forbidden_files" ] || {
    printf '%s\n' "$forbidden_files" >&2
    fail "tracked runtime, capture, credential, or log file"
}

if git grep -nI -E \
    'BEGIN[[:space:]]+(RSA |EC |OPENSSH )?PRIVATE KEY' \
    -- . ':(exclude)scripts/check-public-tree.sh'; then
    fail "private key material"
fi

addresses=$(git grep -hI -o -E '([0-9]{1,3}\.){3}[0-9]{1,3}' -- . | sort -u || true)
for address in $addresses; do
    case "$address" in
        0.0.0.0|127.*|192.0.2.*|198.51.100.*|203.0.113.*)
            ;;
        *)
            fail "non-loopback, non-RFC5737 IPv4 literal: $address"
            ;;
    esac
done

printf '%s\n' "public-tree check passed"
