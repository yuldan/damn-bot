#!/usr/bin/env bash
# Korpus code tools (CODE-TOOLS §10.1) + oracle ripgrep 15.1.0 (F0-REPORT "Oracle rg") di $CORPORA_DIR,
# diverifikasi terhadap bench/corpora.lock: C1 = checkout hermes-agent pada commit lock (jumlah dan byte
# file ter-track), C2/C3 = gen-corpus deterministik (sha256 manifest), rg = sha256 tarball rilis + binary.
# Idempoten: korpus yang sudah terverifikasi (penanda .ok berisi baris lock) tidak dibangun ulang, sehingga
# direktori bisa di-cache CI (actions/cache, kunci = hash lock + skrip + generator).
#   CORPORA_DIR=/tmp/damnbot-corpora CORPORA="C1 C3 rg" scripts/corpora.sh
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
dir=${CORPORA_DIR:-/tmp/damnbot-corpora}
want=${CORPORA:-C1 C2 C3 rg}
lock=bench/corpora.lock
mkdir -p "$dir"

RG_URL=https://github.com/BurntSushi/ripgrep/releases/download/15.1.0/ripgrep-15.1.0-x86_64-unknown-linux-musl.tar.gz
RG_TAR_SHA=1c9297be4a084eea7ecaedf93eb03d058d6faae29bbc57ecdaf5063921491599
RG_BIN_SHA=ebeaf56f8a25e102e9419933423738b3a2a613a444fd749d695e15eba53f71f2
C1_REPO=https://github.com/NousResearch/hermes-agent.git

field() { # <baris lock> <kunci>
	tr ' ' '\n' <<<"$1" | sed -n "s/^$2=//p"
}

ok() { # <nama> <baris lock>: sudah terverifikasi?
	[[ -f "$dir/.$1.ok" && "$(cat "$dir/.$1.ok")" == "$2" ]]
}

fetch_rg() {
	local line="rg $RG_TAR_SHA $RG_BIN_SHA" tmp
	ok rg "$line" && return
	tmp=$(mktemp -d)
	curl -fsSL -o "$tmp/rg.tgz" "$RG_URL"
	echo "$RG_TAR_SHA  $tmp/rg.tgz" | sha256sum -c --quiet
	tar -xzf "$tmp/rg.tgz" -C "$tmp"
	install -m 0755 "$tmp"/ripgrep-15.1.0-x86_64-unknown-linux-musl/rg "$dir/rg"
	echo "$RG_BIN_SHA  $dir/rg" | sha256sum -c --quiet
	rm -rf "$tmp"
	echo "$line" >"$dir/.rg.ok"
}

fetch_c1() {
	local line commit files bytes got_files got_bytes
	line=$(grep '^C1 ' "$lock")
	ok C1 "$line" && return
	commit=$(field "$line" git)
	files=$(field "$line" tracked_files)
	bytes=$(field "$line" tracked_bytes)
	rm -rf "$dir/C1"
	git init -q "$dir/C1"
	git -C "$dir/C1" -c protocol.version=2 fetch -q --depth 1 "$C1_REPO" "$commit"
	git -C "$dir/C1" -c advice.detachedHead=false checkout -q FETCH_HEAD
	[[ $(git -C "$dir/C1" rev-parse HEAD) == "$commit" ]]
	got_files=$(git -C "$dir/C1" ls-files | wc -l)
	got_bytes=$(cd "$dir/C1" && git ls-files -z | xargs -0 stat -c %s | awk '{s += $1} END {print s}')
	if [[ $got_files != "$files" || $got_bytes != "$bytes" ]]; then
		echo "C1: $got_files file / $got_bytes B, lock $files / $bytes" >&2
		exit 1
	fi
	echo "$line" >"$dir/.C1.ok"
}

gen() { # <C2|C3>
	local line out
	line=$(grep "^$1 " "$lock")
	ok "$1" "$line" && return
	rm -rf "${dir:?}/$1"
	out=$(cd bench && go run ./gencorpus/cmd/gen-corpus -name "$1" -seed "$(field "$line" seed)" \
		-files "$(field "$line" files)" -mean "$(field "$line" mean)" -out "$dir/$1")
	if [[ $(field "$out" sha256) != "$(field "$line" sha256)" ]]; then
		echo "$1: $out" >&2
		echo "lock: $line" >&2
		exit 1
	fi
	echo "$line" >"$dir/.$1.ok"
}

for c in $want; do
	case $c in
	rg) fetch_rg ;;
	C1) fetch_c1 ;;
	C2 | C3) gen "$c" ;;
	*)
		echo "korpus tidak dikenal: $c" >&2
		exit 2
		;;
	esac
	echo "corpora: $c ok ($dir)"
done
