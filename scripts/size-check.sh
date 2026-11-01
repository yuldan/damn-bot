#!/usr/bin/env bash
# Gate ukuran binary (SPEC P13, ENGINEERING E-60/E-42): full ≤ 52 MB, minimal ≤ 22 MB.
# Build kanonik: CGO_ENABLED=0, -trimpath, -ldflags "-s -w", tag dari $TAGS (default goolm, E-39).
# Budget per modul SPEC §3.2.4 diukur saat modul ada (F1+).
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
TAGS=${TAGS:-goolm}
FULL_MAX=${FULL_MAX:-$((52 * 1000 * 1000))}
MINIMAL_MAX=${MINIMAL_MAX:-$((22 * 1000 * 1000))}
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT

fail=0
check() { # <nama> <tags> <maks>
	local name=$1 tags=$2 max=$3 bin="$out/damnbot-$1" size
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -tags "$tags" -o "$bin" ./cmd/damnbot
	size=$(stat -c %s "$bin")
	if ((size > max)); then
		echo "SIZE GAGAL $name ($tags): $size B > $max B"
		fail=1
	else
		echo "SIZE ok    $name ($tags): $size B / $max B"
	fi
}
check full "$TAGS" "$FULL_MAX"
check minimal "$TAGS,minimal" "$MINIMAL_MAX"
exit $fail
