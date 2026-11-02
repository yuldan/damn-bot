# F5 acceptance ledger

Acceptance F5 didefinisikan di [ROADMAP.md](ROADMAP.md) §F5
(`ROADMAP.md:170-179`), [SPEC.md](../SPEC.md) §22.1, [FEATURE-PARITY.md](FEATURE-PARITY.md),
[EXEC-AND-CODING.md](EXEC-AND-CODING.md), dan [CODE-TOOLS.md](CODE-TOOLS.md).
Baris memakai bukti yang **ada di main**; PR terbuka tidak dihitung `LULUS`.
Status: `LULUS` | `TERBUKA` | `DITUNDA`.

## Gap terbuka yang diputuskan

| id | cakupan | status | alasan | fase target | bukti/BR |
| --- | --- | --- | --- | --- | --- |
| F5-CODEINTEL-LANG | `repo_map`/`code_intel` hanya mengurai Go (`go/ast`) dan C/C++ (`tree-sitter` WASM). JavaScript/TypeScript/Python belum didukung. | DITUNDA | Aset npm `tree-sitter-*.wasm` adalah side module `dylink.0` yang tidak dapat dimuat wazero tanpa loader dylink; `lewtec/wazero-tree-sitter` tidak mengirim wasm dan mensyaratkan Go 1.27; tidak ada emscripten/clang untuk membangun modul self-contained. Keputusan: tanpa npm/emscripten build step di VPS (ADR-294 amandemen PR 2b). | F6 | ADR-294; `internal/codeintel/ts` hanya mengekspor `{c,cpp}`; `ErrUnsupportedLanguage` untuk bahasa lain |
| F5-BACKGROUND-WINDOWS | `shell background:true` dan registry `process_*` belum didukung di Windows. | DITUNDA | Registry butuh Job Object agar `Close` membunuh seluruh pohon proses; slice ini belum menyambungkannya. Permukaan tetap eksplisit: schema `shell` masih mengiklankan `background`, `Inspect`/`Run` mengembalikan `module_unavailable` yang membungkus `exec.ErrCapability` (bukan `exec_failed`/tool hilang), dan `internal/exec/process_windows_test.go` + `internal/tools/builtins_background_windows_test.go` menahan perilaku ini. | F6 | ADR-297/300; `exec.BackgroundSupported=false` di `internal/exec/process_windows.go` |

## Catatan

- Baris ini **tidak** menurunkan gate F5 yang lain; ia mencatat cakupan yang
  sengaja dipersempit agar tidak hilang saat `PHASE` dinaikkan.
- Opsi penutup gap: (a) paket aset self-contained ber-lisensi + pin sha256,
  (b) loader dylink di host wazero, (c) runtime tree-sitter pure-Go. Semua
  membutuhkan ADR baru dan pengukuran `make size-check` full/minimal.
