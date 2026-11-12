#!/usr/bin/env bash
# Smoke Termux/Android (OPS-031, CROSS-CUTTING §7): build statis CGO=0 -tags goolm lalu
# subset unit test. Opt-in dan tanpa root. Skrip ini tidak dipanggil CI/job default; jalankan
# manual di perangkat Termux.
#
#   DAMNBOT_TERMUX_SMOKE=1 ./scripts/termux-smoke.sh
#
# Variabel: DAMNBOT_TERMUX_BIN (default <repo>/.bin/damnbot-android),
#           DAMNBOT_TERMUX_PKGS (default ./internal/config ./internal/i18n ./internal/schema),
#           DAMNBOT_TERMUX_TIMEOUT (default 300s).
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

if [[ ${DAMNBOT_TERMUX_SMOKE:-0} != 1 ]]; then
	echo "termux-smoke: opt-in; set DAMNBOT_TERMUX_SMOKE=1 untuk menjalankan" >&2
	exit 2
fi

if [[ $(id -u) -eq 0 ]]; then
	echo "termux-smoke: menolak UID 0; jalankan sebagai pengguna Termux biasa (tanpa root)" >&2
	exit 1
fi

if [[ -z ${PREFIX:-} || ! -d ${PREFIX:-} ]]; then
	echo "termux-smoke: bukan Termux (PREFIX tidak diset); jalankan di Termux Android arm64" >&2
	exit 78
fi

if ! command -v go >/dev/null 2>&1; then
	echo "termux-smoke: go tidak ditemukan; jalankan: pkg install golang" >&2
	exit 1
fi

BIN=${DAMNBOT_TERMUX_BIN:-$ROOT/.bin/damnbot-android}
PKGS=${DAMNBOT_TERMUX_PKGS:-"./internal/config ./internal/i18n ./internal/schema"}
TIMEOUT=${DAMNBOT_TERMUX_TIMEOUT:-300s}

mkdir -p "$(dirname "$BIN")"
echo "termux-smoke: build CGO_ENABLED=0 -tags goolm -> $BIN"
CGO_ENABLED=0 GOOS=android GOARCH=arm64 \
	go build -trimpath -tags goolm -ldflags="-s -w" -o "$BIN" ./cmd/damnbot
"$BIN" --version

echo "termux-smoke: unit subset (-timeout $TIMEOUT): $PKGS"
# shellcheck disable=SC2086 # PKGS sengaja daftar paket yang dipisah spasi.
CGO_ENABLED=0 go test -count=1 -tags goolm -timeout "$TIMEOUT" $PKGS

echo "termux-smoke: OK (android/arm64, tanpa root)"
