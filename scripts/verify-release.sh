#!/usr/bin/env bash
# Verifikasi rilis fail-closed (CROSS-CUTTING §1.4/§1.5, ROADMAP F3): tanda tangan ed25519 atas
# byte SHA256SUMS diperiksa lebih dulu, lalu setiap checksum di dalamnya. Exit 2 = pemakaian/
# konfigurasi tidak lengkap (mis. kunci publik tidak ada); exit 1 = tanda tangan atau checksum
# tidak cocok. Tidak ada jalur yang berakhir 0 bila salah satu langkah tidak dapat dibuktikan.
#
# Pemakaian: DAMNBOT_RELEASE_PUBKEY=<base64> scripts/verify-release.sh [--dir DIR] [--pubkey BASE64]
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
dir=.
pub=${DAMNBOT_RELEASE_PUBKEY:-}

while [[ $# -gt 0 ]]; do
	case $1 in
	--dir)
		[[ $# -ge 2 ]] || {
			echo "pemakaian: $0 [--dir DIR] [--pubkey BASE64]" >&2
			exit 2
		}
		dir=$2
		shift 2
		;;
	--pubkey)
		[[ $# -ge 2 ]] || {
			echo "pemakaian: $0 [--dir DIR] [--pubkey BASE64]" >&2
			exit 2
		}
		pub=$2
		shift 2
		;;
	-h | --help)
		echo "pemakaian: $0 [--dir DIR] [--pubkey BASE64]"
		exit 0
		;;
	*)
		echo "VERIFY GAGAL: argumen tidak dikenal: $1" >&2
		exit 2
		;;
	esac
done

if [[ -z $pub ]]; then
	echo "VERIFY GAGAL: DAMNBOT_RELEASE_PUBKEY atau --pubkey wajib (tanpa kunci publik tidak ada verifikasi)" >&2
	exit 2
fi
if [[ ! -d $dir ]]; then
	echo "VERIFY GAGAL: direktori $dir tidak ditemukan" >&2
	exit 2
fi
dir=$(cd "$dir" && pwd)

cd "$root/tools"
exec go run ./release verify --dir "$dir" --pubkey "$pub"
