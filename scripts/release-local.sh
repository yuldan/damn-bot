#!/usr/bin/env bash
# `make release`: satu konfigurasi untuk semua artefak rilis (GAP-INF-04).
# Build 6 target via scripts/release-build.sh (dua build byte-identik + SBOM),
# lalu SHA256SUMS + release.json via scripts/release-manifest.sh. Tanda tangan
# hanya bila DAMNBOT_RELEASE_KEY tersedia; tanpa kunci artefak tetap lokal dan
# tidak diklaim terverifikasi.
#
# VERSION wajib (atau HEAD harus tepat di tag vX.Y.Z).
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

version=${VERSION:-$(git describe --tags --exact-match 2>/dev/null | sed 's/^v//' || true)}
if [[ -z $version ]]; then
	echo "RELEASE GAGAL: set VERSION atau jalankan pada tag vX.Y.Z" >&2
	exit 2
fi
if [[ -z ${DAMNBOT_RELEASE_PUBKEY:-} ]]; then
	echo "RELEASE GAGAL: DAMNBOT_RELEASE_PUBKEY wajib (kunci publik build)" >&2
	exit 2
fi

# Enam target artefak rilis (SPEC §10; android/arm64 bukan artefak rilis).
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
	GOOS=${target%/*} GOARCH=${target#*/} RELEASE_VERSION="$version" \
		RELEASE_PUBKEY="$DAMNBOT_RELEASE_PUBKEY" REQUIRE_CLEAN=1 scripts/release-build.sh
done

scripts/release-manifest.sh "$version"

# SHA256SUMS mencakup biner + SBOM (12 baris), sama dengan job assemble CI.
: >dist/SHA256SUMS
while IFS= read -r f; do
	(cd dist && sha256sum -- "$f")
done < <(find dist -maxdepth 1 -type f ! -name 'SHA256SUMS' ! -name 'SHA256SUMS.sig' ! -name 'release.json' ! -name 'release.json.sig' -printf '%f\n' | LC_ALL=C sort) >>dist/SHA256SUMS

if [[ -n ${DAMNBOT_RELEASE_KEY:-} ]]; then
	scripts/sign-release.sh dist/SHA256SUMS
	(
		cd tools
		DAMNBOT_RELEASE_FILE="$root/dist/release.json" go test -run '^TestSignReleaseFile$' -count=1 ./release
	)
	scripts/verify-release.sh --dir dist
fi
echo "RELEASE ok: dist/ ($version, 6 artefak, SHA256SUMS, release.json)"
