#!/usr/bin/env bash
# Resource guard 60% untuk host bersama (ENGINEERING §5.5, ADR-341).
#
# Aturan owner: total CPU/RAM/disk pekerjaan agent ≤ 60% host. Alat ini adalah gerbang
# operasional, bukan pengganti limit kernel:
#   check    gagal bila disk / > 57% (margin 3 pp), MemAvailable < 41% MemTotal
#            (fallback 9 GiB bila MemTotal tak terbaca), atau load1 > 5.5.
#   report   tabel metrik + status; --json untuk konsumen mesin.
#   cleanup  hapus HANYA: `go clean -cache` (bila disk > 57%), direktori /tmp/go-build* dan
#            /tmp/gocache-* yang berumur > 1 jam dan tidak sedang dibuka proses, serta
#            `git worktree prune`. Tidak menyentuh ~/.cache/damnbot, worktree dirty,
#            ~/go/pkg/mod, atau source. Mencetak byte yang dibebaskan.
#   watch    loop 120 dtk: disk > 57% → cleanup; disk > 60% → tulis flag PAUSE_HEAVY;
#            disk < 55% → hapus flag. `--once` menjalankan satu iterasi (dipakai tes).
#
# Semua ambang tetap (57/60/55%, 41% RAM, 5.5) agar aturan owner tidak bisa dilonggarkan
# diam-diam lewat environment. Untuk tes, nilai pengukuran dapat dipalsukan:
#   GUARD_FAKE_DF=57.5          # persen disk, atau output `df -P` mentah
#   GUARD_FAKE_MEMINFO=21153700 # KiB MemAvailable, atau isi /proc/meminfo
#   GUARD_FAKE_MEMTOTAL=24641536 # KiB MemTotal, atau isi /proc/meminfo (0 = tak terbaca)
#   GUARD_FAKE_LOADAVG=0.63     # load1, atau isi /proc/loadavg
# Seam lain (hanya untuk tes/operasi aman): GUARD_STATE_DIR, GUARD_TMPDIR, GUARD_REPO_ROOT,
# GUARD_DRY_RUN=1, GUARD_INTERVAL.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
STATE_DIR=${GUARD_STATE_DIR:-${HOME}/.cache/damnbot}
PAUSE_FLAG=$STATE_DIR/PAUSE_HEAVY
TMP_ROOT=${GUARD_TMPDIR:-/tmp}
REPO_ROOT=${GUARD_REPO_ROOT:-$ROOT}
DRY_RUN=${GUARD_DRY_RUN:-0}
WATCH_INTERVAL=${GUARD_INTERVAL:-120}

DISK_LIMIT=57
DISK_PAUSE=60
DISK_RESUME=55
MEM_AVAIL_PCT=41
MEM_FALLBACK_KIB=$((9 * 1024 * 1024))
LOAD_LIMIT=5.5

die() {
	echo "agent-guard: $*" >&2
	exit 2
}

usage() {
	cat <<'EOF'
Pemakaian: scripts/agent-guard.sh <check|report|cleanup|watch> [opsi]

  check             exit 1 bila disk / > 57%, MemAvailable < 41% MemTotal (fallback 9 GiB), atau load1 > 5.5
  report [--json]   cetak metrik + status (selalu exit 0)
  cleanup           bersihkan cache build sementara yang aman (lihat header)
  watch [--once]    loop 120 dtk; --once = satu iterasi untuk tes
EOF
}

# disk_pct: persen terpakai untuk /. GUARD_FAKE_DF boleh berupa angka atau output df mentah.
disk_pct() {
	if [[ -n ${GUARD_FAKE_DF:-} ]]; then
		if [[ $GUARD_FAKE_DF =~ ^[0-9]+([.][0-9]+)?$ ]]; then
			printf '%s\n' "$GUARD_FAKE_DF"
		else
			awk 'NF >= 5 && $1 != "Filesystem" { v = $5; sub(/%$/, "", v); last = v } END { if (last == "") exit 1; print last }' <<<"$GUARD_FAKE_DF"
		fi
		return
	fi
	df -P / | awk 'NR > 1 { v = $5; sub(/%$/, "", v); last = v } END { if (last == "") exit 1; print last }'
}

# mem_available_kib: MemAvailable dalam KiB.
mem_available_kib() {
	if [[ -n ${GUARD_FAKE_MEMINFO:-} ]]; then
		if [[ $GUARD_FAKE_MEMINFO =~ ^[0-9]+$ ]]; then
			printf '%s\n' "$GUARD_FAKE_MEMINFO"
		else
			awk '/^MemAvailable:/ { print $2; found = 1; exit } END { if (!found) exit 1 }' <<<"$GUARD_FAKE_MEMINFO"
		fi
		return
	fi
	awk '/^MemAvailable:/ { print $2; found = 1; exit } END { if (!found) exit 1 }' /proc/meminfo
}

# mem_total_kib: MemTotal dalam KiB; 0 berarti tidak terbaca (pemanggil memakai fallback).
mem_total_kib() {
	if [[ -n ${GUARD_FAKE_MEMTOTAL:-} ]]; then
		if [[ $GUARD_FAKE_MEMTOTAL =~ ^[0-9]+$ ]]; then
			printf '%s\n' "$GUARD_FAKE_MEMTOTAL"
		else
			awk '/^MemTotal:/ { print $2; found = 1; exit } END { if (!found) exit 1 }' <<<"$GUARD_FAKE_MEMTOTAL"
		fi
		return
	fi
	if [[ -n ${GUARD_FAKE_MEMINFO:-} && $GUARD_FAKE_MEMINFO == *MemTotal:* ]]; then
		awk '/^MemTotal:/ { print $2; found = 1; exit } END { if (!found) exit 1 }' <<<"$GUARD_FAKE_MEMINFO"
		return
	fi
	awk '/^MemTotal:/ { print $2; found = 1; exit } END { if (!found) exit 1 }' /proc/meminfo
}

# mem_min_kib <total_kib>: batas MemAvailable (41% total; fallback 9 GiB bila total 0).
mem_min_kib() {
	local total=${1:-0}
	if [[ $total =~ ^[0-9]+$ ]] && ((total > 0)); then
		awk -v t="$total" 'BEGIN { printf "%d\n", int(t * 41 / 100) }'
	else
		printf '%s\n' "$MEM_FALLBACK_KIB"
	fi
}

# mem_used_pct <total_kib> <available_kib>: persen RAM terpakai (n/a bila total 0).
mem_used_pct() {
	awk -v t="${1:-0}" -v a="${2:-0}" 'BEGIN { if (t > 0) printf "%.1f", (t - a) * 100 / t; else printf "n/a" }'
}

# load1: rata-rata beban 1 menit.
load1() {
	if [[ -n ${GUARD_FAKE_LOADAVG:-} ]]; then
		awk '{ print $1; exit }' <<<"$GUARD_FAKE_LOADAVG"
		return
	fi
	awk '{ print $1; exit }' /proc/loadavg
}

gt() { awk -v a="$1" -v b="$2" 'BEGIN { exit !(a > b) }'; }
lt() { awk -v a="$1" -v b="$2" 'BEGIN { exit !(a < b) }'; }

status_disk() { gt "$1" "$DISK_LIMIT" && echo GAGAL || echo ok; }
status_mem() { lt "$1" "$2" && echo GAGAL || echo ok; }
status_load() { gt "$1" "$LOAD_LIMIT" && echo GAGAL || echo ok; }

cmd_check() {
	local disk mem_kib mem_total load fail=0 mem_gib mem_min mem_gib_min used used_disp mem_rule
	disk=$(disk_pct)
	mem_kib=$(mem_available_kib)
	mem_total=$(mem_total_kib || true)
	load=$(load1)
	mem_gib=$(awk -v k="$mem_kib" 'BEGIN { printf "%.2f", k / 1048576 }')
	mem_min=$(mem_min_kib "$mem_total")
	mem_gib_min=$(awk -v k="$mem_min" 'BEGIN { printf "%.2f", k / 1048576 }')
	used=$(mem_used_pct "$mem_total" "$mem_kib")
	if [[ $used == n/a ]]; then
		used_disp=n/a
	else
		used_disp="${used}%"
	fi
	if [[ $mem_total =~ ^[0-9]+$ ]] && ((mem_total > 0)); then
		mem_rule="< 41% total = ${mem_gib_min} GiB"
	else
		mem_rule="< fallback 9 GiB (MemTotal tak terbaca)"
	fi

	printf 'disk /        : %s%% (gagal bila > %s%%)\n' "$disk" "$DISK_LIMIT"
	printf 'MemAvailable  : %s GiB = %s KiB (used %s; gagal bila %s)\n' "$mem_gib" "$mem_kib" "$used_disp" "$mem_rule"
	printf 'load1         : %s (gagal bila > %s)\n' "$load" "$LOAD_LIMIT"

	if gt "$disk" "$DISK_LIMIT"; then
		echo "GUARD GAGAL disk: ${disk}% > ${DISK_LIMIT}%" >&2
		fail=1
	fi
	if lt "$mem_kib" "$mem_min"; then
		if [[ $mem_total =~ ^[0-9]+$ ]] && ((mem_total > 0)); then
			echo "GUARD GAGAL memory: avail ${mem_gib} GiB (used ${used}%) < 41% total (${mem_gib_min} GiB)" >&2
		else
			echo "GUARD GAGAL memory: avail ${mem_gib} GiB < fallback 9 GiB (MemTotal tak terbaca)" >&2
		fi
		fail=1
	fi
	if gt "$load" "$LOAD_LIMIT"; then
		echo "GUARD GAGAL load: ${load} > ${LOAD_LIMIT}" >&2
		fail=1
	fi
	if ((fail)); then
		return 1
	fi
	echo "GUARD OK: resource di bawah ambang 60%"
}

cmd_report() {
	local json=0 disk mem_kib mem_total load mem_gib ok=true flag=false
	local mem_min mem_gib_min used used_disp used_json mem_rule mem_basis
	if [[ ${1:-} == --json ]]; then
		json=1
	elif [[ -n ${1:-} ]]; then
		die "opsi report tidak dikenal: $1"
	fi
	disk=$(disk_pct)
	mem_kib=$(mem_available_kib)
	mem_total=$(mem_total_kib || true)
	load=$(load1)
	mem_gib=$(awk -v k="$mem_kib" 'BEGIN { printf "%.2f", k / 1048576 }')
	mem_min=$(mem_min_kib "$mem_total")
	mem_gib_min=$(awk -v k="$mem_min" 'BEGIN { printf "%.2f", k / 1048576 }')
	used=$(mem_used_pct "$mem_total" "$mem_kib")
	if [[ $used == n/a ]]; then
		used_disp=n/a
	else
		used_disp="${used}%"
	fi
	if [[ $mem_total =~ ^[0-9]+$ ]] && ((mem_total > 0)); then
		mem_rule="avail <41% total (${mem_gib_min} GiB)"
		mem_basis=41pct_total
		used_json=$used
	else
		mem_rule="avail <9 GiB fallback"
		mem_basis=fallback_9GiB
		mem_total=0
		used_json=null
	fi
	if [[ $(status_disk "$disk") == GAGAL || $(status_mem "$mem_kib" "$mem_min") == GAGAL || $(status_load "$load") == GAGAL ]]; then
		ok=false
	fi
	if [[ -e $PAUSE_FLAG ]]; then
		flag=true
	fi

	if ((json)); then
		printf '{"disk_percent":%s,"disk_limit_percent":%s,"mem_total_kib":%s,"mem_available_kib":%s,"mem_used_percent":%s,"mem_min_kib":%s,"mem_min_basis":"%s","load1":%s,"load_limit":%s,"ok":%s,"pause_flag":%s}\n' \
			"$disk" "$DISK_LIMIT" "$mem_total" "$mem_kib" "$used_json" "$mem_min" "$mem_basis" "$load" "$LOAD_LIMIT" "$ok" "$flag"
		return 0
	fi

	printf '%-16s %-22s %-22s %s\n' METRIK NILAI AMBANG STATUS
	printf '%-16s %-22s %-22s %s\n' 'disk /' "${disk}%" ">${DISK_LIMIT}% gagal" "$(status_disk "$disk")"
	printf '%-16s %-22s %-22s %s\n' MemAvailable "${mem_gib} GiB (used ${used_disp})" "$mem_rule" "$(status_mem "$mem_kib" "$mem_min")"
	printf '%-16s %-22s %-22s %s\n' load1 "$load" ">${LOAD_LIMIT} gagal" "$(status_load "$load")"
	printf '%-16s %-22s %-22s %s\n' PAUSE_HEAVY "$([[ $flag == true ]] && echo ada || echo tidak-ada)" "disk > ${DISK_PAUSE}%" "$([[ $flag == true ]] && echo pause || echo ok)"
	if [[ $ok == true ]]; then
		echo "STATUS: OK (di bawah ambang 60%)"
	else
		echo "STATUS: GAGAL (minimal satu metrik melewati ambang)"
	fi
}

dir_kib() {
	local kib
	kib=$(du -sk -- "$1" 2>/dev/null | awk 'NR == 1 { print $1 }') || kib=0
	printf '%s\n' "${kib:-0}"
}

# in_use <path>: ada proses dengan fd menunjuk ke path atau isinya.
in_use() {
	local path=$1 hit
	hit=$(find /proc/[0-9]*/fd -lname "${path}*" -print -quit 2>/dev/null || true)
	[[ -n $hit ]]
}

cmd_cleanup() {
	local dry=$DRY_RUN freed_kib=0 disk cache before after kib dir
	disk=$(disk_pct)

	if gt "$disk" "$DISK_LIMIT"; then
		if command -v go >/dev/null 2>&1; then
			cache=$(go env GOCACHE 2>/dev/null || true)
			before=0
			if [[ -n $cache && -d $cache ]]; then
				before=$(dir_kib "$cache")
			fi
			if [[ $dry == 1 ]]; then
				echo "cleanup: (dry-run) go clean -cache (disk ${disk}% > ${DISK_LIMIT}%)"
			else
				echo "cleanup: go clean -cache (disk ${disk}% > ${DISK_LIMIT}%)"
				go clean -cache || echo "cleanup: go clean -cache gagal (dilewati)" >&2
				after=0
				if [[ -n $cache && -d $cache ]]; then
					after=$(dir_kib "$cache")
				fi
				if ((before > after)); then
					freed_kib=$((before - after))
				fi
			fi
		else
			echo "cleanup: go tidak ditemukan, lewati go clean -cache" >&2
		fi
	else
		echo "cleanup: disk ${disk}% <= ${DISK_LIMIT}%, go clean -cache tidak dijalankan"
	fi

	# Hanya dua pola cache build sementara; source, ~/.cache/damnbot, dan ~/go/pkg/mod
	# tidak pernah menjadi kandidat.
	while IFS= read -r -d '' dir; do
		if [[ ! -O $dir ]]; then
			echo "cleanup: lewati (bukan milik user): $dir"
			continue
		fi
		if in_use "$dir"; then
			echo "cleanup: lewati (fd aktif): $dir"
			continue
		fi
		kib=$(dir_kib "$dir")
		if [[ $dry == 1 ]]; then
			echo "cleanup: (dry-run) hapus stale $dir (${kib} KiB)"
		elif rm -rf -- "$dir"; then
			freed_kib=$((freed_kib + kib))
			echo "cleanup: hapus stale $dir (${kib} KiB)"
		else
			echo "cleanup: gagal hapus $dir (dilewati)" >&2
		fi
	done < <(find "$TMP_ROOT" -mindepth 1 -maxdepth 1 -type d \
		\( -name 'go-build*' -o -name 'gocache-*' \) -mmin +60 -print0 2>/dev/null)

	if git -C "$REPO_ROOT" rev-parse --git-dir >/dev/null 2>&1; then
		if [[ $dry == 1 ]]; then
			echo "cleanup: (dry-run) git worktree prune"
		else
			echo "cleanup: git worktree prune"
			git -C "$REPO_ROOT" worktree prune || echo "cleanup: git worktree prune gagal (dilewati)" >&2
		fi
	fi

	printf 'dibebaskan: %s B (%s MiB)\n' "$((freed_kib * 1024))" "$((freed_kib / 1024))"
}

cmd_watch() {
	local once=0 disk
	if [[ ${1:-} == --once ]]; then
		once=1
	elif [[ -n ${1:-} ]]; then
		die "opsi watch tidak dikenal: $1"
	fi

	while :; do
		disk=$(disk_pct)
		if gt "$disk" "$DISK_LIMIT"; then
			echo "watch: disk ${disk}% > ${DISK_LIMIT}% — menjalankan cleanup"
			cmd_cleanup || echo "watch: cleanup gagal, lanjut" >&2
		fi
		if gt "$disk" "$DISK_PAUSE"; then
			mkdir -p "$STATE_DIR"
			printf 'PAUSE_HEAVY\nwaktu=%s\ndisk=%s%%\nalasan=disk di atas %s%% (batas owner 60%%)\n' \
				"$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$disk" "$DISK_PAUSE" >"$PAUSE_FLAG.tmp"
			mv -f -- "$PAUSE_FLAG.tmp" "$PAUSE_FLAG"
			echo "watch: GUARD PERINGATAN disk ${disk}% > ${DISK_PAUSE}% — PAUSE_HEAVY ditulis ($PAUSE_FLAG)"
		elif lt "$disk" "$DISK_RESUME"; then
			if [[ -e $PAUSE_FLAG ]]; then
				rm -f -- "$PAUSE_FLAG"
				echo "watch: disk ${disk}% < ${DISK_RESUME}% — PAUSE_HEAVY dicabut"
			fi
		fi
		if ((once)); then
			return 0
		fi
		sleep "$WATCH_INTERVAL"
	done
}

main() {
	local cmd=${1:-}
	(($#)) || {
		usage >&2
		exit 2
	}
	shift
	case $cmd in
	check)
		(($# == 0)) || die "check tidak menerima opsi"
		cmd_check
		;;
	report) cmd_report "${1:-}" ;;
	cleanup)
		(($# == 0)) || die "cleanup tidak menerima opsi"
		cmd_cleanup
		;;
	watch) cmd_watch "${1:-}" ;;
	help | -h | --help) usage ;;
	*) usage >&2 && exit 2 ;;
	esac
}

main "$@"
