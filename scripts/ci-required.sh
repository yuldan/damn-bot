#!/usr/bin/env bash
# Agregat gate CI (E-78, ADR-135 butir 5, ADR-324). Satu-satunya status workflow `ci` yang wajib di
# branch protection, dan namanya tidak berubah. Memeriksa himpunan gate per konteks:
#   pr    : gate murah yang tetap per-PR
#   smoke : push main (lint, static, smoke) — main tidak boleh menyerap biaya gate PR
#   full  : merge_group, nightly (workflow_call), workflow_dispatch
# Gate wajib harus `success`; gate di luar konteks harus `skipped`. Tier tak dikenal atau output
# `changes` kosong = gagal. Pemakaian: NEEDS=<json> TIER=<pr|smoke|full> CODE=<true|false>.
set -uo pipefail

JOBS=(changes lint test test-timing static deadcode deadcode-linux portability fuzz phase perf coverage e2e
	image nix bench bench-parity supply supply-chain skill-refs sqlite-engine os-matrix-qemu smoke)

check() { # <needs-json> <tier> <code>
	local needs=$1 tier=$2 code=$3
	printf '%s\n' "$needs" | jq -r 'to_entries[] | "\(.key): \(.value.result)"'
	case $tier in
	pr | smoke | full) ;;
	*)
		echo "::error::tier tidak dikenal (${tier:-kosong}): job changes gagal atau tidak menghasilkan output"
		return 1
		;;
	esac
	case $code in
	true | false) ;;
	*)
		echo "::error::output code tidak dikenal (${code:-kosong})"
		return 1
		;;
	esac

	local common="lint test static phase e2e image nix bench supply supply-chain skill-refs sqlite-engine"
	local deep="test-timing deadcode portability fuzz perf coverage bench-parity os-matrix-qemu"
	local smoke_set="lint static smoke"
	# ADR-324 amandemen: `deadcode-linux` berjalan di SETIAP tier saat `code=true`.
	local linux_dead="deadcode-linux"
	local expected=""
	if [ "$code" = true ]; then
		case $tier in
		pr) expected="$common $linux_dead" ;;
		smoke) expected="$smoke_set $linux_dead" ;;
		full) expected="$common $deep $linux_dead" ;;
		esac
	fi

	local fail=0 job result want
	for job in $(printf '%s\n' "$needs" | jq -r 'keys[]' | grep -vx ci-required); do
		result=$(printf '%s\n' "$needs" | jq -r --arg j "$job" '.[$j].result')
		if [ "$job" = changes ] || [[ " $expected " == *" $job "* ]]; then
			want=success
		else
			want=skipped
		fi
		if [ "$result" != "$want" ]; then
			echo "::error::gate $job=$result, diharapkan $want (tier=$tier, code=$code)"
			fail=1
		fi
	done
	if ((fail)); then return 1; fi
	echo "E-78: seluruh gate wajib tier $tier sukses; gate di luar tier dilewati sah"
}

# fixture <json successes>: changes sukses, semua job lain skipped, lalu timpa hasil yang diberikan.
fixture() {
	jq -cn --argjson ok "$1" '
		{changes: {result: "success"}}
		+ (["lint", "test", "test-timing", "static", "deadcode", "deadcode-linux", "portability", "fuzz", "phase",
			"perf", "coverage", "e2e", "image", "nix", "bench", "bench-parity", "supply", "supply-chain", "skill-refs",
			"sqlite-engine", "os-matrix-qemu", "smoke"] | map({key: ., value: {result: "skipped"}}) | from_entries)
		+ ($ok | with_entries(.value = {result: .value}))'
}

self_test() {
	local fail=0 n=0
	run_case() { # <name> <tier> <code> <expected-rc> <json>
		local name=$1 tier=$2 code=$3 want_rc=$4 json=$5 out rc
		n=$((n + 1))
		out=$(check "$json" "$tier" "$code" 2>&1)
		rc=$?
		if [ "$rc" -eq "$want_rc" ]; then
			echo "ok   $name"
		else
			echo "FAIL $name (exit $rc, harap $want_rc)"
			printf '%s\n' "$out" | sed 's/^/     /'
			fail=1
		fi
	}

	local pr_common deep
	pr_common='{"lint":"success","test":"success","static":"success","phase":"success","e2e":"success","image":"success","nix":"success","bench":"success","supply":"success","supply-chain":"success","skill-refs":"success","sqlite-engine":"success","deadcode-linux":"success"}'
	deep=$(printf '%s' "$pr_common" | jq -c '. + {"test-timing":"success","deadcode":"success","portability":"success","fuzz":"success","perf":"success","coverage":"success","bench-parity":"success","os-matrix-qemu":"success"}')

	run_case "pr hijau" pr true 0 "$(fixture "$pr_common")"
	# ADR-324 amandemen: deadcode linux wajib di SETIAP tier yang mengubah kode.
	run_case "pr deadcode-linux skipped" pr true 1 "$(fixture "$(printf '%s' "$pr_common" | jq -c 'del(."deadcode-linux")')")"
	# ADR-324 amandemen: matrix sqlite-engine mengecil di PR (3 leg) tetapi job/gate tetap wajib.
	run_case "sqlite-engine pr-tier success" pr true 0 "$(fixture "$pr_common")"
	run_case "sqlite-engine pr-tier skipped" pr true 1 "$(fixture "$(printf '%s' "$pr_common" | jq -c 'del(."sqlite-engine")')")"
	run_case "pr gate berat ikut jalan (harus skipped)" pr true 1 "$(fixture "$deep")"
	run_case "pr test skipped" pr true 1 "$(fixture "$(printf '%s' "$pr_common" | jq -c 'del(.test)')")"
	run_case "pr fuzz failure" pr true 1 "$(fixture "$(printf '%s' "$pr_common" | jq -c '. + {"fuzz":"failure"}')")"
	run_case "push main smoke hijau" smoke true 0 "$(fixture '{"lint":"success","static":"success","smoke":"success","deadcode-linux":"success"}')"
	run_case "push main smoke tanpa job smoke" smoke true 1 "$(fixture '{"lint":"success","static":"success","deadcode-linux":"success"}')"
	run_case "push main smoke tanpa deadcode-linux" smoke true 1 "$(fixture '{"lint":"success","static":"success","smoke":"success"}')"
	run_case "push main smoke dengan test jalan" smoke true 1 "$(fixture '{"lint":"success","static":"success","smoke":"success","deadcode-linux":"success","test":"success"}')"
	run_case "push main smoke dengan fuzz jalan" smoke true 1 "$(fixture '{"lint":"success","static":"success","smoke":"success","deadcode-linux":"success","fuzz":"success"}')"
	run_case "merge_group full hijau" full true 0 "$(fixture "$deep")"
	run_case "merge_group deadcode-linux skipped" full true 1 "$(fixture "$(printf '%s' "$deep" | jq -c 'del(."deadcode-linux")')")"
	run_case "merge_group bench-parity skipped" full true 1 "$(fixture "$(printf '%s' "$deep" | jq -c 'del(."bench-parity")')")"
	run_case "merge_group os-matrix-qemu skipped" full true 1 "$(fixture "$(printf '%s' "$deep" | jq -c 'del(."os-matrix-qemu")')")"
	run_case "pr os-matrix-qemu jalan (harus skipped)" pr true 1 "$(fixture "$(printf '%s' "$pr_common" | jq -c '. + {"os-matrix-qemu":"success"}')")"
	run_case "merge_group deadcode cancelled" full true 1 "$(fixture "$(printf '%s' "$deep" | jq -c '. + {"deadcode":"cancelled"}')")"
	run_case "pr docs-only" pr false 0 "$(fixture '{}')"
	run_case "push main docs-only" smoke false 0 "$(fixture '{}')"
	run_case "merge_group docs-only" full false 0 "$(fixture '{}')"
	run_case "docs-only dengan gate Go jalan" pr false 1 "$(fixture '{"lint":"success"}')"
	run_case "changes gagal / tier kosong" "" "" 1 "$(fixture '{}' | jq -c '.changes.result="failure"')"

	if ((fail)); then
		echo "ci-required self-test: GAGAL"
		return 1
	fi
	echo "ci-required self-test: $n/$n skenario lulus"
}

if [ "${1:-}" = --self-test ]; then
	self_test
	exit $?
fi

: "${NEEDS:?NEEDS = JSON hasil toJSON(needs)}"
check "$NEEDS" "${TIER:-}" "${CODE:-}"
