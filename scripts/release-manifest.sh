#!/usr/bin/env bash
# Susun selimut rilis (GAP-INF-04): SHA256SUMS + release.json dari artefak di
# dist/ dan CHANGELOG.md. Teks changelog memuat seluruh entri versi; klien
# `update --check` memotongnya antara versi terpasang dan target.
#
# Pemakaian: scripts/release-manifest.sh <version>
set -euo pipefail

version=${1:?pemakaian: release-manifest.sh <version>}
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
[[ -d dist ]] || { echo "MANIFEST GAGAL: dist/ tidak ada" >&2; exit 1; }

tmp=$(mktemp -d "${TMPDIR:-/tmp}/damnbot-manifest.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

# Seluruh entri `## ...` (Unreleased + versi), bukan hanya Unreleased: manifest
# bertanda tangan harus memuat potongan yang dibutuhkan klien mana pun.
awk '/^## / { on = 1 } on' CHANGELOG.md >"$tmp/changelog.md"
if [[ ! -s $tmp/changelog.md ]]; then
	echo "MANIFEST GAGAL: CHANGELOG tidak memuat entri ##" >&2
	exit 1
fi

assets=$(
	cd dist
	for f in damnbot_*; do
		name=${f#./}
		case "$name" in *.sbom.txt) continue ;; esac
		if [[ $name =~ ^damnbot_(.+)_(linux|darwin|windows)_(amd64|arm64)(\.exe)?$ ]]; then
			file_version=${BASH_REMATCH[1]}
			os=${BASH_REMATCH[2]}
			arch=${BASH_REMATCH[3]}
		else
			echo "MANIFEST GAGAL: asset tak dikenal $name" >&2
			exit 1
		fi
		if [[ $file_version != "${version//[^A-Za-z0-9._-]/_}" ]]; then
			echo "MANIFEST GAGAL: versi asset $file_version tidak cocok tag $version" >&2
			exit 1
		fi
		sha=$(sha256sum "$f" | cut -d' ' -f1)
		jq -n --arg os "$os" --arg arch "$arch" --arg name "$name" --arg sha256 "$sha" \
			'{os:$os,arch:$arch,name:$name,sha256:$sha256}'
	done | jq -s 'sort_by(.name)'
)
channel=stable
if [[ $version == *-* ]]; then channel=beta; fi
jq -n --argjson schema 1 --arg channel "$channel" --arg version "$version" \
	--arg commit "$(git rev-parse HEAD)" --arg published_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
	--rawfile changelog "$tmp/changelog.md" --argjson assets "$assets" \
	'{schema:$schema,channel:$channel,version:$version,commit:$commit,published_at:$published_at,changelog:$changelog,assets:$assets}' \
	| jq -S . >dist/release.json
