# REVIEW-F2-SEC14 — Review keamanan recall OR, seam injeksi `run`, dan harness `eval recall` (ADR-137/138)

Review independen untuk tumpukan #228 → #229 → #230 (basis `main` `3387c2f`). Reviewer tidak menulis kode yang direview, dan kode produk tidak diubah. Tanggal: 2026-10-22.

## Round 1 — 2026-10-22

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #228 recall OR + `ctxdb.FTSNorm` (ADR-137) | `2c19ccd` | **APPROVE dengan MINOR** (SEC14-5) |
| #229 seam provider/config/retriever | `ab19958` | **APPROVE** |
| #230 harness `eval recall` + ledger (ADR-138) | `e516a2d` | **REQUEST CHANGES**: SEC14-1 (MEDIUM: cap ledger dapat dilewati oleh respons tanpa usage), SEC14-2 (MEDIUM: model menulis file di repo tanpa approval), SEC14-3/4/6 (MINOR) |

Gate di `e516a2d`: `go test -race -tags goolm -count=1 ./cmd/damnbot ./internal/ctxdb/memstore ./internal/memory ./internal/archtest` → ok; `./internal/ctxdb -race -run 'FTS|Search|Recall'` → ok; `go vet ./...` + `GOOS=windows|darwin ./cmd/damnbot` bersih; `make deadcode` 0; `make loc` `ctxdb 3429/3500`. `git check-ignore`: `bench/h2h/results/<run>/data/**` ter-ignore.

Probe `TestZZSec14*` (`cmd/damnbot`, `e516a2d`) dan `TestZZSec14Main` (`main` `3387c2f`, sebagai pembanding) dihapus setelah dijalankan.

### #228 — kandidat recall OR

| Probe (`memstore.Search`) | Hasil |
|---|---|
| Korpus: `user` approved "my locker number is 42"; `gateway:telegram:bob` approved 99; `project:/x` approved 7; `user` pending 13 / rejected 66 / approved-expired 77; 50 filler `user` | — |
| `Scopes=[user]`, "What is my locker number?", limit 10 | 42 di peringkat 1, sisanya filler `user`. **Tidak ada** bob/project/pending/rejected/expired ✔ |
| `Scopes=[user]`, status default | hanya `approved` ✔ |
| Tanpa scope | 0 baris (JN3-1, tetap deny) ✔ |
| `Scopes=[gateway:telegram:bob]` | hanya baris bob ✔ |
| Injeksi: `locker" OR "bob`, `NEAR(locker bob)`, `content:bob`, `scope:gateway`, `bob*`, `-locker`, `^my`, `OR`, `AND NOT`, NUL, `locker OR bob` | tanpa error syntax; tidak ada baris lintas scope; operator menjadi term (`OR` → 0 hasil) ✔ |
| 20.000 term (5 kata diulang), limit 10 | **7,8 s** (PR) vs **0,9 s** AND di `main` → SEC14-5 |

Kode: `ftsQuery` menyisakan hanya rune huruf/angka setelah `ctxdb.FTSNorm`, lalu setiap token di-quote dan digabung dengan ` OR `. Tidak ada karakter `"`, `*`, `:`, `(`, atau `^` yang bisa lolos. Klausa scope, status, `expires_at`, dan purge di `searchIDs` tidak diubah (diff hanya mengubah `MATCH dmb_fts_norm(?)` → `MATCH ?`). Volume recall ke prompt tetap terbatas: `memory.Service.Recall` meng-clamp limit ke `DefaultRecall`/`MaxRecall`, dan assembler memotong memori di `CapMemory` = 300 token. OR hanya memperluas *kandidat* di dalam scope dan status yang sama.

### #229 — seam injeksi

`prov`, `cfgOverride`, dan `retrieverFor` adalah field **tak-ekspor** `runOptions` yang tidak terhubung ke flag atau env (grep: satu-satunya penulis non-test adalah `eval.go:432`). `damnbot run` tidak bisa menukar provider atau config saat runtime. `os.Getenv` di `providerEndpoint` (`<PROV>_API_KEY`) sudah ada sebelumnya. Jalur produksi yang memakai seam hanya subperintah eksplisit `damnbot eval recall`, yang dibahas di #230. ✔

### #230 — harness `eval recall`

| Probe | Hasil |
|---|---|
| Provider palsu mengembalikan `Reply` **tanpa usage**; cap 0,05; 500 call (batas worst-case 1,45 USD) | **500 call lolos, `spent` tercatat 0,000000**, tidak pernah berhenti → SEC14-1 |
| Provider error terus-menerus; cap 0,01 | berhenti setelah 4 percobaan, masing-masing dicharge worst-case ✔ |
| Model memanggil `write_file{"path":"zz_sec14_pwned.txt"}` dalam satu langkah eval | **file tertulis di cwd proses** (checkout repo), tanpa approval → SEC14-2 |
| Task `id: "../../victim"` | `os.RemoveAll(<out>/data/../../victim)` **menghapus direktori di luar `--out`** → SEC14-3 |
| Dua proses membuka ledger yang sama (cap 1,0), masing-masing reserve+settle 0,7 | keduanya lolos; belanja nyata 1,4, file mencatat **0,70** (lost update) → SEC14-4 |
| Mode file ledger | `0600` ✔ |
| `summary.json` live yang di-commit + `f2-spend.json` | tidak memuat key, `Bearer`, URL, atau error. Hanya skor, jawaban, call, dan biaya ✔ |

Kunci API: dibaca dari `--key-file` di `liveProvider` saat runtime, di-`TrimSpace`, dan hanya masuk `provider.Route.APIKey`. Error hanya mencetak *path*. Kunci tidak disimpan di `evalOptions`, `summary.json`, ledger, atau ctxdb per task (`runOptions.apiKey` tidak diisi, karena provider diinjeksi). ✔ Anotasi `#nosec G304` untuk ledger dan key file sah karena path berasal dari user lokal sendiri. Untuk `loadEvalTasks`, komentar menyebut "argument" padahal path-nya tetap `bench/evals/v1/E4.jsonl`, relatif terhadap cwd (lihat SEC14-6).

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC14-1 | **MEDIUM** | confirmed | **Cap ledger dilewati oleh respons tanpa usage.** `evalProvider.Stream` me-reserve worst-case, tetapi `settle` memakai `usageCost(reply.Usage)`. Bila upstream tidak mengirim usage (router tanpa `stream_options.include_usage`, stream terpotong yang tetap berstatus sukses) atau mengirim `cost: 0`, biaya tercatat 0 dan `reserve` berikutnya selalu lolos. Probe: 500 call, `spent` 0. Ini bertentangan dengan klaim ADR-138 butir 7 ("cap tidak bisa dilewati"). | Settle `max(actual, estimasi)`. Bila `Input == 0 && Output == 0`, atau `CostUSD` nil/0 sementara token > 0, charge worst-case yang sudah di-reserve (fail-closed). Tambahkan test provider tanpa usage. |
| SEC14-2 | **MEDIUM** | confirmed | **Eval meng-auto-approve tulis file model di checkout repo.** `runE4Step` memaksa `approval: "auto"` (`ModeHeadlessAuto`: tulis workspace auto-approved), `cfgOverride = config.Default()` (mengabaikan konfigurasi user), dan cwd/roots = cwd proses (root repo, karena path eval relatif). Model remote (default router pihak ketiga) dapat menulis/mengedit file repo tanpa opt-in. Probe: `write_file` membuat file di cwd. Tugas E4 hanya tanya-jawab dan tidak butuh tool tulis. | Pakai `approval: "ask"` (`ModeHeadless` → tulis ditolak) dan jalankan langkah dengan cwd/roots = `<out>/data/<id>`. Tambahkan test: `write_file` dari model ditolak dan tidak ada file di luar data dir. |
| SEC14-3 | MINOR | confirmed | `os.RemoveAll(filepath.Join(outDir, "data", t.ID))` memakai `id` dari file eval tanpa validasi. `../..` menghapus direktori di luar `--out` (probe). Sumbernya file repo, tetapi PR yang mengubah `E4.jsonl` cukup untuk menghapus file developer yang menjalankan eval. | Validasi `id` (`^[A-Za-z0-9._-]+$`, bukan `.`/`..`) dan pastikan path hasil `Join` tetap di bawah `<out>/data`. |
| SEC14-4 | MINOR | confirmed | Ledger tanpa kunci lintas proses: dua run paralel sama-sama membaca `spent`, sama-sama lolos cap, dan penulis terakhir menimpa (probe: belanja 1,4 dengan cap 1,0; file 0,70). | Lock file (`O_EXCL`/flock) selama run, atau re-read + compare sebelum setiap `reserve`/`settle`. |
| SEC14-5 | MINOR | confirmed | **#228:** ekspresi OR tidak men-dedupe dan tidak membatasi jumlah term. Kueri 20.000 term (5 kata diulang) butuh 7,8 s, dibanding 0,9 s untuk AND di `main`. Kueri recall = pesan user pertama, dan kelak termasuk pesan gateway dari user remote, sehingga input panjang menjadi biaya CPU per sesi. | Dedupe token dan batasi (mis. 64 term unik pertama) sebelum `strings.Join(..., " OR ")`. Tambahkan test untuk batas tersebut. |
| SEC14-6 | MINOR | open | Kebersihan: (a) `res.Err` (teks error provider/turn) masuk `summary.json` tanpa `redact`, padahal redactor tersedia; (b) `--out` di luar `bench/h2h/results/*/` membuat `data/` (SQLite berisi isi percakapan) tidak ter-ignore; (c) izin key file tidak diperiksa (0644 diterima); (d) ADR-138 butir 9 menyebut `summary.json` memuat harga, padahal tidak; (e) komentar `#nosec G304` di `loadEvalTasks` keliru (path tetap relatif cwd, bukan argumen). | Redact `Err`; peringatkan bila `--out` tidak ter-ignore, atau tulis `data/` ke temp dir; tolak key file yang bisa dibaca group/other; selaraskan ADR dan komentar. |

**Ringkasan:** #228 **APPROVE dengan MINOR**: OR tidak memperluas recall lintas scope atau status, tidak ada injeksi operator FTS, dan volume ke prompt tetap dibatasi limit + `CapMemory`. Sisa SEC14-5: dedupe dan cap jumlah term. #229 **APPROVE**: seam tidak dapat diatur lewat flag atau env. #230 **REQUEST CHANGES**: kunci API hanya dipakai saat runtime dan artefak yang di-commit bersih, tetapi cap ledger lolos ketika usage kosong (SEC14-1) dan model dapat menulis file repo tanpa approval (SEC14-2). Path traversal saat penghapusan data dir (SEC14-3) dan lost update ledger (SEC14-4) sebaiknya ikut diperbaiki di putaran yang sama.
