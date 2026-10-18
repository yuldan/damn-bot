#!/usr/bin/env bash
# Gate meta PR (ENGINEERING §7.2, ADR-073, ADR-078 AUD-056, ADR-091 NEW2-26, ADR-094).
# Pemakaian: BASE=<sha> [HEAD=<sha>] PR_TITLE=… PR_BODY=… PR_LABELS="a,b" PR_APPROVALS=<n> scripts/pr-meta.sh [cek...]
# Cek (default semua): title (E-80), changelog (E-81), adrgate (E-82), size (E-84), spec-guard (SPEC §20).
# PR_APPROVALS = jumlah reviewer berbeda yang status review terakhirnya APPROVED (workflow mengisinya
# dari API GitHub). Setiap pelanggaran mencetak "PR-META GAGAL <cek>: …"; exit 1 bila ada.
set -uo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
: "${BASE:?BASE = commit dasar PR}"
HEAD=${HEAD:-HEAD}
PR_TITLE=${PR_TITLE:-}
PR_BODY=${PR_BODY:-}
PR_APPROVALS=${PR_APPROVALS:-0}
labels=",${PR_LABELS:-},"
labels=${labels// /}
CHECKS=(title changelog adrgate size spec-guard)
[[ $# -gt 0 ]] && CHECKS=("$@")
SIZE_MAX=600

changed=$(git diff --name-only "$BASE" "$HEAD")
fail=0
bad() { echo "PR-META GAGAL $1: $2"; fail=1; }
has_label() { [[ $labels == *",$1,"* ]]; }
touched() { grep -qxF "$1" <<<"$changed"; }
type=$(sed -nE 's/^([a-z]+)(\(.*\))?!?: .*/\1/p' <<<"$PR_TITLE")

# section_changed <file> <heading-ERE>: apakah diff menyentuh bagian Markdown yang judulnya cocok
# (sampai judul berikutnya dengan level sama atau lebih tinggi), di sisi lama atau sisi baru.
section_range() { # <rev> <file> <ERE> → "awal akhir" (inklusif) atau kosong
	git show "$1:$2" 2>/dev/null | awk -v re="$3" '
		/^#+ / { lvl = index($0, " ") - 1
			if (start && lvl <= slvl) { print start, NR - 1; found = 1; exit }
			if (!start && $0 ~ re) { start = NR; slvl = lvl } }
		END { if (start && !found) print start, NR }'
}
section_changed() { # <file> <ERE>
	local file=$1 re=$2 old new
	touched "$file" || return 1
	old=$(section_range "$BASE" "$file" "$re")
	new=$(section_range "$HEAD" "$file" "$re")
	# Diff ditampung dulu: awk keluar lebih awal, dan SIGPIPE pada git tidak boleh menjadi status (pipefail).
	local diff
	diff=$(git diff -U0 "$BASE" "$HEAD" -- "$file")
	awk -v old="$old" -v new="$new" '
		function hit(r, a, n,   s) { if (r == "") return 0; split(r, s, " ")
			if (n == 0) return (a >= s[1] && a <= s[2] + 1) # hapus murni: titik sisip
			return (a <= s[2] && a + n - 1 >= s[1]) }
		/^@@ / { split($2, o, ","); split($3, w, ","); oa = substr(o[1], 2); na = substr(w[1], 2)
			on = (2 in o) ? o[2] : 1; nn = (2 in w) ? w[2] : 1
			if (hit(old, oa, on) || hit(new, na, nn)) { found = 1; exit } }
		END { exit !found }' <<<"$diff"
}

check_title() { # E-80
	local re='^(feat|fix|perf|refactor|test|docs|build|ci|chore)(\([a-z0-9/-]+\))?!?: .{1,72}$'
	grep -qE "$re" <<<"$PR_TITLE" || bad title "judul \"$PR_TITLE\" bukan Conventional Commits (E-80)"
}

check_changelog() { # E-81
	grep -E '^(cmd|internal)/.*\.go$' <<<"$changed" | grep -v '_test\.go$' >/dev/null || return 0
	if [[ $type =~ ^(refactor|test|ci|build)$ ]] && has_label no-changelog; then return 0; fi
	section_changed CHANGELOG.md '^## Unreleased' ||
		bad changelog "PR menyentuh .go non-test di cmd/ atau internal/ tanpa mengubah CHANGELOG.md bagian Unreleased (E-81)"
}

check_adrgate() { # E-82 + NEW2-26
	local why=()
	touched deps.allow && why+=(deps.allow)
	touched loc.budget && why+=(loc.budget)
	grep -qE '^internal/config/defaults[^/]*\.go$' <<<"$changed" && why+=("internal/config/defaults*.go")
	section_changed SPEC.md '^## 2\. ' && why+=("SPEC §2")
	section_changed SPEC.md '^#+ 3\.2\.4 ' && why+=("SPEC §3.2.4")
	section_changed SPEC.md '^## 17\. ' && why+=("SPEC §17")
	section_changed docs/ENGINEERING.md '^### 2\.4 ' && why+=("ENGINEERING §2.4")
	((${#why[@]})) || return 0
	touched docs/DECISIONS.md || bad adrgate "${why[*]} berubah tanpa perubahan docs/DECISIONS.md (E-82)"
}

check_size() { # E-84 (ADR-094: baris Go non-test, non-generated, ditambah + dihapus)
	local n=0 a d f tmp
	tmp=$(mktemp)
	while read -r a d f; do
		[[ $f == *.go && $f != *_test.go && $a != - ]] || continue
		git show "$HEAD:$f" 2>/dev/null >"$tmp" || true
		# Header generated Go: baris "// Code generated … DO NOT EDIT." sebelum klausa package.
		awk '/^package /{exit} /^\/\/ Code generated .* DO NOT EDIT\.$/{g=1} END{exit !g}' "$tmp" && continue
		n=$((n + a + d))
	done < <(git diff --numstat "$BASE" "$HEAD")
	rm -f "$tmp"
	echo "pr-meta size: $n baris Go non-test diubah (maks $SIZE_MAX)"
	((n <= SIZE_MAX)) && return 0
	has_label large-change && ((PR_APPROVALS >= 2)) && return 0
	bad size "$n > $SIZE_MAX baris Go non-test; perlu label large-change + 2 approver (E-84)"
}

# Dokumen yang tidak dipantau spec-guard: entri seluruh-file di testdata/docs_exclusions.txt
# (baris tanpa '#…' anker; pola glob diizinkan, AUD-056).
excluded_doc() {
	local pat
	while read -r pat; do
		# shellcheck disable=SC2053 # $pat sengaja glob
		[[ $1 == $pat ]] && return 0
	done < <(sed -E 's/[[:space:]]+#.*$//; /^#/d; /^[[:space:]]*$/d; /#/d' testdata/docs_exclusions.txt 2>/dev/null)
	return 1
}

check_spec_guard() { # SPEC §20 + AUD-056
	local f docs=()
	while read -r f; do
		[[ $f == SPEC.md || $f =~ ^docs/[^/]+\.md$ ]] || continue
		[[ $f == docs/DECISIONS.md ]] && continue
		excluded_doc "$f" || docs+=("$f")
	done <<<"$changed"
	((${#docs[@]})) || return 0
	has_label spec-typo && ((PR_APPROVALS >= 1)) && return 0
	grep -qE 'ADR-[0-9]{3}' <<<"$PR_BODY" && touched docs/DECISIONS.md && return 0
	bad spec-guard "${docs[*]} berubah: deskripsi PR wajib merujuk ADR-NNN dan docs/DECISIONS.md wajib berubah (atau label spec-typo + 1 approver)"
}

for c in "${CHECKS[@]}"; do "check_${c//-/_}"; done
((fail)) || echo "pr-meta: ${CHECKS[*]} ok"
exit $fail
