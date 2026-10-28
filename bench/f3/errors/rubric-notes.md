# Rubric notes — usulan v1.1 (tanpa mengubah `rubric.json` v1)

Catatan ini mengumpulkan ambiguitas yang muncul dari skor A/B dan catatan
kedua rater. **`rubric.json` v1 tidak diubah** agar skor A/B tetap sebanding
dengan SHA `ff1938264c452fdb0bf7492f0dd705961c586610b48b7302e88555bad8c0576b`.
Usulan di bawah adalah bahan v1.1; bila diadopsi, kedua rater harus menilai
ulang dan skor lama tidak boleh dicampur dengan skor v1.1.

## 1. Dimensi `c` (what_to_do): anchor 1 vs 3

**Ambiguitas.** Banyak kasus hanya menyebut objek/penyebab tanpa langkah
eksplisit. A dan B kadang memberi 1, kadang 2, kadang 3 untuk fenomena yang
sama:

- `migrate-schema-newer`: A `c=1`, B `c=2`; pesan menunjukkan `db version 9 > binary 8` tetapi tidak menyuruh upgrade.
- `db-random-bytes`: A `c=2`, B `c=1`; pesan menyebut DB tidak valid, tanpa tindakan.
- `cfg-range`: A/B `c=4`; rentang sah disebut, nilai aktual tidak.
- `data-dir-readonly`: `c=2`; hanya `blobs` relatif, tanpa langkah `chmod`/pindah path.

**Usulan v1.1.**

- `1` = tidak ada objek maupun tindakan yang bisa ditindaklanjuti.
- `2` = objek/penyebab disebut, tetapi tidak ada tindakan (termasuk "restore/rebuild" yang hanya tersirat).
- `3` = satu tindakan konkret disebut tanpa nilai/contoh (mis. "hapus file", "hentikan proses", "pakai binary lebih baru").
- `4` = tindakan + nilai/format/contoh yang bisa langsung dipakai.
- `5` = urutan pemulihan + verifikasi/rollback.

**Dampak.** Definisi ini kemungkinan menaikkan sebagian `c=1` menjadi `2`,
tetapi tidak boleh diterapkan retroaktif.

## 2. Dimensi `e` (no_stack_for_usage): apa yang dihitung "lapisan wrapper"?

**Ambiguitas.** Capture JSON memuat rantai seperti
`run: open ctxdb: ctxdb: ctxdb: file is not a database (26)` atau
`provider: … after 1 attempt(s): provider network: inference POST …`.
A memberi `e=4`, B `e=3` untuk kasus DB; A/B juga berbeda pada prefiks `run:`.
Pertanyaannya: apakah prefiks `run:` dan pengulangan `ctxdb: ctxdb:` dihitung
sebagai frame/wrapper?

**Usulan v1.1.**

- Stack trace/panic/`goroutine` tetap `1`.
- Frame `file.go:line` atau daftar call internal = `2`.
- Rantai wrapper error ≥ 4 lapisan = `3`.
- 1–3 pembungkus yang masih membantu (termasuk prefiks `run:`) = `4`.
- Pesan bersih, atau detail internal hanya berupa kode/status terstruktur = `5`.
- Prefiks `run:` adalah presentasi CLI, bukan lapisan internal; pengulangan
  nama paket yang identik (`ctxdb: ctxdb:`) dihitung satu lapisan.

**Dampak.** Kasus DB yang sekarang `e=4`/`e=3` perlu disepakati ulang; jangan
menaikkan skor lama tanpa re-rating.

## 3. Dimensi `a` (what_failed): 4 vs 5

**Ambiguitas.** Batas "objek konkret" dan "keadaan akhir spesifik" tidak
konsisten:

- `cfg-unknown-key`: A `a=5`, B `a=4`; key disebut, tetapi daftar key sah tidak.
- `cfg-duplicate-key`: A `a=4`, B `a=5`; baris/tabel duplikat disebut.
- `db-is-directory`: A/B `a=3`; path DB tidak disebut.

**Usulan v1.1.**

- `4` = operasi + objek konkret (path/key/model/endpoint).
- `5` = operasi + objek + keadaan akhir yang membedakan kasus (nilai, versi,
  baris, status HTTP + kode, atau posisi byte).
- Bila pesan yang sama dipakai untuk beberapa penyebab berbeda (mis. semua
  kegagalan config menjadi "cannot read file"), `a` maksimal `3` sampai
  penyebab dibedakan.

## 4. Dimensi `d` (no_secret_leak): host/endpoint dan metadata lingkungan

**Ambiguitas.** B memberi `d=4` pada `net-dns` karena resolver lokal
`127.0.0.53` ikut tercetak; A memberi `d=5`. Sebaliknya, host/endpoint publik
(`api.openai.com`, `127.0.0.1`) muncul di banyak capture dan tetap dinilai
aman.

**Usulan v1.1.**

- Secret, token, password, isi prompt, isi file user, path rumah, nama user
  mentah, atau nama host internal = pelanggaran (`1`–`3` sesuai berat).
- Endpoint/host publik, alamat loopback, dan alamat resolver lokal bukan
  secret; ini metadata lingkungan dan tidak menurunkan `d` di bawah `4`.
- `d=5` bila tidak ada secret/PII dan nilai lingkungan sudah berupa placeholder.

**Dampak.** `net-dns` berpotensi `d=5` pada v1.1, bukan `4`; skor A/B tidak
diubah.

## 5. Unit penilaian: metadata JSON ikut dinilai?

**Ambiguitas.** Capture provider berbentuk satu baris JSON dengan
`schema`, `session_id`, `stop_reason`, `stop_detail`, `error`, `usage`,
`iterations`. Rater bisa menilai metadata sebagai bagian pesan (mis.
`stop_reason: error` menambah kejelasan) atau mengabaikannya.

**Usulan v1.1.**

- Nilai hanya field yang dilihat pengguna:
  - `stderr` untuk error CLI/config/DB;
  - `error`, `stop_reason`, `stop_detail` untuk output JSON;
  - baris `tool_result` untuk output `stream-json`.
- Jangan menilai `session_id`, `usage`, `iterations`, `duration_ms`, `argv`,
  `schema`, atau metadata runner kecuali field itu memuat pesan error.
- Baris `stop_reason=max_iterations` pada kasus `tool-args` adalah artefak
  harness; nilai `tool_result`, bukan baris itu.

**Dampak.** Bila aturan ini diterapkan, sebagian skor `e`/`a` dapat berubah
karena metadata tidak lagi dihitung sebagai pesan. Skor A/B saat ini tetap
memakai pembacaan bebas (lihat `scores-proxy.json`) dan tidak boleh
direkonsiliasi tanpa re-rating.

## 6. Isu fixture, bukan rubrik

`net-tls` v1 memakai literal `{port}` yang tidak tersubstitusi, sehingga
capture lama menguji `invalid port`, bukan TLS. Runner sudah diperbaiki
(substitusi `{port}`) dan capture baru memuat
`http: server gave HTTP response to HTTPS client`. Skor A/B lama untuk
`net-tls` **tidak valid** untuk capture baru dan dikecualikan dari basis
`valid_cases` di `scores-proxy.json`; kasus ini perlu dinilai ulang.
