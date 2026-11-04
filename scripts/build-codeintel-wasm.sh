#!/usr/bin/env bash
# Build the embedded tree-sitter WASM for internal/codeintel/ts.
#
# Reproducible by construction: the only inputs are the pinned malivvan source
# tarball and the pinned Zig toolchain, both verified by SHA-256 before use.
# The output is compared byte-for-byte with the committed asset; a mismatch is
# a hard failure, never a silent overwrite.
#
# Usage: scripts/build-codeintel-wasm.sh [output.wasm]
# Requires: curl, tar, sha256sum, xz; network access on a cold cache.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out=${1:-$root/internal/codeintel/ts/ts.wasm}

malivvan_commit=5aa45b3d087ac4f9014faa31e2ea7a5eeabe834b
malivvan_sha256=549771a043950dbd5c12668d0b1d705c0ad9aa680fd9547fbbd5d20704396059
zig_version=0.17.1
zig_sha256=3581863e3cbcfd0fce0efc682ce7e9e93da96668a977791d356a26d2a7e9b17a
expected_sha256=f5a527335c1c388d04197177daaf5cd23ffa64a4c7c666ae6c46103d344ab3a1

case $(uname -s)-$(uname -m) in
Linux-x86_64) ;;
*) echo "build-codeintel-wasm: pin a Zig tarball for $(uname -s)-$(uname -m) first" >&2; exit 1 ;;
esac

cache=${XDG_CACHE_HOME:-$HOME/.cache}/damnbot/codeintel-wasm
mkdir -p "$cache"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fetch() { # <url> <dest> <sha256>
	if [[ ! -f $2 ]]; then
		curl -fsSL --retry 3 --max-time 600 "$1" -o "$2.tmp"
		mv "$2.tmp" "$2"
	fi
	echo "$3  $2" | sha256sum -c - >/dev/null
}

src_tgz=$cache/malivvan-$malivvan_commit.tar.gz
zig_tgz=$cache/zig-$zig_version-x86_64-linux.tar.xz
fetch "https://github.com/malivvan/tree-sitter/archive/$malivvan_commit.tar.gz" "$src_tgz" "$malivvan_sha256"
fetch "https://ziglang.org/download/$zig_version/zig-x86_64-linux-$zig_version.tar.xz" "$zig_tgz" "$zig_sha256"

mkdir -p "$work/src" "$work/zig"
tar xzf "$src_tgz" -C "$work/src" --strip-components=1
tar xf "$zig_tgz" -C "$work/zig" --strip-components=1
zig=$work/zig/zig

exports=(
	malloc free strlen
	ts_parser_new ts_parser_parse_string ts_parser_set_language ts_parser_delete
	ts_query_new ts_query_cursor_new ts_query_cursor_exec ts_query_cursor_next_match
	ts_query_capture_name_for_id ts_language_name ts_language_version
	ts_tree_root_node ts_node_string ts_node_child_count ts_node_named_child_count
	ts_node_child ts_node_named_child ts_node_type ts_node_start_byte ts_node_end_byte
	ts_node_is_error
	tree_sitter_c tree_sitter_cpp tree_sitter_javascript tree_sitter_python
	tree_sitter_typescript tree_sitter_tsx
)
ldflags=(-Wl,--no-entry -Wl,-z -Wl,stack-size=65536 -Wl,--strip-debug -Wl,--import-symbols)
for name in "${exports[@]}"; do ldflags+=("-Wl,--export=$name"); done

(
	cd "$work/src"
	"$zig" cc --target=wasm32-wasi-musl -mexec-model=reactor -I src/ \
		src/lib.c \
		src/c/parser.c \
		src/cpp/parser.c src/cpp/scanner.c \
		src/javascript/parser.c src/javascript/scanner.c \
		src/python/parser.c src/python/scanner.c \
		src/typescript/typescript/parser.c src/typescript/typescript/scanner.c \
		src/typescript/tsx/parser.c src/typescript/tsx/scanner.c \
		-o "$work/ts.wasm" -Os -fPIC "${ldflags[@]}"
)

actual_sha256=$(sha256sum "$work/ts.wasm" | cut -d' ' -f1)
if [[ $actual_sha256 != "$expected_sha256" ]]; then
	echo "build-codeintel-wasm: output $actual_sha256 != pinned $expected_sha256" >&2
	exit 1
fi

mkdir -p "$(dirname "$out")"
cp "$work/ts.wasm" "$out"
echo "built $out ($(stat -c %s "$out") bytes, sha256 $actual_sha256)"
