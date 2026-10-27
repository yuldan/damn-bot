#!/usr/bin/env bash
# Uji batas keamanan rilis (CROSS-CUTTING §1.4/§1.5): unit tools/release + sign→verify end-to-end
# dengan kunci uji yang dibuat in-process di direktori sementara (tidak pernah di repo).
# Kontrol negatif wajib: (a) artefak sah lolos, (b) satu byte diubah → gagal, (c) kunci/passphrase/
# kunci publik tidak ada → gagal keras (bukan lolos). Dipanggil job CI `supply-chain` lewat
# `make release-test`; kegagalan apa pun exit ≠ 0.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/damnbot-release-test.XXXXXX")
trap 'rm -rf "$work"' EXIT

fail() {
	echo "RELEASE TEST GAGAL: $*" >&2
	exit 1
}

passphrase='uji-rilis-damnbot-2026'
artifact='damnbot_test_linux_amd64'

echo "release-test: unit tools/release"
(cd "$root/tools" && go test -count=1 ./release)

echo "release-test: keygen (in-process, kunci tidak pernah menyentuh disk)"
export DAMNBOT_RELEASE_PASSPHRASE="$passphrase"
keygen=$(cd "$root/tools" && go run ./release keygen)
key=$(awk '$1 == "material" { print $2 }' <<<"$keygen")
pub=$(awk '$1 == "pubkey" { print $2 }' <<<"$keygen")
[[ -n $key && -n $pub ]] || fail "keygen tidak menghasilkan material/pubkey"

printf 'AAAA\n' >"$work/$artifact"
printf 'BBBB\n' >"$work/damnbot_test_windows_amd64.exe"
(cd "$work" && sha256sum -- damnbot_test_* | LC_ALL=C sort -k2 >SHA256SUMS)

echo "release-test: (a) sign→verify artefak sah"
DAMNBOT_RELEASE_KEY="$key" "$root/scripts/sign-release.sh" "$work/SHA256SUMS" >/dev/null
[[ -s $work/SHA256SUMS.sig ]] || fail "sign tidak menulis SHA256SUMS.sig"
if grep -R -qF -- "$key" "$work"; then
	fail "material kunci tertulis ke disk"
fi
DAMNBOT_RELEASE_PUBKEY="$pub" "$root/scripts/verify-release.sh" --dir "$work" >/dev/null ||
	fail "artefak sah tidak lolos verifikasi"

echo "release-test: (b) satu byte artefak diubah → gagal checksum"
cp "$work/$artifact" "$work/$artifact.orig"
printf 'Z' | dd of="$work/$artifact" bs=1 seek=0 conv=notrunc 2>/dev/null
if DAMNBOT_RELEASE_PUBKEY="$pub" "$root/scripts/verify-release.sh" --dir "$work" >"$work/neg" 2>&1; then
	fail "verifikasi LOLOS setelah satu byte artefak diubah"
fi
grep -q 'sha256 tidak cocok' "$work/neg" || fail "tamper artefak gagal karena sebab lain: $(cat "$work/neg")"
mv "$work/$artifact.orig" "$work/$artifact"

echo "release-test: (b) byte SHA256SUMS diubah → gagal tanda tangan"
cp "$work/SHA256SUMS" "$work/SHA256SUMS.orig"
printf 'Z' | dd of="$work/SHA256SUMS" bs=1 seek=0 conv=notrunc 2>/dev/null
if DAMNBOT_RELEASE_PUBKEY="$pub" "$root/scripts/verify-release.sh" --dir "$work" >"$work/neg" 2>&1; then
	fail "verifikasi LOLOS setelah SHA256SUMS diubah"
fi
grep -q 'tanda tangan' "$work/neg" || fail "tamper SHA256SUMS gagal karena sebab lain: $(cat "$work/neg")"
mv "$work/SHA256SUMS.orig" "$work/SHA256SUMS"

echo "release-test: (c) kunci/passphrase tidak ada → sign gagal keras"
rm -f "$work/SHA256SUMS.sig"
if env -u DAMNBOT_RELEASE_KEY "$root/scripts/sign-release.sh" "$work/SHA256SUMS" >"$work/neg" 2>&1; then
	fail "sign LOLOS tanpa DAMNBOT_RELEASE_KEY"
fi
grep -q 'DAMNBOT_RELEASE_KEY' "$work/neg" || fail "alasan kegagalan tidak menyebut DAMNBOT_RELEASE_KEY"
[[ ! -e $work/SHA256SUMS.sig ]] || fail "sign tanpa kunci tetap menulis SHA256SUMS.sig"
if env -u DAMNBOT_RELEASE_PASSPHRASE DAMNBOT_RELEASE_KEY="$key" \
	"$root/scripts/sign-release.sh" "$work/SHA256SUMS" >"$work/neg" 2>&1; then
	fail "sign LOLOS tanpa DAMNBOT_RELEASE_PASSPHRASE"
fi
[[ ! -e $work/SHA256SUMS.sig ]] || fail "sign tanpa passphrase tetap menulis SHA256SUMS.sig"

echo "release-test: (c) kunci publik tidak ada / salah → verify gagal keras"
if env -u DAMNBOT_RELEASE_PUBKEY "$root/scripts/verify-release.sh" --dir "$work" >"$work/neg" 2>&1; then
	fail "verifikasi LOLOS tanpa kunci publik"
fi
if DAMNBOT_RELEASE_PUBKEY="$pub" "$root/scripts/verify-release.sh" --dir "$work" >"$work/neg" 2>&1; then
	fail "verifikasi LOLOS tanpa SHA256SUMS.sig"
fi
DAMNBOT_RELEASE_KEY="$key" "$root/scripts/sign-release.sh" "$work/SHA256SUMS" >/dev/null
other=$(cd "$root/tools" && DAMNBOT_RELEASE_PASSPHRASE="passphrase-lain" go run ./release keygen)
otherpub=$(awk '$1 == "pubkey" { print $2 }' <<<"$other")
[[ -n $otherpub ]] || fail "keygen kedua tidak menghasilkan pubkey"
if DAMNBOT_RELEASE_PUBKEY="$otherpub" "$root/scripts/verify-release.sh" --dir "$work" >"$work/neg" 2>&1; then
	fail "verifikasi LOLOS dengan kunci publik yang salah"
fi

echo "release-test: kontrol positif terakhir"
DAMNBOT_RELEASE_KEY="$key" "$root/scripts/sign-release.sh" "$work/SHA256SUMS" >/dev/null
DAMNBOT_RELEASE_PUBKEY="$pub" "$root/scripts/verify-release.sh" --dir "$work" >/dev/null ||
	fail "artefak sah tidak lolos setelah kontrol negatif"

echo "RELEASE TEST ok: (a) sah lolos, (b) byte artefak/checksum diubah ditolak, (c) kunci, passphrase, dan kunci publik hilang ditolak"
