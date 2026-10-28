# F3 — Bahan penilaian "Error informatif"

Suite 60 kesalahan nyata yang dipicu lewat binary `damnbot`, dengan capture
`stdout`/`stderr`/exit code/durasi yang sudah disanitasi. Suite ini
**menyiapkan bahan** untuk dimensi SCORECARD §1.4; ia tidak memberi skor
kualitas pesan. Penilaian tetap dilakukan rater manusia dengan
[`rubric.json`](rubric.json).

## 1. Rujukan normatif

| Sumber | Isi yang dipakai |
|---|---|
| `docs/DECISIONS.md:1533` (ADR-065) | Baris `§1.4 Error informatif → F3`, "suite 60 kesalahan butuh permukaan CLI/config/setup". |
| `docs/DECISIONS.md:2075` (ADR-083) | Acceptance fase wajib memuat status baseline untuk dimensi ADR-065/083 berstatus `Fx`. |
| `docs/SCORECARD.md:68` | Metrik: `% pesan error (suite 60 kesalahan umum) yang menyebut penyebab + langkah perbaikan`; rubrik biner, 2 penilai; target ≥ 95%. |
| `docs/BASELINE-hermes.md:79-91` (§5) | Pembanding reliabilitas hermes; khususnya `Validasi argumen tool: hanya json.loads` (`:90`) dan `except Exception` (`:83`). |
| `docs/ROADMAP.md:142` | F3 "Permukaan & plug-and-play"; deliverable config/setup/importer, acceptance fase. |
| `SPEC.md:2893` (GAP-REL-05) | Error migrasi fail-closed; DB lebih baru dari binary menolak start. |
| `SPEC.md:2952` (GAP-UX-03) | Kegagalan tidak pernah dilaporkan sebagai sukses; error harus bertipe dan menyebut konteks. |
| `cmd/damnbot/main.go:20-38` | Permukaan binary pada commit ini: hanya `run` dan `--version`. |
| `cmd/damnbot/run.go:351-352` | Config yang **tidak ada** sengaja jatuh ke `config.Default()`, jadi bukan error. |

## 2. Cakupan: 12 grup × 5 kasus

| Grup | Kasus | Yang dipicu |
|---|---:|---|
| `config-invalid` | 5 | TOML rusak, key tak dikenal, nilai di luar rentang, durasi salah, tabel ganda. |
| `config-unreadable` | 5 | Path config berupa direktori, tanpa izin baca, symlink melingkar, >1 MiB, induk tanpa izin. |
| `data-dir` | 5 | Data dir berupa file, induk berupa file, read-only, induk read-only, subdir `run` bentrok. |
| `db-corrupt` | 5 | Bukan SQLite, DB berupa direktori, DB terpotong, magic rusak, page size rusak. |
| `db-migration` | 5 | Schema lebih baru, checksum migrasi rusak, migrasi diulang, holder lama, backup premigrate gagal. |
| `provider-model` | 5 | Ref tanpa `/`, provider tak dikenal, provider kosong, model 404, model ditolak 400. |
| `credentials` | 5 | Key tidak ada, key salah, akun dilarang, key dari env salah, key berisi newline. |
| `network` | 5 | Connection refused, DNS gagal, mode offline, base URL invalid, TLS ke server non-TLS. |
| `timeout-retry` | 5 | TTFB timeout, dial timeout, rate limit, 500, 503 (retry habis). |
| `tool-args` | 5 | Tool tak dikenal, argumen bukan JSON, path traversal, properti wajib hilang, shell denied. |
| `provider-stream` | 5 | Chunk bukan JSON, body HTML, stream kosong, redirect 302, chunk tanpa `choices`. |
| `cli-usage` | 5 | Tanpa argumen, flag tak dikenal, enum output salah, prompt hilang, subcommand tak dikenal. |

**Kasus yang diganti, bukan dipalsukan.** Permukaan binary saat ini
(`cmd/damnbot/main.go:20-38`) belum punya subcommand sesi/impor/TUI/ACP/setup:

- **"config tidak ada"** tidak bisa menjadi kasus error: config yang tidak ada
  sengaja memakai default (`cmd/damnbot/run.go:351-352`). Diganti dengan
  `config-unreadable` (path ada tetapi tidak bisa dibaca).
- **"sesi tidak ditemukan"** tidak dapat dipicu karena tidak ada subcommand
  sesi; diganti dengan `provider-stream`.
- **"impor sumber rusak"** tidak dapat dipicu karena importer belum ada;
  diganti dengan `cli-usage`.
- **"DB terkunci"** tidak dijadikan kasus tersendiri karena lock barrier
  menunggu 30 s dan hasilnya bergantung pada proses kedua. Kondisi terkait
  diwakili `db-migration/migrate-old-holder` (migrasi ditolak karena holder
  lama), yang benar-benar dapat dipicu dan deterministik.

Semua kasus di `cases.json` memakai `"expect_exit_nonzero": true`; tidak ada
kasus yang hanya "seharusnya" gagal.

## 3. Metode

1. `bench/f3/run-errors.sh` membangun binary asli dengan `-tags goolm`.
2. Runner (`bench/f3/cmd/f3errors`) membuat satu fake provider loopback
   (tanpa jaringan berbayar), satu template DB bersih, lalu menjalankan setiap
   `argv` kasus sebagai proses binary asli.
3. Setiap kasus mendapat `HOME`, `TMPDIR`, dan working directory sendiri di
   bawah direktori sementara; tidak ada data user nyata yang disentuh.
4. Fixture DB rusak/migrasi dibuat dari template yang sama, lalu diubah pada
   byte header SQLite (`user_version` di offset 60), checksum migrasi, atau
   layout direktori. Fixture tidak memakai SQL mentah dan tidak menambah
   dependency Go.
5. `stdout`, `stderr`, exit code, dan durasi disimpan ke
   `captured/<id>.json` setelah sanitasi.

Kasus provider memakai `--output json` (atau `stream-json` untuk tool) agar
error yang dipancarkan binary tertangkap. Ini bukan kosmetik; lihat temuan di
bawah.

### Temuan (bukan gate): `run --output text` kosong saat provider gagal

Pada commit yang diuji, `run --output text` keluar dengan kode 2 tetapi tidak
menulis pesan error apa pun. Bukti capture
[`captured/finding-text-output-empty.json`](captured/finding-text-output-empty.json):
`exit_code: 2`, `stdout: "\n"` (satu baris kosong), `stderr: ""`, tanpa teks
penyebab. Capture ini **tidak** dihitung ke 60 kasus/gate; ia probe diagnostik
yang ditulis runner setelah suite. `summary.json` tetap 60/60, 0 kebocoran, 0
panic, 60/60 non-kosong.

Reproduksi tanpa fake provider:

```bash
<binary> run -p hi --model openai/fake --data-dir /tmp/f3-text \
  --base-url http://127.0.0.1:1/v1 --api-key dummy \
  --provider-attempts 1 --output text
# exit 2, stdout hanya newline, stderr kosong
```

Bandingkan dengan capture JSON yang memuat error sebenarnya, misalnya
[`captured/cred-missing.json`](captured/cred-missing.json) dan
[`captured/net-refused.json`](captured/net-refused.json).

Saran perbaikan (tidak dikerjakan di PR ini): di `cmd/damnbot/output.go:86-112`
cabang `text` hanya mencetak `r.answer`; ketika `r.err != ""`, tulis error itu
ke `stderr` (mis. `run: <error>` atau `stop_reason: <detail>`) agar exit
nonzero selalu punya pesan. Tambahkan satu kasus regresi setelah perilaku
diperbaiki; jangan mengubah `--output json`/`stream-json`.

Kasus `tool-args` memakai `[loop] max_iterations = 1`. Tool error muncul di
`stream-json`, lalu turn berhenti dengan `max_iterations` (exit 2). Jadi exit
nonzero berasal dari batas iterasi, sedangkan pesan yang dinilai adalah
`tool_result` (mis. `unknown_tool`, `invalid_args`, `outside_workspace`,
`denied`). Rater harus menilai baris `tool_result`, bukan baris
`stop_reason=max_iterations`.

## 4. Sanitasi

Placeholder yang dipakai di capture:

`{TMP}`, `{HOME}`, `{BIN}`, `{SERVER}`, `{PORT}`, `{USER}`, `{HOST}`,
`{SESSION_ID}`, `{REDACTED}`. Input kasus juga mendukung `{port}` (port fake
provider) dan `{server}` (base URL loopback).

Runner mengganti path workspace, home, path binary, alamat/port fake provider,
nama user/host, session id acak, dan nilai API key dummy. Nilai API key juga
tidak pernah ditulis ke capture (argumen `--api-key` menjadi `{REDACTED}`).
Capture hanya memuat output binary setelah sanitasi.

Pemeriksaan artefak yang dijalankan:

```bash
cd bench/f3/errors
grep -RInF "$HOME" . || true
grep -RInF "$(hostname)" . || true
grep -RInE 'sk-[A-Za-z0-9_-]{20,}|Bearer [A-Za-z0-9._~+/-]{8,}|api[_-]?key[=:]' . || true
grep -RInE 'panic:|goroutine ' captured || true
```

Hasil pada artefak ini: 0 path rumah (pola `$HOME` diperluas saat perintah dijalankan), 0 nama host, 0 pola secret, 0
`panic:`/`goroutine `. (Nama user tidak dicek dengan substring karena kata
Indonesia "dan" akan menjadi false positive; path rumah sudah mencakupnya.)

## 5. Cek otomatis dan hasilnya

Runner menjalankan empat gate dan menulis `summary.json`:

| Cek | Hasil |
|---|---|
| (i) exit code ≠ 0 untuk semua kasus | **60/60** |
| (ii) tidak ada pola secret di output tersanitasi | **0/60** |
| (iii) tidak ada `panic:`/`goroutine ` | **0/60** |
| (iv) pesan tidak kosong (≥ 1 baris) | **60/60** |

Angka ini adalah gate mekanis, bukan skor kualitas. Gate tidak menilai apakah
pesan menyebut penyebab atau langkah perbaikan.

## 6. Skor proxy (llm-proxy)

**Status: skor proxy, bukan ratifikasi manusia, bukan gate.** Angka di bawah
dihasilkan dua rater LLM independen A/B dengan rubrik v1
(`rubric.json`, SHA-256
`ff1938264c452fdb0bf7492f0dd705961c586610b48b7302e88555bad8c0576b`); kedua
rater tidak saling melihat. Skor mentah ada di
`~/.cache/damnbot/errors-scores-{a,b}.json` (di luar repo, read-only) dan
agregasinya di [`scores-proxy.json`](scores-proxy.json). Angka dihitung ulang
dari file, bukan disalin dari ringkasan siapa pun. `summary.json` gate
**tidak** memuat skor ini.

Skala 1–5; dimensi: a=`what_failed`, b=`why`, c=`what_to_do`,
d=`no_secret_leak`, e=`no_stack_for_usage`. Mean per dimensi pada 60 kasus:

| Dim | Rater A | Rater B | Rata-rata | Exact match | Mean abs diff |
|---|---:|---:|---:|---:|---:|
| a | 4,3667 | 4,3167 | 4,3417 | 78,33% | 0,2167 |
| b | 3,9000 | 3,8833 | 3,8917 | 71,67% | 0,2833 |
| c | 2,3167 | 2,1833 | 2,2500 | 68,33% | 0,3333 |
| d | 5,0000 | 4,9833 | 4,9917 | 98,33% | 0,0167 |
| e | 3,7167 | 3,7000 | 3,7083 | 81,67% | 0,1833 |

Overall: A **3,8600**, B **3,8133**, rata-rata **3,8367**; exact match kelima
dimensi sekaligus **35,00%** (21/60). Agreement tertinggi di `d` (MAD 0,0167),
terendah di `c` (MAD 0,3333). Ini menunjukkan `c` (langkah perbaikan) adalah
dimensi paling ambigu bagi kedua rater, bukan bukti bahwa pesan sudah baik.

**Kasus dengan selisih terbesar** (jumlah |A−B| lima dimensi; tie-break id
ascending):

| Kasus | Grup | Σ|A−B| | Dimensi yang berbeda |
|---|---|---:|---|
| `db-bad-magic` | db-corrupt | 3 | b, c, e |
| `db-bad-pagesize` | db-corrupt | 3 | b, c, e |
| `db-is-directory` | db-corrupt | 3 | b, c, e |
| `db-random-bytes` | db-corrupt | 3 | b, c, e |
| `db-truncated` | db-corrupt | 3 | b, c, e |

Ada **7 kasus** yang seri di Σ=3; `model-empty-provider` dan `tool-bad-json`
juga seri dan hanya tersisih oleh tie-break id. Detail semua kasus ada di
`scores-proxy.json`.

**Kasus `net-tls` tidak valid untuk agregat.** Capture lama memakai literal
`{port}` yang tidak tersubstitusi, sehingga yang diuji adalah parse error
`invalid port`, bukan TLS (catatan kedua rater menyebutnya). Runner sudah
diperbaiki: capture baru memakai `https://127.0.0.1:{PORT}/ok/v1` dan
menghasilkan `http: server gave HTTP response to HTTPS client`. Skor A/B lama
untuk `net-tls` disimpan sebagai baris historis tetapi **dikecualikan** dari
basis `valid_cases` (59 kasus) sampai dinilai ulang. Pada basis 59 kasus,
mean gabungan a/b/c/d/e = 4,3475 / 3,8898 / 2,2542 / 4,9915 / 3,7203 dan
exact-match kelima dimensi 35,59%; perbedaannya kecil, tetapi tetap dilaporkan
agar tidak mencampur capture yang salah sasaran.

**Snapshot capture.** Capture tersanitasi dan skor A/B di PR ini dihasilkan
pada binary commit `42ad2b10` (sebelum rebase PR). Commit F3 setelahnya dapat
mengubah sebagian pesan; jalankan `run-errors.sh` pada commit target dan
lakukan re-rating, jangan mencampur snapshot. Field `capture_snapshot` di
`scores-proxy.json` mencatat commit itu.

**Batasan eksplisit.**

- Ini **bukan ratifikasi manusia**, **bukan gate**, dan **bukan** keputusan
  `LULUS` final. `LULUS` menunggu penilai manusia independen.
- Target SCORECARD ≥ 95% belum diadjudikasi; angka ini hanya sinyal proxy.
- Kesepakatan A/B mengukur konsistensi pembacaan rubrik, bukan kebenaran
  pesan. Ambiguitas rubrik v1 didokumentasikan di
  [`rubric-notes.md`](rubric-notes.md); `rubric.json` v1 tidak diubah agar
  skor tetap sebanding.
- `net-tls` memerlukan re-rating; skor lamanya tidak boleh dipakai untuk
  menyimpulkan kualitas TLS.

## 7. Reproduksi

Prasyarat: Linux, user non-root (kasus izin memakai `chmod`; root dapat
melewatinya), Go 1.26.10, `python3` tidak diperlukan, tanpa kredensial berbayar. Jalankan dari root repo:

```bash
unset GH_TOKEN
cd bench/f3
ulimit -v 4194304
nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 ./run-errors.sh
```

Atau dua langkah eksplisit:

```bash
# 1. build binary asli
CGO_ENABLED=0 go build -tags goolm -o /tmp/damnbot-f3 ./cmd/damnbot

# 2. jalankan suite (menulis captured/*.json dan summary.json)
cd bench
go run ./f3/cmd/f3errors \
  --bin /tmp/damnbot-f3 \
  --cases f3/errors/cases.json \
  --out f3/errors
```

Durasi suite pada mesin ini ±59 detik; `timeout-ttfb` menghabiskan ±30 detik
dan `timeout-dial` ±10 detik. `duration_ms` di capture akan berubah antar-run;
`exit_code`, `stdout`, dan `stderr` adalah bahan yang dinilai.

## 8. Menghitung skor bila rater mengisi

Rater memberi skor integer 1–5 untuk lima dimensi di `rubric.json` pada setiap
kasus, tanpa melihat skor rater lain:

1. `what_failed` — apa yang gagal.
2. `why` — mengapa/konteks.
3. `what_to_do` — langkah perbaikan konkret.
4. `no_secret_leak` — tidak membocorkan rahasia/data.
5. `no_stack_for_usage` — tidak ada stack trace/panic.

Bentuk isian yang disarankan (satu objek per kasus):

```json
{
  "id": "cfg-range",
  "rater": "R1",
  "scores": {
    "what_failed": 4,
    "why": 4,
    "what_to_do": 4,
    "no_secret_leak": 5,
    "no_stack_for_usage": 5
  }
}
```

Perhitungan (detail di `rubric.json`):

- `per_case_mean` = mean lima dimensi, satu desimal.
- `informative` = `what_failed ≥ 3` **dan** `why ≥ 3` **dan**
  `what_to_do ≥ 3` **dan** `no_secret_leak ≥ 4` **dan**
  `no_stack_for_usage ≥ 4`.
- `informative_pct` = `100 × jumlah kasus informative / 60`. Angka ini yang
  dibandingkan dengan target SCORECARD ≥ 95%.
- `unsafe` = `no_secret_leak ≤ 2` atau `no_stack_for_usage ≤ 2`; laporkan
  terpisah, jangan dirata-ratakan.
- Dua rater: hitung `informative_pct` masing-masing, laporkan rata-rata dan
  selisih. Kasus dengan selisih > 1 pada dimensi mana pun diadjudikasi rater
  ketiga; jangan merata-ratakan sebelum adjudikasi.
- Laporkan per grup (n=5): mean, jumlah informative, daftar unsafe. Jangan
  melaporkan satu angka tanpa dua angka rater.

## 9. Batasan jujur

- **Ini bukan skor.** Tidak ada nilai kualitas pesan yang dikarang di repo ini.
- Penilaian kualitas tetap membutuhkan rater manusia dan rubrik di atas.
- Binary yang diuji adalah permukaan F1; kelompok F3 (TUI, ACP, setup,
  importer, sesi) belum ada. Suite ini menutup permukaan yang **dapat**
  dipicu sekarang dan mencatat penggantiannya, bukan mengklaim cakupan F3
  penuh.
- Fake provider loopback hanya meniru error HTTP/SSE yang relevan; ia bukan
  provider sungguhan dan tidak menguji latency jaringan nyata.
- `timeout-retry` memuat dua timeout nyata (TTFB, dial) dan tiga kegagalan
  retry (429/500/503). Idle-timeout 60 detik tidak dimasukkan agar suite tetap
  di bawah satu menit.
- Capture adalah snapshot; pesan binary dapat berubah saat kode berubah.
  Generate ulang dengan perintah di §7, lalu simpan capture baru.
