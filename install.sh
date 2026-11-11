#!/bin/sh
# Official Damn-Bot installer (GAP-SEC-19/GAP-OPS-03): verify pinned ed25519 SHA256SUMS + checksums before extract/exec; record every write in install-manifest.json.
set -eu
umask 077
# Pinned release key (base64 minisign); release builds substitute a real key, the sentinel fails closed.
PUBKEY=${DAMNBOT_INSTALL_PUBKEY:-'__DAMNBOT_RELEASE_PUBKEY__'}
BASE=${DAMNBOT_INSTALL_BASE_URL:-https://github.com/damnbot/damnbot/releases}
VERSION=${DAMNBOT_VERSION:-latest}
BIN_DIR=${DAMNBOT_BIN_DIR:-$HOME/.local/bin}
BEGIN='# >>> damnbot >>>'
END='# <<< damnbot <<<'
die() { printf 'install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }
fetch() { # <url> <out>
	case $1 in
	https://*) ;;
	http://127.0.0.1:[0-9]* | http://127.0.0.1/* | http://localhost:[0-9]* | http://localhost/* | "http://[::1]":[0-9]* | "http://[::1]"/*) ;;
	*) die "refusing non-https URL: $1" ;;
	esac
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --proto '=https,http' --proto-redir '=https' --max-time 300 --retry 2 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then wget -q -O "$2" "$1"
	else die "curl or wget is required"; fi
}
hash() { # <file> -> hex sha256
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
	else shasum -a 256 "$1" | cut -d' ' -f1; fi
}
# verify_signature <file> <minisig>: ed25519 over BLAKE2b-512 (alg ED) or the
# file itself (legacy Ed), plus the trusted-comment global signature.
verify_signature() {
	need openssl
	f=$1 s=$2
	[ "$(wc -l <"$s")" -ge 4 ] || die "malformed signature file"
	sed -n '2p' "$s" | openssl base64 -d -A >"$tmp/sig"
	sed -n '3p' "$s" | sed 's/^trusted comment: //' | tr -d '\r\n' >"$tmp/trusted"
	sed -n '4p' "$s" | openssl base64 -d -A >"$tmp/global"
	[ "$(wc -c <"$tmp/sig")" -eq 74 ] && [ "$(wc -c <"$tmp/global")" -eq 74 ] || die "malformed signature"
	printf '%s' "$PUBKEY" | openssl base64 -d -A >"$tmp/pub"
	[ "$(wc -c <"$tmp/pub")" -eq 42 ] || die "pinned public key is not a minisign key"
	printf '\060\052\060\005\006\003\053\145\160\003\041\000' >"$tmp/der"
	dd if="$tmp/pub" bs=1 skip=10 count=32 2>/dev/null >>"$tmp/der"
	{ printf '%s\n' '-----BEGIN PUBLIC KEY-----'; openssl base64 -A -in "$tmp/der" | fold -w 64; printf '\n%s\n' '-----END PUBLIC KEY-----'; } >"$tmp/pub.pem"
	dd if="$tmp/sig" bs=1 skip=0 count=2 2>/dev/null >"$tmp/alg"
	if [ "$(cat "$tmp/alg")" = ED ]; then openssl dgst -blake2b512 -binary -out "$tmp/msg" "$f"
	else cp "$f" "$tmp/msg"; fi
	dd if="$tmp/sig" bs=1 skip=10 count=64 2>/dev/null >"$tmp/sig.bin"
	openssl pkeyutl -verify -rawin -pubin -inkey "$tmp/pub.pem" -in "$tmp/msg" -sigfile "$tmp/sig.bin" >/dev/null 2>&1 ||
		die "signature verification failed for $f"
	cat "$tmp/sig.bin" "$tmp/trusted" >"$tmp/gmsg"
	dd if="$tmp/global" bs=1 skip=10 count=64 2>/dev/null >"$tmp/gsig"
	openssl pkeyutl -verify -rawin -pubin -inkey "$tmp/pub.pem" -in "$tmp/gmsg" -sigfile "$tmp/gsig" >/dev/null 2>&1 ||
		die "trusted comment signature failed for $f"
}
json() { printf '"%s"' "$(printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')"; }
strip_block() { # replace an existing marker block, never user lines
	f=$1
	[ -f "$f" ] || return 0
	[ -L "$f" ] && die "refusing symlinked rc file: $f"
	b=$(grep -cxF "$BEGIN" "$f" || true)
	e=$(grep -cxF "$END" "$f" || true)
	b=${b:-0}
	e=${e:-0}
	[ "$b" -eq 0 ] && [ "$e" -eq 0 ] && return 0
	[ "$b" -eq 1 ] && [ "$e" -eq 1 ] || die "unbalanced damnbot markers in $f"
	awk -v b="$BEGIN" -v e="$END" '$0==b{s=1;next} $0==e{s=0;next} !s' "$f" >"$f.damnbot.$$"
	mv "$f.damnbot.$$" "$f"
}
add_block() { # <rc> <body-line>
	f=$1 body=$2
	[ -f "$f" ] || return 0
	strip_block "$f"
	before=$(hash "$f")
	added=0
	if [ -s "$f" ] && [ "$(tail -c 1 "$f" | od -An -tx1 | tr -d ' \n')" != 0a ]; then
		printf '\n' >>"$f"
		added=1
	fi
	printf '%s\n%s\n%s\n' "$BEGIN" "$body" "$END" >>"$f"
	printf '%s\t%s\t%s\trc\n' "$f" "$before" "$(hash "$f")" >>"$tmp/files"
	printf '%s\t%s\n' "$f" "$added" >>"$tmp/blocks"
}
tmp=$(mktemp -d "${TMPDIR:-/tmp}/damnbot-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
need tar
case $(uname -s) in
Linux) os=linux state=${XDG_DATA_HOME:-$HOME/.local/share}/damnbot ;;
Darwin) os=darwin state="$HOME/Library/Application Support/damnbot" ;;
*) die "unsupported OS: $(uname -s); use install.ps1 on Windows" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture: $(uname -m)" ;;
esac
if [ "$VERSION" = latest ]; then rel=$BASE/latest/download; else rel=$BASE/v$VERSION; fi
printf 'install: downloading %s artifacts\n' "$VERSION"
fetch "$rel/SHA256SUMS" "$tmp/SHA256SUMS"
fetch "$rel/SHA256SUMS.minisig" "$tmp/SHA256SUMS.minisig"
[ "$PUBKEY" != '__DAMNBOT_RELEASE_PUBKEY__' ] || die "no pinned release key in this checkout"
verify_signature "$tmp/SHA256SUMS" "$tmp/SHA256SUMS.minisig"
asset=$(awk -v suffix="_${os}_${arch}.tar.gz" '{n=$2; sub(/^\*/,"",n); if (n ~ /^damnbot_[A-Za-z0-9._-]+_/ && substr(n,length(n)-length(suffix)+1)==suffix) print n}' "$tmp/SHA256SUMS")
[ -n "$asset" ] && [ "$(printf '%s\n' "$asset" | wc -l)" -eq 1 ] || die "release has no unique ${os}/${arch} archive"
want=$(awk -v n="$asset" '{m=$2; sub(/^\*/,"",m)} m==n {print $1}' "$tmp/SHA256SUMS")
[ -n "$want" ] || die "archive checksum missing"
fetch "$rel/$asset" "$tmp/$asset"
[ "$(hash "$tmp/$asset")" = "$want" ] || die "checksum mismatch for $asset"
mkdir "$tmp/x"
tar -tzf "$tmp/$asset" >"$tmp/list" || die "archive is not a valid tar.gz"
while IFS= read -r n; do
	case $n in damnbot | ./damnbot) ;; *) die "unsafe path in archive: $n" ;; esac
done <"$tmp/list"
grep -qxF './damnbot' "$tmp/list" || grep -qxF 'damnbot' "$tmp/list" || die "archive has no damnbot binary"
tar -xzf "$tmp/$asset" -C "$tmp/x"
[ -f "$tmp/x/damnbot" ] && [ ! -L "$tmp/x/damnbot" ] || die "archive binary is missing or a symlink"
mkdir -p "$BIN_DIR"
before=null
[ -f "$BIN_DIR/damnbot" ] && before=$(hash "$BIN_DIR/damnbot")
cp "$tmp/x/damnbot" "$BIN_DIR/.damnbot.new.$$"
chmod 755 "$BIN_DIR/.damnbot.new.$$"
mv -f "$BIN_DIR/.damnbot.new.$$" "$BIN_DIR/damnbot"
printf '%s\t%s\t%s\tbinary\n' "$BIN_DIR/damnbot" "$before" "$(hash "$BIN_DIR/damnbot")" >"$tmp/files"
: >"$tmp/blocks"
add_block "$HOME/.bashrc" "case \":\$PATH:\" in *\":$BIN_DIR:\"*) ;; *) PATH=\"$BIN_DIR:\$PATH\"; export PATH ;; esac"
add_block "$HOME/.zshrc" "case \":\$PATH:\" in *\":$BIN_DIR:\"*) ;; *) PATH=\"$BIN_DIR:\$PATH\"; export PATH ;; esac"
add_block "${XDG_CONFIG_HOME:-$HOME/.config}/fish/config.fish" "contains \"$BIN_DIR\" \$PATH; or set -gx PATH \"$BIN_DIR\" \$PATH"
mkdir -p "$state"
chmod 700 "$state"
{
	printf '{"schema":1,"version":%s,"state_dir":%s,"installed_at":"%s","files":[' "$(json "$VERSION")" "$(json "$state")" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	first=1
	while IFS='	' read -r p b a k; do
		[ "$first" -eq 1 ] || printf ','
		first=0
		[ "$b" = null ] && bs=null || bs=$(json "$b")
		printf '{"path":%s,"kind":%s,"sha256_before":%s,"sha256_after":%s}' "$(json "$p")" "$(json "$k")" "$bs" "$(json "$a")"
	done <"$tmp/files"
	printf '],"blocks":['
	first=1
	while IFS='	' read -r p added; do
		[ "$first" -eq 1 ] || printf ','
		first=0
		printf '{"path":%s,"begin":%s,"end":%s,"added_newline":%s}' "$(json "$p")" "$(json "$BEGIN")" "$(json "$END")" "$([ "$added" = 1 ] && echo true || echo false)"
	done <"$tmp/blocks"
	printf '],"dirs":[%s,%s]}\n' "$(json "$BIN_DIR")" "$(json "$state")"
} >"$tmp/manifest" || die "cannot write manifest"
chmod 600 "$tmp/manifest"
mv -f "$tmp/manifest" "$state/install-manifest.json"
printf 'install: %s installed to %s\n' "$VERSION" "$BIN_DIR/damnbot"
printf 'install: manifest %s; run `damnbot setup --uninstall` to remove\n' "$state/install-manifest.json"
