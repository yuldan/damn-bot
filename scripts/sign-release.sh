#!/usr/bin/env bash
# Tanda tangan rilis (CROSS-CUTTING §1.4/§1.5, ROADMAP F3): ed25519 atas byte SHA256SUMS.
# Kunci TIDAK pernah ditulis ke disk repo: material dibaca dari environment dan dipakai di memori
# oleh tools/release. Tanpa kunci ATAU passphrase skrip gagal keras (exit 2) dan tidak menulis
# SHA256SUMS.sig; kegagalan apa pun tidak boleh diteruskan sebagai rilis yang "sah".
#
# Pemakaian: DAMNBOT_RELEASE_KEY=<base64> DAMNBOT_RELEASE_PASSPHRASE=<rahasia> \
#            scripts/sign-release.sh <dir>/SHA256SUMS
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)

if [[ $# -ne 1 ]]; then
	echo "pemakaian: $0 <SHA256SUMS>" >&2
	exit 2
fi
sums=$1
if [[ ! -f $sums ]]; then
	echo "SIGN GAGAL: $sums tidak ditemukan" >&2
	exit 2
fi
sums=$(cd "$(dirname "$sums")" && pwd)/$(basename "$sums")

for name in DAMNBOT_RELEASE_KEY DAMNBOT_RELEASE_PASSPHRASE; do
	if [[ -z ${!name:-} ]]; then
		echo "SIGN GAGAL: $name tidak diset; material kunci hanya dari secret, tidak pernah dari disk" >&2
		exit 2
	fi
done

cd "$root/tools"
exec go run ./release sign "$sums"
