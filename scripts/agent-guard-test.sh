#!/usr/bin/env bash
# Tes resource guard 60% (ENGINEERING §5.5, ADR-341). Tidak menyentuh data nyata:
# semua pengukuran dipalsukan lewat GUARD_FAKE_*, state/tmp/repo diarahkan ke direktori
# sementara, dan cleanup yang destruktif hanya dijalankan pada fixture /tmp sendiri.
# Pemakaian: scripts/agent-guard-test.sh
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
GUARD=$ROOT/scripts/agent-guard.sh
HEAVY=$ROOT/scripts/agent-heavy.sh
work=$(mktemp -d /tmp/agent-guard-test.XXXXXX)
bg_pid=
trap '[[ -n $bg_pid ]] && kill "$bg_pid" 2>/dev/null || true; rm -rf -- "$work"' EXIT

pass=0
fail=0
last_out=$work/output

ok() {
	pass=$((pass + 1))
	printf 'PASS %s\n' "$1"
}
no() {
	fail=$((fail + 1))
	printf 'FAIL %s\n' "$1"
	[[ -f $last_out ]] && sed 's/^/    | /' "$last_out"
}

# fake_env <state> <tmp> [VAR=…]: env deterministik; host nyata tidak dibaca.
fake_env() {
	local state=$1 tmp=$2
	shift 2
	env GUARD_STATE_DIR="$state" GUARD_TMPDIR="$tmp" GUARD_REPO_ROOT="$work/no-repo" \
		GUARD_FAKE_MEMINFO=21153700 GUARD_FAKE_MEMTOTAL=24641536 GUARD_FAKE_LOADAVG=0.63 "$@"
}
contains() { grep -qF -- "$2" "$1"; }

scenario() { printf '\n== %s\n' "$1"; }

scenario "check lolos di disk 36%"
state=$work/state-check; mkdir -p "$state"
if fake_env "$state" "$work" GUARD_FAKE_DF=36 "$GUARD" check >"$last_out" 2>&1 &&
	contains "$last_out" '36%' && contains "$last_out" 'GUARD OK'; then
	ok "check 36% lulus dan mencetak nilai terukur"
else
	no "check 36% seharusnya lulus"
fi

scenario "check gagal di disk 58%"
if ! fake_env "$state" "$work" GUARD_FAKE_DF=58 "$GUARD" check >"$last_out" 2>&1 &&
	contains "$last_out" 'GUARD GAGAL disk: 58% > 57%'; then
	ok "check 58% gagal dengan exit 1 dan alasan disk"
else
	no "check 58% seharusnya gagal"
fi

scenario "check gagal di MemAvailable 8 GiB dan load1 6.0"
if ! fake_env "$state" "$work" GUARD_FAKE_DF=36 GUARD_FAKE_MEMINFO=$((8 * 1024 * 1024)) \
	"$GUARD" check >"$last_out" 2>&1 && contains "$last_out" 'GUARD GAGAL memory'; then
	ok "check menolak avail 8 GiB di bawah 41% total"
else
	no "check seharusnya menolak MemAvailable 8 GiB"
fi
if ! fake_env "$state" "$work" GUARD_FAKE_DF=36 GUARD_FAKE_LOADAVG=6.0 \
	"$GUARD" check >"$last_out" 2>&1 && contains "$last_out" 'GUARD GAGAL load'; then
	ok "check menolak load1 > 5.5"
else
	no "check seharusnya menolak load1 6.0"
fi

scenario "ambang RAM berbasis persentase (41% MemTotal)"
if ! fake_env "$state" "$work" GUARD_FAKE_MEMINFO=4050000 GUARD_FAKE_MEMTOTAL=10000000 \
	"$GUARD" check >"$last_out" 2>&1 &&
	contains "$last_out" 'used 59.5%' && contains "$last_out" 'GUARD GAGAL memory'; then
	ok "avail 40,5% dari MemTotal ditolak"
else
	no "avail 40,5% seharusnya ditolak"
fi
if fake_env "$state" "$work" GUARD_FAKE_MEMINFO=4150000 GUARD_FAKE_MEMTOTAL=10000000 \
	"$GUARD" check >"$last_out" 2>&1 &&
	contains "$last_out" 'used 58.5%' && contains "$last_out" 'GUARD OK'; then
	ok "avail 41,5% dari MemTotal lolos"
else
	no "avail 41,5% seharusnya lolos"
fi
if ! fake_env "$state" "$work" GUARD_FAKE_MEMINFO=8388608 GUARD_FAKE_MEMTOTAL=0 \
	"$GUARD" check >"$last_out" 2>&1 &&
	contains "$last_out" 'fallback 9 GiB' && contains "$last_out" 'GUARD GAGAL memory'; then
	ok "MemTotal tak terbaca memakai fallback 9 GiB"
else
	no "fallback 9 GiB tidak sesuai"
fi

scenario "report tabel dan --json"
if fake_env "$state" "$work" GUARD_FAKE_DF=36 "$GUARD" report >"$last_out" 2>&1 &&
	contains "$last_out" 'MemAvailable' && contains "$last_out" 'STATUS: OK'; then
	ok "report mencetak tabel metrik + status"
else
	no "report tabel tidak sesuai"
fi
if fake_env "$state" "$work" GUARD_FAKE_DF=36 "$GUARD" report --json >"$last_out" 2>&1 &&
	contains "$last_out" '"disk_percent":36' && contains "$last_out" '"ok":true'; then
	ok "report --json memuat nilai dan status"
else
	no "report --json tidak sesuai"
fi

scenario "watch memanggil cleanup di 57,5%"
state=$work/state-cleanup; tmp=$work/tmp-cleanup; mkdir -p "$state" "$tmp"
if fake_env "$state" "$tmp" GUARD_FAKE_DF=57.5 GUARD_DRY_RUN=1 "$GUARD" watch --once >"$last_out" 2>&1 &&
	contains "$last_out" 'disk 57.5% > 57% — menjalankan cleanup' &&
	contains "$last_out" 'dibebaskan:'; then
	ok "watch 57,5% menjalankan cleanup (dry-run, tanpa hapus nyata)"
else
	no "watch 57,5% tidak menjalankan cleanup"
fi

scenario "watch menulis flag di 60,1% dan mencabutnya di 54%"
state=$work/state-flag; tmp=$work/tmp-flag; mkdir -p "$state" "$tmp"
if fake_env "$state" "$tmp" GUARD_FAKE_DF=60.1 "$GUARD" watch --once >"$last_out" 2>&1 &&
	[[ -s $state/PAUSE_HEAVY ]] && contains "$last_out" 'PAUSE_HEAVY ditulis'; then
	ok "flag PAUSE_HEAVY dibuat di 60,1%"
else
	no "flag PAUSE_HEAVY tidak dibuat di 60,1%"
fi
if fake_env "$state" "$tmp" GUARD_FAKE_DF=54 "$GUARD" watch --once >"$last_out" 2>&1 &&
	[[ ! -e $state/PAUSE_HEAVY ]] && contains "$last_out" 'PAUSE_HEAVY dicabut'; then
	ok "flag PAUSE_HEAVY dicabut di 54%"
else
	no "flag PAUSE_HEAVY tidak dicabut di 54%"
fi

scenario "cleanup fixture: hanya stale, tanpa fd, di tmp sendiri"
state=$work/state-fixture; tmp=$work/fixture-tmp; mkdir -p "$state" "$tmp"/{go-build-stale,gocache-stale,go-build-fresh,keep,go-build-used}
touch -d '2 hours ago' "$tmp/go-build-stale/marker" "$tmp/gocache-stale/marker"
touch -d '2 hours ago' "$tmp/go-build-stale" "$tmp/gocache-stale"
bash -c 'exec 7>>"$1/held"; sleep 5' _ "$tmp/go-build-used" &
bg_pid=$!
for _ in $(seq 50); do [[ -e $tmp/go-build-used/held ]] && break; sleep 0.05; done
touch -d '2 hours ago' "$tmp/go-build-used"
if fake_env "$state" "$tmp" GUARD_FAKE_DF=50 "$GUARD" cleanup >"$last_out" 2>&1 &&
	[[ ! -e $tmp/go-build-stale ]] && [[ ! -e $tmp/gocache-stale ]] &&
	[[ -e $tmp/go-build-fresh && -e $tmp/keep && -e $tmp/go-build-used ]] &&
	contains "$last_out" 'fd aktif'; then
	ok "cleanup menghapus stale saja, melewati yang muda/ber-fd, mencetak byte"
else
	no "cleanup fixture tidak sesuai"
fi
kill "$bg_pid" 2>/dev/null || true
wait "$bg_pid" 2>/dev/null || true
bg_pid=

scenario "agent-heavy menolak saat guard gagal"
state=$work/state-heavy; tmp=$work/tmp-heavy; mkdir -p "$state" "$tmp"
if ! fake_env "$state" "$tmp" GUARD_FAKE_DF=58 "$HEAVY" -- true >"$last_out" 2>&1 &&
	contains "$last_out" 'check gagal'; then
	ok "agent-heavy menolak di disk 58%"
else
	no "agent-heavy seharusnya menolak saat guard gagal"
fi

scenario "agent-heavy menolak saat flag PAUSE_HEAVY ada"
state=$work/state-pause; tmp=$work/tmp-pause; mkdir -p "$state" "$tmp"
printf 'pause\n' >"$state/PAUSE_HEAVY"
if ! fake_env "$state" "$tmp" GUARD_FAKE_DF=36 "$HEAVY" -- true >"$last_out" 2>&1 &&
	contains "$last_out" 'PAUSE_HEAVY aktif'; then
	ok "agent-heavy menolak tanpa --force saat flag ada"
else
	no "agent-heavy seharusnya menolak saat flag PAUSE_HEAVY ada"
fi
if fake_env "$state" "$tmp" GUARD_FAKE_DF=36 "$HEAVY" --force -- true >"$last_out" 2>&1; then
	ok "agent-heavy --force melewati flag PAUSE_HEAVY"
else
	no "agent-heavy --force seharusnya berjalan"
fi
rm -f "$state/PAUSE_HEAVY"

scenario "agent-heavy memasang limit 60%"
if fake_env "$state" "$tmp" "$HEAVY" --force -- bash -c \
	'printf "%s|%s|%s|%s|%s\n" "$GOMAXPROCS" "$GOFLAGS" "$GOMEMLIMIT" "$(ulimit -v)" "$(nproc)"' \
	>"$last_out" 2>"$work/heavy-err" &&
	contains "$last_out" '2|-p=1|1500MiB|4194304|6'; then
	ok "GOMAXPROCS=2, GOFLAGS=-p=1, GOMEMLIMIT=1500MiB, ulimit-v=4 GiB, 6 CPU"
else
	no "limit agent-heavy tidak sesuai"
fi

scenario "flock mengantre: proses kedua menunggu lalu berhasil"
state=$work/state-lock; tmp=$work/tmp-lock; mkdir -p "$state" "$tmp"
fake_env "$state" "$tmp" "$HEAVY" --force -- sleep 1 >"$work/first-out" 2>&1 &
bg_pid=$!
locked=0
for _ in $(seq 100); do
	if ! flock -n "$state/heavy.lock" -c true 2>/dev/null; then
		locked=1
		break
	fi
	sleep 0.05
done
if ((locked)) && kill -0 "$bg_pid" 2>/dev/null &&
	fake_env "$state" "$tmp" HEAVY_WAIT=5 "$HEAVY" --force -- bash -c 'echo SECOND-RAN' \
		>"$last_out" 2>"$work/second-err" &&
	contains "$last_out" 'SECOND-RAN' && contains "$work/second-err" 'menunggu hingga 5s'; then
	ok "proses kedua mengantre dengan HEAVY_WAIT lalu berhasil"
else
	no "proses kedua tidak mengantre/berhasil"
fi
kill "$bg_pid" 2>/dev/null || true
wait "$bg_pid" 2>/dev/null || true
bg_pid=

scenario "flock timeout saat pemegang menahan"
state=$work/state-lock-timeout; tmp=$work/tmp-lock-timeout; mkdir -p "$state" "$tmp"
fake_env "$state" "$tmp" "$HEAVY" --force -- sleep 5 >"$work/first-out" 2>&1 &
bg_pid=$!
locked=0
for _ in $(seq 100); do
	if ! flock -n "$state/heavy.lock" -c true 2>/dev/null; then
		locked=1
		break
	fi
	sleep 0.05
done
if ((locked)) &&
	! fake_env "$state" "$tmp" "$HEAVY" --force --wait 1 -- true >"$last_out" 2>"$work/timeout-err" &&
	contains "$work/timeout-err" 'timeout 1s menunggu lock' &&
	contains "$work/timeout-err" 'pemegang lock tidak diketahui'; then
	ok "HEAVY_WAIT/--wait timeout -> exit 1 dengan pesan jelas"
else
	no "timeout lock tidak sesuai"
fi
kill "$bg_pid" 2>/dev/null || true
wait "$bg_pid" 2>/dev/null || true
bg_pid=

printf '\nHASIL: %d lulus, %d gagal\n' "$pass" "$fail"
((fail == 0))
