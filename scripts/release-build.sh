#!/usr/bin/env bash
# Satu resep build artefak rilis (GAP-INF-04, CROSS-CUTTING §1.4/§1.5).
# Dipakai matrix CI (satu target per job) dan `make release` (semua target).
# Dua build terpisah harus byte-identik; SBOM ditulis di samping artefak.
#
# Input (environment): GOOS, GOARCH, RELEASE_VERSION, RELEASE_PUBKEY,
# REQUIRE_CLEAN=1 (tolak pohon kotor; wajib di CI dan `make release`).
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

: "${GOOS:?GOOS wajib}"
: "${GOARCH:?GOARCH wajib}"
: "${RELEASE_VERSION:?RELEASE_VERSION wajib}"
RELEASE_PUBKEY=${RELEASE_PUBKEY:-}
REQUIRE_CLEAN=${REQUIRE_CLEAN:-0}
REQUIRE_VCS=${REQUIRE_VCS:-1}

if [[ $REQUIRE_CLEAN == 1 && -n $(git status --porcelain) ]]; then
	echo "REPRODUCIBLE GAGAL: pohon tidak bersih" >&2
	exit 1
fi
file_version=${RELEASE_VERSION//[^A-Za-z0-9._-]/_}
ldflags="-s -w -X main.version=$RELEASE_VERSION -X github.com/damnbot/damnbot/internal/update.version=$RELEASE_PUBKEY"
exe=damnbot
suffix=
if [[ $GOOS == windows ]]; then
	exe=damnbot.exe
	suffix=.exe
fi
name="damnbot_${file_version}_${GOOS}_${GOARCH}${suffix}"
epoch=$(git log -1 --format=%ct)
work=$(mktemp -d "${TMPDIR:-/tmp}/damnbot-build.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir -p dist

for pass in a b; do
	# Cache terpisah agar build kedua tidak memakai hasil build pertama.
	GOCACHE="$work/gocache-$pass" SOURCE_DATE_EPOCH="$epoch" CGO_ENABLED=0 \
		go build -trimpath -buildvcs=true -tags goolm -ldflags="$ldflags" \
		-o "$work/$exe.$pass" ./cmd/damnbot
done
if ! cmp -s "$work/$exe.a" "$work/$exe.b"; then
	echo "REPRODUCIBLE GAGAL: dua build tidak byte-identik untuk $name" >&2
	sha256sum "$work/$exe.a" "$work/$exe.b" >&2
	exit 1
fi
cp "$work/$exe.a" "dist/$name"
if [[ $REQUIRE_VCS == 1 ]]; then
	if ! go version -m "dist/$name" | grep -Eq 'vcs\.revision=[0-9a-f]{40}'; then
		echo "REPRODUCIBLE GAGAL: build tanpa stempel vcs.revision" >&2
		exit 1
	fi
	if go version -m "dist/$name" | grep -q 'vcs\.modified=true'; then
		echo "REPRODUCIBLE GAGAL: build dari pohon termodifikasi" >&2
		exit 1
	fi
fi
sha=$(sha256sum "dist/$name" | cut -d' ' -f1)
printf 'sha256 `%s`  %s\n' "$sha" "$name"
go version -m "dist/$name" >"$work/version-m.txt"
(cd tools && go run ./release sbom --artifact "$name" --sha256 "$sha") \
	<"$work/version-m.txt" >"dist/$name.sbom.txt"
