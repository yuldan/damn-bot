#!/bin/sh
# QEMU core suite untuk dua target E-39 tanpa runner hosted (ROADMAP F9, ADR-073).
#
# Target `linux/arm` dan `android/arm64` tidak punya runner GitHub hosted. Job CI
# `os-matrix-qemu` (tier `full`, termasuk nightly via workflow_call) mengompilasi
# test binary di host lalu MENJALANKANNYA di userspace arch yang sama dengan
# qemu-user-static:
#   build <target>          GOOS/GOARCH dari target, `go test -c` untuk paket inti
#   run   <target>          eksekusi manifest di dalam container (dipanggil dari
#                           image arch target; linux/arm = golang resmi,
#                           android/arm64 = termux-docker yang membawa bionic +
#                           /system/bin/linker64 sehingga binary GOOS=android bisa start)
#
# Paket inti: ctxdb (DB), exec, acp, dan runner headless (`damnbot run`).
# Bukti eksekusi: setiap binary mencetak `=== RUN`/`PASS`; kegagalan apa pun
# menggagalkan job. Bukan hanya cross-compile.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
out=${QEMU_TEST_DIR:-$root/.qemu-tests}
headless_run='^TestRunHeadless'
# E-39: tag goolm wajib di setiap build/test; tag tambahan boleh lewat QEMU_TEST_TAGS.
gotags=${QEMU_TEST_TAGS:-goolm}
core_packages='./internal/ctxdb/... ./internal/exec/... ./internal/acp/...'

usage() {
	echo "pemakaian: $0 build|run <linux/arm|android/arm64>" >&2
	exit 2
}

[ $# -eq 2 ] || usage
mode=$1
target=$2

case $target in
linux/arm)
	goos=linux
	goarch=arm
	;;
android/arm64)
	goos=android
	goarch=arm64
	;;
*) usage ;;
esac

dir=$out/$goos-$goarch
manifest=$dir/manifest.tsv

build() {
	rm -rf "$dir"
	mkdir -p "$dir"
	: >"$manifest"
	for pkg in $(go list -tags "$gotags" $core_packages); do
		name=$(printf '%s' "$pkg" | tr '/' '_')
		bin=$dir/$name.test
		echo "cross-compile $pkg ($goos/$goarch)"
		CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go test -tags "$gotags" -c -o "$bin" "$pkg"
		chmod 0755 "$bin"
		pkgdir=$(go list -f '{{.Dir}}' "$pkg")
		rel=${pkgdir#"$root"/}
		printf '%s\t%s\t%s\n' "$pkg" "$rel" "${bin#"$root"/}" >>"$manifest"
	done
	# Runner headless: seluruh binary dikompilasi, yang dieksekusi hanya
	# TestRunHeadless* (suite `damnbot run`), karena cmd/damnbot memuat tes TUI/CLI
	# yang tidak relevan untuk matriks OS inti.
	echo "cross-compile ./cmd/damnbot ($goos/$goarch)"
	CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go test -tags "$gotags" -c -o "$dir/headless.test" ./cmd/damnbot
	chmod 0755 "$dir/headless.test"
	printf '%s\t%s\t%s\n' "headless" "cmd/damnbot" "${dir#"$root"/}/headless.test" >>"$manifest"
	echo "manifest: ${manifest#"$root"/} ($(wc -l <"$manifest") binary)"
}

run() {
	[ -f "$manifest" ] || {
		echo "manifest tidak ada: ${manifest#"$root"/}; jalankan build dahulu" >&2
		exit 1
	}
	while IFS='	' read -r pkg reldir bin; do
		echo "=== $pkg ($target)"
		if [ "$pkg" = headless ]; then
			(cd "$root/$reldir" && "$root/$bin" -test.count=1 -test.run "$headless_run")
		else
			(cd "$root/$reldir" && "$root/$bin" -test.count=1)
		fi
	done <"$manifest"
	echo "OS matrix $target: seluruh paket inti lulus"
}

case $mode in
build) build ;;
run) run ;;
*) usage ;;
esac
