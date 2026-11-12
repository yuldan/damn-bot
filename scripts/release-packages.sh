#!/usr/bin/env bash
# Hasilkan metadata paket rilis (GAP-SEC-19): Homebrew tap, winget, .deb, .rpm.
# Sumber tunggal adalah dist/release.json (versi) + dist/SHA256SUMS (hash arsip
# installer yang sudah ditandatangani). Tidak ada unduhan jaringan; hash yang
# tidak ada di SHA256SUMS = gagal keras.
#
# Pemakaian: scripts/release-packages.sh [dist]
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
dist=${1:-$root/dist}
[[ -f $dist/release.json && -f $dist/SHA256SUMS ]] || {
	echo "PACKAGES GAGAL: $dist/release.json dan SHA256SUMS wajib" >&2
	exit 1
}
command -v jq >/dev/null 2>&1 || {
	echo "PACKAGES GAGAL: jq wajib" >&2
	exit 1
}
version=$(jq -r '.version' "$dist/release.json")
[[ $version =~ ^[A-Za-z0-9._-]+$ ]] || {
	echo "PACKAGES GAGAL: versi tidak valid: $version" >&2
	exit 1
}
base=https://github.com/damnbot/damnbot/releases
out=$dist/packaging
mkdir -p "$out/winget" "$out/deb" "$out/rpm"

sha_for() {
	local sha
	sha=$(awk -v n="$1" '{m=$2; sub(/^\*/,"",m)} m==n {print $1}' "$dist/SHA256SUMS")
	if [[ -z $sha ]]; then
		echo "PACKAGES GAGAL: $1 tidak ada di SHA256SUMS" >&2
		exit 1
	fi
	printf '%s' "$sha"
}

fill() { # <template> <output> <key> <value>...
	local src=$1 dst=$2
	shift 2
	cp "$src" "$dst"
	while (($#)); do
		sed "s|@$1@|$2|g" "$dst" >"$dst.tmp"
		mv "$dst.tmp" "$dst"
		shift 2
	done
	if grep -qE '@[A-Z_]+@' "$dst"; then
		echo "PACKAGES GAGAL: placeholder tersisa di $dst" >&2
		exit 1
	fi
}

fill "$root/packaging/homebrew/damnbot.rb.in" "$out/damnbot.rb" \
	VERSION "$version" BASE "$base" \
	SHA_DARWIN_ARM64 "$(sha_for "damnbot_${version}_darwin_arm64.tar.gz")" \
	SHA_DARWIN_AMD64 "$(sha_for "damnbot_${version}_darwin_amd64.tar.gz")" \
	SHA_LINUX_ARM64 "$(sha_for "damnbot_${version}_linux_arm64.tar.gz")" \
	SHA_LINUX_AMD64 "$(sha_for "damnbot_${version}_linux_amd64.tar.gz")"

fill "$root/packaging/winget/DamnBot.DamnBot.yaml.in" "$out/winget/DamnBot.DamnBot.yaml" \
	VERSION "$version"
fill "$root/packaging/winget/DamnBot.DamnBot.locale.en-US.yaml.in" "$out/winget/DamnBot.DamnBot.locale.en-US.yaml" \
	VERSION "$version"
fill "$root/packaging/winget/DamnBot.DamnBot.installer.yaml.in" "$out/winget/DamnBot.DamnBot.installer.yaml" \
	VERSION "$version" BASE "$base" \
	SHA_WINDOWS_AMD64 "$(sha_for "damnbot_${version}_windows_amd64.zip")" \
	SHA_WINDOWS_ARM64 "$(sha_for "damnbot_${version}_windows_arm64.zip")"

for arch in amd64 arm64; do
	fill "$root/packaging/deb/control.in" "$out/deb/control-$arch" VERSION "$version" ARCH "$arch"
done

rpmversion=${version//[^A-Za-z0-9._+~]/.}
for pair in amd64:x86_64 arm64:aarch64; do
	arch=${pair%%:*}
	rpmarch=${pair##*:}
	fill "$root/packaging/rpm/damnbot.spec.in" "$out/rpm/damnbot-$rpmarch.spec" \
		VERSION "$version" RPMVERSION "$rpmversion" RPMARCH "$rpmarch"
done

echo "PACKAGES ok: $out (brew, winget, deb, rpm) untuk $version"
