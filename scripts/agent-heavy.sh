#!/usr/bin/env bash
# Pembungkus WAJIB untuk perintah berat di host bersama (ENGINEERING §5.5, ADR-341).
#
# Pekerjaan agent memakai maksimal 60% host (10 vCPU → 6 CPU, RAM 23,5 GiB → GOMEMLIMIT
# 1500 MiB per proses, disk → lihat agent-guard.sh). Wrapper ini:
#   - menolak jalan bila `agent-guard.sh check` gagal atau flag PAUSE_HEAVY ada (kecuali --force);
#   - mengambil flock global ~/.cache/damnbot/heavy.lock sehingga hanya SATU perintah berat
#     berjalan pada satu waktu (proses kedua langsung ditolak, bukan mengantre);
#   - menjalankan perintah dengan nice 19, taskset 0-5 (6/10 vCPU), GOMAXPROCS=2, GOFLAGS=-p=1,
#     GOMEMLIMIT=1500MiB, dan alamat virtual 4 GiB (8 GiB untuk `-race`).
#
# Pemakaian: scripts/agent-heavy.sh [--force] <perintah> [arg...]
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
GUARD=$ROOT/scripts/agent-guard.sh
STATE_DIR=${GUARD_STATE_DIR:-${HOME}/.cache/damnbot}
LOCK=$STATE_DIR/heavy.lock
PAUSE_FLAG=$STATE_DIR/PAUSE_HEAVY

usage() {
	cat <<'EOF'
Pemakaian: scripts/agent-heavy.sh [--force] <perintah> [arg...]

  --force   lewati cek ambang dan flag PAUSE_HEAVY (flock dan limit tetap berlaku)
EOF
}

force=0
while (($#)); do
	case $1 in
	--force)
		force=1
		shift
		;;
	--help | -h)
		usage
		exit 0
		;;
	--)
		shift
		break
		;;
	-*)
		echo "agent-heavy: opsi tidak dikenal: $1" >&2
		usage >&2
		exit 2
		;;
	*) break ;;
	esac
done
(($#)) || {
	usage >&2
	exit 2
}
command -v taskset >/dev/null 2>&1 || {
	echo "agent-heavy: taskset tidak tersedia; tidak bisa membatasi CPU ke 60%" >&2
	exit 2
}
command -v flock >/dev/null 2>&1 || {
	echo "agent-heavy: flock tidak tersedia; tidak bisa menjamin satu perintah berat" >&2
	exit 2
}

mkdir -p "$STATE_DIR"
exec 9>>"$LOCK"
if ! flock -n 9; then
	echo "agent-heavy: perintah berat lain sedang berjalan (lock: $LOCK); tunggu sampai selesai" >&2
	exit 1
fi

if ((!force)); then
	if [[ -e $PAUSE_FLAG ]]; then
		echo "agent-heavy: PAUSE_HEAVY aktif ($PAUSE_FLAG); disk di atas 60%. Pakai --force hanya bila disengaja" >&2
		exit 1
	fi
	if ! "$GUARD" check; then
		echo "agent-heavy: ditolak: agent-guard.sh check gagal (lihat nilai di atas); pakai --force bila disengaja" >&2
		exit 1
	fi
fi

export GOMAXPROCS=2
export GOFLAGS=-p=1
export GOMEMLIMIT=1500MiB

race=0
for arg in "$@"; do
	case $arg in
	-race | -race=*) race=1 ;;
	esac
done
if ((race)); then
	ulimit -v 8388608
else
	ulimit -v 4194304
fi

echo "agent-heavy: nice=19 cpu=0-5 GOMAXPROCS=$GOMAXPROCS GOFLAGS=$GOFLAGS GOMEMLIMIT=$GOMEMLIMIT ulimit-v=$(ulimit -v) race=$race" >&2
exec nice -n 19 taskset -c 0-5 "$@"
