# Termux (Android)

Status paritas: **OPS-031** ([`FEATURE-PARITY.md`](FEATURE-PARITY.md) baris OPS-031) dan target
Android/Termux arm64 di [`CROSS-CUTTING.md`](CROSS-CUTTING.md) §7. Damn-Bot adalah satu binary Go
statis (ADR-001, ADR-011): Termux tidak memerlukan Python, Node, root, atau systemd.

## Persyaratan

- Android 7+ arm64 dengan Termux dari F-Droid (build Play Store membatasi `pkg`/exec).
- Go ≥ 1.26 dan git: `pkg install golang git make`.
- Opsional `termux-setup-storage` bila workspace berada di `~/storage` (izin media Android).
- **Tanpa root.** Jangan menjalankan di `su`, proot root, atau emulator yang memberi UID 0;
  `scripts/termux-smoke.sh` menolak UID 0 sebelum build.

## Build dan verifikasi

```bash
pkg install golang git make
git clone https://github.com/damnbot/damnbot
cd damnbot
CGO_ENABLED=0 go build -trimpath -tags goolm -ldflags="-s -w" -o "$PREFIX/bin/damnbot" ./cmd/damnbot
damnbot --version
```

Tag build kanonik tetap `goolm` dan `CGO_ENABLED=0` (ENGINEERING E-39); binary Android/arm64
dibangun dari sumber yang sama dengan target rilis lain. Smoke opt-in (build + subset unit,
tanpa root, tidak dijalankan otomatis oleh CI):

```bash
DAMNBOT_TERMUX_SMOKE=1 ./scripts/termux-smoke.sh
```

Variabel opsional: `DAMNBOT_TERMUX_BIN` (path binary), `DAMNBOT_TERMUX_PKGS` (subset paket
`go test`, default `./internal/config ./internal/i18n ./internal/schema`),
`DAMNBOT_TERMUX_TIMEOUT` (mis. `300s`).

## Data dan config

Termux menyetel `HOME` ke direktori privat aplikasi; Damn-Bot mengikuti konvensi XDG
(SPEC §4.9):

- config: `~/.config/damnbot/config.toml`
- data: `~/.local/share/damnbot` (sesi, DB, log, backup)
- cache: `~/.cache/damnbot`

Password/keychain memakai `secrets.enc` 0600 di config dir karena Android tidak menyediakan
OS keychain yang dapat dijangkau Termux. Gunakan `damnbot setup` untuk membuat config awal.

## Latar belakang dan layanan

Android tidak mendukung systemd/launchd/Windows Service (OPS-026). Pilihan yang didukung:

- `termux-wake-lock` sebelum `damnbot serve` agar Android tidak membekukan proses; lepas
  dengan `termux-wake-unlock` setelah berhenti.
- `pkg install termux-services` lalu `sv-enable damnbot` bila ingin proses diawasi runit
  Termux (skrip unit milik pengguna, bukan bagian binary).
- `tmux` untuk sesi TUI yang tetap hidup saat aplikasi berpindah.

Gateway tetap mengikuti kebijakan SPEC: satu proses penulis per data dir (SPEC §3.3), jadi
jangan menjalankan dua `damnbot serve` pada data dir yang sama.

## Batasan

- Tidak ada CGO: engine SQLite memakai `modernc.org/sqlite`, bukan `libsqlite3` sistem.
- Race detector Go tidak tersedia untuk `GOOS=android`; suite `-race` berjalan di target
  Linux/macOS/Windows (CROSS-CUTTING §7).
- Docker/Podman backend `execute_code` tidak tersedia (tidak ada daemon container di
  Android); gunakan backend lokal dengan approval.
- Chromium/ffmpeg tidak dibundel (GAP-INF-03); modul browser/media tetap opt-in dan butuh
  binary eksternal.
- Ketersediaan memori lebih ketat daripada desktop: jalankan satu sesi `serve` dan hindari
  `--verbose` pada perangkat RAM rendah.

## Bukti

- `TestTermuxScriptSyntax` (parse bash dengan `mvdan.cc/sh`, tolak `sudo`/root/privilege).
- `TestDocsLinksOffline` (tautan internal dokumen ini tanpa jaringan).
- `scripts/termux-smoke.sh` dijalankan manual di perangkat Termux (opt-in, no root).
