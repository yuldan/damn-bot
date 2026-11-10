#!/usr/bin/env bash
# Pembungkus WAJIB untuk perintah berat di host bersama (ENGINEERING §5.5, ADR-341).
#
# Pekerjaan agent memakai maksimal 60% host (10 vCPU → 6 CPU, RAM 23,5 GiB → GOMEMLIMIT
# 1500 MiB per proses, disk → lihat agent-guard.sh). Wrapper ini:
#   - menolak jalan bila `agent-guard.sh check` gagal atau flag PAUSE_HEAVY ada (kecuali --force);
#   - mengambil flock global ~/.cache/damnbot/heavy.lock sehingga hanya SATU perintah berat
#     berjalan pada satu waktu; proses lain MENGANTRE dengan batas HEAVY_WAIT (default 1800 dtk,
#     bisa lewat --wait) dan timeout = exit 1 dengan pesan jelas;
#   - menjalankan perintah dengan nice 19, taskset 0-5 (6/10 vCPU), GOMAXPROCS=2, GOFLAGS=-p=1,
#     GOMEMLIMIT=1500MiB, dan alamat virtual 4 GiB (8 GiB untuk `-race`).
#
# Pemakaian: scripts/agent-heavy.sh [--force] [--wait <detik>] <perintah> [arg...]
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
GUARD=$ROOT/scripts/agent-guard.sh
STATE_DIR=${GUARD_STATE_DIR:-${HOME}/.cache/damnbot}
LOCK=$STATE_DIR/heavy.lock
PAUSE_FLAG=$STATE_DIR/PAUSE_HEAVY

usage() {
	cat <<'EOF'
Pemakaian: scripts/agent-heavy.sh [--force] [--wait <detik>] <perintah> [arg...]

  --force     lewati cek ambang dan flag PAUSE_HEAVY (flock dan limit tetap berlaku)
  --wait N    batas antrean lock dalam detik (default HEAVY_WAIT=1800); timeout = exit 1
EOF
}

force=0
wait_secs=${HEAVY_WAIT:-1800}
while (($#)); do
	case $1 in
	--force)
		force=1
		shift
		;;
	--wait)
		shift
		if (($# == 0)); then
			echo "agent-heavy: --wait membutuhkan nilai detik" >&2
			exit 2
		fi
		wait_secs=$1
		shift
		;;
	--wait=*)
		wait_secs=${1#--wait=}
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
if [[ ! $wait_secs =~ ^[0-9]+$ ]]; then
	echo "agent-heavy: HEAVY_WAIT/--wait harus bilangan bulat detik (dapat: $wait_secs)" >&2
	exit 2
fi
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
	echo "agent-heavy: lock dipegang proses lain; menunggu hingga ${wait_secs}s ($LOCK)" >&2
	if ! flock -w "$wait_secs" 9; then
		echo "agent-heavy: timeout ${wait_secs}s menunggu lock ($LOCK); pemegang lock tidak diketahui; naikkan HEAVY_WAIT/--wait atau tunggu" >&2
		exit 1
	fi
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
