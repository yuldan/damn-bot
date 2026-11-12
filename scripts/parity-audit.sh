#!/usr/bin/env bash
# parity-audit.sh — audit read-only docs/FEATURE-PARITY.md.
#
# Pemakaian: scripts/parity-audit.sh [docs/FEATURE-PARITY.md]
#
# Mencetak: (1) hitungan status seluruh 854 baris, (2) hitungan planned per
# fase, (3) hitungan planned per prefix, (4) daftar lengkap baris planned,
# (5) status pin 121 deferred, dan (6) residual 63 (planned F1–F5 di luar pin).
# Tidak menulis file dan tidak butuh jaringan. Exit 1 bila dokumen tidak ada.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
doc=${1:-"$root/docs/FEATURE-PARITY.md"}

if [[ ! -f "$doc" ]]; then
	echo "parity-audit: dokumen tidak ditemukan: $doc" >&2
	exit 1
fi

# Pin 121 deferred (ADR-349). Harus sama dengan
# internal/archtest/f9_parity_deferred_test.go; script hanya membaca.
PINNED121="
AUTO-021 AUTO-022 AUTO-024 CLI-003 CLI-004 CLI-005 CLI-007 CLI-008 CLI-010
CLI-013 CLI-020 CLI-021 CLI-022 CLI-033 CLI-034 CLI-035 CLI-038 CLI-043
CLI-044 CLI-053 CLI-062 CLI-064 CLI-071 CLI-072 CLI-075 CORE-006 CORE-010
CORE-016 CORE-017 EXEC-008 EXEC-023 EXEC-025 EXEC-026 EXEC-027 EXEC-028
EXEC-029 EXEC-030 EXEC-031 EXEC-032 MEM-017 MEM-022 MEM-036 OPS-005 OPS-012
OPS-017 OPS-025 PLUG-001 PLUG-002 PLUG-003 PLUG-004 PLUG-005 PLUG-006
PLUG-007 PLUG-008 PLUG-033 PROV-055 PROV-056 PROV-058 PROV-059 PROV-060
PROV-061 PROV-077 PROV-079 SKILL-003 SKILL-005 SKILL-006 SLASH-004 SLASH-005
SLASH-006 SLASH-007 SLASH-008 SLASH-009 SLASH-011 SLASH-014 SLASH-016
SLASH-017 SLASH-019 SLASH-024 SLASH-025 SLASH-026 SLASH-032 SLASH-033
SLASH-036 SLASH-039 SLASH-042 SLASH-044 SLASH-045 SLASH-046 SLASH-048
SLASH-050 SLASH-051 SLASH-054 SLASH-055 SLASH-057 SLASH-058 SLASH-059
SLASH-060 SLASH-062 SLASH-066 SLASH-067 SLASH-068 SLASH-069 SLASH-070
SLASH-071 SLASH-074 SLASH-083 SLASH-085 SLASH-086 SLASH-087 SLASH-088
SLASH-097 SLASH-100 SLASH-101 SLASH-103 TSET-008 TSET-013 UI-009 UI-013
UI-025 UI-027 UI-028
"

awk -v pinned="$PINNED121" '
function trim(s) { gsub(/^[ \t]+|[ \t]+$/, "", s); return s }
function is_pinned(id,   n, a, i) {
	n = split(pinned, a, " ")
	for (i = 1; i <= n; i++) if (a[i] == id) return 1
	return 0
}

/^\| [A-Z]+-[0-9][0-9][0-9] \|/ {
	line = $0
	sub(/\|[ \t]*$/, "", line)
	n = split(line, cell, "|")
	if (n < 4) next
	id = trim(cell[2])
	status = trim(cell[n])
	fase = trim(cell[n - 1])
	fitur = trim(cell[3])
	dash = index(id, "-")
	prefix = dash > 0 ? substr(id, 1, dash - 1) : id
	total++
	if (index(status, "dropped-with-approval") == 1) dropped++
	else if (status == "LULUS") lulus++
	else if (status == "planned") planned++
	else other++
	if (is_pinned(id)) {
		pinTotal++
		if (status == "planned") pinPlanned++
	}
	if (status == "planned") {
		byPhase[fase]++
		byPrefix[prefix]++
		plannedRows[planned] = sprintf("%-12s %-3s %s", id, fase, fitur)
		if (fase ~ /^F[1-5]$/ && !is_pinned(id)) {
			residual++
			byResidual[prefix]++
			residualRows[residual] = sprintf("%-12s %-3s %s", id, fase, fitur)
		}
	}
}

END {
	printf "Status: total=%d planned=%d LULUS=%d dropped=%d other=%d\n", \
		total, planned, lulus, dropped, other

	print ""
	print "Planned per fase:"
	for (f in byPhase) printf "  %-3s %d\n", f, byPhase[f]
	print ""
	print "Planned per prefix:"
	for (p in byPrefix) printf "  %-7s %d\n", p, byPrefix[p]

	print ""
	printf "Pinned121: total=%d planned=%d non-planned=%d\n", pinTotal, pinPlanned, pinTotal - pinPlanned
	printf "Residual63: planned=%d (F1–F5 planned di luar pin 121)\n", residual
	print "Residual63 per prefix:"
	for (p in byResidual) printf "  %-7s %d\n", p, byResidual[p]

	print ""
	print "Planned rows:"
	for (i = 1; i <= planned; i++) print "  " plannedRows[i]
	print ""
	print "Residual63 rows:"
	for (i = 1; i <= residual; i++) print "  " residualRows[i]

	if (pinTotal != 121) print "WARN: pin berubah " pinTotal " (harus 121)"
	if (residual != 63) print "WARN: residual berubah " residual " (harus 63 sampai owner memutuskan)"
}
' "$doc"
