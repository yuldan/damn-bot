# REVIEW-F1-SEC3 — Review keamanan pasca-merge: provider, budget/ledger, netx DLP putaran 3

Tanggal: 2026-10-19. Reviewer independen, bukan penulis PR.
- Kode yang direview **sudah di-merge** sebelum review (PR #22, #30, #31, #20).
- Basis review: `main` @ `aaa7a6d`, turunan dari `efe3cad`.
- Kode tidak diubah. Eksperimen dijalankan di salinan `/tmp` yang dihapus setelah selesai, dan tidak ada secret yang dicetak.

Severity untuk kode yang sudah di-merge:
- **BLOCKER**: bug yang bisa dieksploitasi lewat input jarak jauh, sehingga perlu task perbaikan segera.
- **MAJOR**: wajib diperbaiki sebelum acceptance F1.
- **MINOR**: errata.

| Area | Verdict | BLOCKER | MAJOR | MINOR |
|---|---|---:|---:|---:|
| `internal/provider` + `providertest` (#22, #30, #31; ADR-072/082/102) | **perbaikan wajib** | 1 | 5 | 8 |
| `internal/budget` + `internal/ctxdb/ledgerstore` (#20; ADR-101/074) | **perbaikan sebelum acceptance F1** | 0 | 3 | 4 |
| netx DLP #28 (`8a0cb66`; NETX-15/16) | **changes required** | 1 | 0 | 3 |

## `internal/provider` (input jarak jauh tidak tepercaya)

Perintah yang dijalankan:
- `go test -race -count=1 ./internal/provider/... ./internal/netx/...`: ok.
- `FuzzDeltaDecode` 330 s (5,08 juta eksekusi) dan `FuzzSSEParse` 200 s (1,24 juta): PASS. Celah diferensial di B1 dan m1 baru tertangkap setelah ditambahkan seed manual.
- `GOOS=windows|darwin go vet`: ok.
- Run arm64 lewat `qemu-aarch64`.
- 20 mutan.
- Test adversarial di salinan: nesting dalam, fragmen nama, `{}`, 10⁶ chunk argumen, index 1e9/2³¹/2⁵³/1e300/−1, duplikat key, reasoning parts, redirect, fallback gagal, varian Retry-After.

| ID | Severity | Temuan | Bukti / repro | Rekomendasi |
|---|---|---|---|---|
| PROV-B1 | **BLOCKER** | Rekursi tanpa batas di `jscan.skip` lewat `object`/`array`, dijangkau dari setiap key yang diabaikan dan dari nilai reasoning non-string. Satu event SSE `{"x":` + 4.194.293 × `[` masih di bawah cap event 4 MiB, tetapi butuh stack > 512 MiB di arm64. Go menaikkan stack ke 1 GiB, yang melewati batas, sehingga terjadi `fatal error: stack overflow`. Fatal ini tidak bisa di-recover dan mematikan seluruh proses, termasuk semua sesi dan gateway. Target arm64 didukung (Apple Silicon, `ubuntu-24.04-arm`, `windows-11-arm`, RPi). Di amd64, kedalaman 4,19 jt butuh 512 MiB stack dan 5–8 s CPU per event. Ini juga celah diferensial: `encoding/json` menolak kedalaman > 10.000. | `internal/provider/jscan.go:237-245`; `delta.go:61`, `:71`, `:75`, `:117`.<br>End-to-end `providertest` → `OpenAI.Do` → `readStream` → `Decode` di `qemu-aarch64`: `goroutine stack exceeds 1000000000-byte limit / fatal error: stack overflow`.<br>Dikonfirmasi ulang oleh auditor di amd64: kedalaman 1.000.000 → `StackInuse=128 MiB`. | Tambahkan penghitung kedalaman di `jscan` dengan batas 10.000 (sama dengan `encoding/json`) → `errJSON`, atau buat `skip` iteratif. Tambahkan test frame berukuran maksimum (jalankan juga di runner arm64) dan seed fuzz dengan kedalaman 10.001. **Diteruskan ke Main sebagai task perbaikan segera.** |
| PROV-M1 | MAJOR | Penggabungan nama tool kuadratik: `c.name += string(t.Name)` per fragmen. Satu event 4 MiB memuat sekitar 116 ribu fragmen. Cap 16 MiB menghitung byte, bukan biaya CPU. | `assemble.go:106`. 200k fragmen: 13,6 s / 19,8 GiB dialokasi. Event 4 MiB dengan nama 1 MiB: 27,7 s CPU / 120 GiB. Pola ini berulang sampai idle watchdog (60 s, retryable) memutus, lalu diulang ≤ 3× per model dan per fallback. | Kumpulkan nama dalam `[]byte` lewat `append` (seperti `args`), dan batasi panjang nama (mis. 256 B → `ErrResponseTooLarge`). |
| PROV-M2 | MAJOR | Entri tool call kosong `{}` menjadi `ToolDelta` 88 B dan menambah 0 ke `a.size`, sehingga tidak satu pun cap terpicu. | `delta.go:142`, `:174`. Satu frame 4 MiB berisi sekitar 1,4 jt `{}`: dekode mengalokasi 636 MiB dan menahan 126 MiB, tanpa error. Bisa dikirim berulang tanpa batas. | Batasi jumlah entri tool call per chunk (mis. 256), dan hitung setiap fragmen ke budget assembler. |
| PROV-M3 | MAJOR | Mode "parts" pada reasoning menyimpan isi tiga kali (`quote(d.Reasoning)`, `reasonText`, `joinParts`/`quote` di `result()`), padahal `a.size` hanya menghitung byte mentah. Escape `\u0001` mengembang 6×. Hasilnya sekitar 40× cap 16 MiB. | `assemble.go:59`, `:125-129`. 25 event terhitung 16 MiB, tetapi heap 179 MiB sebelum `result()` dan 637 MiB sesudahnya. Selain itu `Response.Reasoning` di-replay ke provider. | Hitung ukuran yang benar-benar disimpan ke `maxResponseBytes`, atau bangun `Reasoning` sekali saja di `result()`. |
| PROV-M4 | MAJOR | Client Inference netx tidak punya `CheckRedirect`. Pada 307/308 ke host lain, Go hanya membuang `Authorization`/`Cookie`; header kredensial kustom `Route.Headers` (Azure `api-key`, `x-api-key`, kasus EC-SEC-005) dan seluruh body prompt ikut dikirim. Redirect https → http di host yang sama mempertahankan Bearer token dalam plaintext. | `internal/provider/client.go:222-224`; tidak ada `CheckRedirect` di `internal/netx`. Repro: provider `httptest` membalas 307 ke `http://localhost:<evil>/steal`. Host evil menerima `api-key`, `X-Api-Key`, dan body `{"model":"m","messages":[…`. | Untuk Purpose Inference: `CheckRedirect` → `http.ErrUseLastResponse`, dan 3xx diperlakukan sebagai `bad_request` non-retryable. Minimal, tolak redirect yang mengganti host atau skema. Tambahkan test. |
| PROV-M5 | MAJOR | Fallback yang gagal tidak terlihat. `OnFallback` hanya dipanggil bila fallback sukses, padahal prompt lengkap sudah terkirim ke provider kedua. P7 menuntut 100% fallback terlihat. | `client.go:97-101`. Repro: primary 500 × 3 dan fallback 500 × 3 → fallback menerima 3 request, `OnFallback` 0 event. | Picu event (dengan route) sebelum setiap percobaan fallback, lalu laporkan hasilnya terpisah. |
| PROV-m1 | MINOR | Duplikat key di-merge, bukan last-wins: `tool_calls` berulang, `choices` berulang, atau `delta` bersama `message`. Akibatnya klaim diferensial vs `encoding/json` gagal, dan tool call bisa tersembunyi dari parser last-wins. | Seed `{"choices":[{"delta":{"tool_calls":[{…"name":"x"}],"tool_calls":[]}}]}` → jscan 1 call, `encoding/json` 0; `FuzzDeltaDecode` FAIL. | Terapkan last-wins (reset slice) atau tolak duplikat key yang dikenal. Tambahkan seed ke korpus fuzz. |
| PROV-m2 | MINOR | `finish_reason` last-wins: `content_filter` lalu `tool_calls` menghasilkan `tool_calls`, sehingga call tetap dieksekusi. Finish tak dikenal (`SAFETY`, `refusal`, `error`) dipetakan ke `stop`. | `assemble.go:73`, `:156` | Jadikan `content_filter` sticky, dan petakan `error`/nilai tak dikenal ke kelas error. |
| PROV-m3 | MINOR | `"error": null` di chunk yang valid diperlakukan sebagai error `server`: 3 retry lalu fallback. | `client.go:260` | Perlakukan `null` sama dengan tidak ada. |
| PROV-m4 | MINOR | `Body` error in-stream bisa sampai 4 MiB, padahal doc menyebut ≤ 64 KiB. Kelasnya selalu `server`, apa pun `error.code`-nya. | `types.go`; `client.go:261` | Potong ke 64 KiB, dan klasifikasikan lewat `error.code` seperti `toError`. |
| PROV-m5 | MINOR | `Code` dari server (≤ 64 KiB, termasuk ANSI/newline) dan nama tool dengan karakter kontrol diteruskan mentah ke `Error()` dan nama. Belum ada konsumennya. | `client.go`; `assemble.go` | Sanitasi `Code` dan batasi ke 128 B karakter printable. |
| PROV-m6 | MINOR | `Retry-After` di atas `uint32` di-parse sebagai 0, sehingga request di-retry dengan backoff alih-alih `ErrRetryAfterTooLong` + fallback. | `internal/netx/errors.go:102` | Parse sebagai `uint64` dan saturasikan. |
| PROV-m7 | MINOR | Tidak ada deadline total untuk stream: delta kosong mereset idle timer, sehingga turn tidak pernah berakhir tanpa `loop.max_wall_s` (belum ada). | `client.go` | Bergantung eksplisit pada wall clock loop (acceptance F1), atau tambahkan cap jumlah event / waktu total. |
| PROV-m8 | MINOR | Mutan yang lolos suite: kasus `{"a":"\x01"}` ternyata literal backtick sehingga kontrol tidak teruji; EC-PROV-013 hanya menguji `stripControl` per unit (panggilan di `result()` boleh dihapus); dimensi kredensial pada breaker tidak terkunci; "non-retryable tidak fallback" tidak teruji; cost `Inf` diterima. | `delta_test.go:182`; mutasi | Tambahkan kasus literal `"\x01"`, test end-to-end `stripControl`, test breaker dua kredensial, dan test auth + fallback. |

Diperiksa tanpa temuan:
- **Param drop:** hanya terjadi bila `error.param` menyebut `temperature`/`reasoning_effort` yang benar-benar dikirim. `tools`, `max_tokens`, `stream`, dan `stream_options` selalu ditulis, sehingga server tidak bisa memaksa parameter keamanan dilepas. Re-send dibatasi ≤ 2.
- **Retry:** ≤ 3 per model. `Retry-After` ≤ 30 s dihormati; di atasnya `ErrRetryAfterTooLong` lalu fallback. Format HTTP-date dan nilai negatif tertangani. 429 = `rate_limited`.
- **Fallback dan breaker:** fallback ≤ 2, hanya untuk kelas retryable. Breaker per provider+kredensial, dan circuit yang terbuka menolak lokal.
- **Batas ukuran:** event 4 MiB (`ErrEventTooLarge`), respons 16 MiB / 128 call (`ErrResponseTooLarge`), keduanya tanpa retry atau fallback.
- **`content_filter`:** dikembalikan tanpa retry atau fallback, dengan konten parsial dipertahankan.
- **Input rusak:** frame tidak lengkap atau tanpa `finish_reason` → `stream_broken`. UTF-8 rusak → U+FFFD. Angka token di luar jangkauan ditolak. Kapitalisasi key harus persis. Kontrol mentah dan NUL ditolak.
- **Index tool call:** nilai besar tidak memicu alokasi; −1, 0.5, dan 1e300 ditolak. 10⁶ chunk argumen: 1,0 s dengan alokasi total 4 MiB.
- **Logging:** paket provider tidak menulis log (P29). `Error()` tidak memuat body.
- **EC-PROV:** 001, 004..007, 011, 012, 015, 016, 025 teruji non-vakum (lewat mutasi atau skrip). 013 baru parsial (m8).

## `internal/budget` + `internal/ctxdb/ledgerstore` (ledger)

Saat ini belum ada kode produksi yang menulis ledger (pemanggil `ledgerstore`/`Cost`/`Account`/`Estimator` hanya ada di test). Karena itu temuan di bawah berstatus MAJOR, bukan BLOCKER, tetapi wajib ditutup sebelum guard P27 dan acceptance F1.

| ID | Severity | Temuan | Bukti / repro | Rekomendasi |
|---|---|---|---|---|
| LED-1 | MAJOR | `TestLedgerAppendOnly` vakum untuk UPDATE: callback mengembalikan `fmt.Errorf("update: %w", uerr)`, yang tidak pernah nil. DELETE tidak diuji sama sekali. Klaim F1-REPORT "UPDATE ditolak" tidak dibuktikan oleh test ini. | `internal/ctxdb/ledgerstore/ledgerstore_test.go:121-125`. Mutasi: trigger `usage_no_update`/`usage_no_delete` (`migrations/0001_core.sql:306-307`) dihapus → suite `ledgerstore` dan `ctxdb` tetap lulus. | Kembalikan `uerr` apa adanya atau cek `errors.Is(err, ctxdb.ErrInternal)`, lalu tambahkan kasus DELETE. |
| LED-2 | MAJOR | `usage.cost` dari provider dengan nilai finite raksasa (≤ 1,8e308) dikonversi `int64(math.Round(usd*1e6))`. Konversi ini tidak terdefinisi, dan di amd64 hasilnya `MinInt64` dengan `cost_source=provider`. Σ ledger jadi negatif, sehingga gateway yang jahat atau dibobol bisa melewati semua cap biaya begitu P27 ada. | `internal/budget/ledger.go:51-52`; `provider/delta.go:196-200`. `Cost(Usage{CostUSD:&1e14})` → `-9223372036854775808`. | Terima biaya provider hanya bila `0 ≤ usd ≤` batas wajar. Di luar itu → `ErrInvalidUsage`, atau fallback ke tabel harga / unknown. |
| LED-3 | MAJOR | Jumlah token tanpa batas atas: perkalian `tokens × micros/MTok + 999_999` overflow diam-diam, sehingga biaya negatif atau mendekati 0 (melanggar "bulatkan ke atas" ADR-101). | `ledger.go:60-61`; `delta.go:189-191`. Pada 3 USD/MTok: `Input=4e12` → `-6446744073708`; `MaxInt64/2` → `-2`. | Batasi token (mis. ≤ 1e12 → `ErrInvalidUsage`), atau pakai aritmetika yang dicek overflow (`bits.Mul64`) dengan saturasi ke atas. |
| LED-4 | MINOR | `Estimator.Observe`/`NewEstimator` menerima `real` negatif atau raksasa sebagai anchor, sehingga estimasi menjadi negatif atau overflow (berdampak ke P8 dan pre-check budget nanti). | `internal/budget/estimate.go:109-111`, `:131`. `Observe(MaxInt64,1)` → estimasi `-9223372036854775659`. | Clamp atau abaikan nilai di luar batas, saturasikan penjumlahan, dan validasi usage di batas provider. |
| LED-5 | MINOR | DDL tidak punya `CHECK(>=0)`, sehingga baris bertoken/biaya negatif diterima. `SessionTotals` mengembalikan error saat SUM overflow. | `ledgerstore.go:46-70`, `:108-115`; `0001_core.sql:94-116` (= SPEC §11.2) | LED-2/3 membatasi input. Opsional lewat ADR: `CHECK(x >= 0)` di SPEC §11.2 dan migrasi. Error totals → P27 harus fail-closed. |
| LED-6 | MINOR | Satu sampel outlier (real/est = 1e12) mengunci faktor kalibrasi di 1,5 untuk seluruh jendela 50 sampel, karena clamp hanya diterapkan setelah EMA. Arahnya aman (estimasi berlebih). | `estimate.go:137-143` | Clamp rasio per sampel ke [0,5; 1,5] sebelum masuk EMA. |
| LED-7 | MINOR | Guard `guard_usage_ins` mengecek kesamaan sesi, tetapi tidak mengecek `kind = 'llm_call'` event yang dirujuk (P9). | `migrations/0008_guards.sql:59-63` | Opsional: tambahkan cek kind ke guard. |

Diperiksa tanpa temuan:
- **Append-only ditegakkan trigger DB** (`usage_no_update`/`usage_no_delete`). Satu-satunya pengecualian adalah `dmb_maint()='purge'`.
- **Guard lintas sesi** di `guard_usage_ins`: mutasi menghapusnya membuat test gagal, jadi test ini nyata.
- **DDL identik dengan SPEC §11.2.**
- **Pembulatan tabel harga ke atas** dalam rentang valid.
- **Harga unknown:** disimpan `NULL` + `cost_source='unknown'`, dihitung sebagai `UnknownCost` dan tidak dianggap 0.
- **Validasi usage:** token negatif atau `cached > input` → `ErrInvalidUsage`. Cost negatif/NaN/Inf → fallback.
- **Transaksi:** `Insert` berjalan di transaksi milik pemanggil. Probe konkuren 200 × `Insert` + `SessionTotals` dengan `-race`: total konsisten.
- **Kalibrasi:** `prompt_tokens=0` diabaikan.

## netx DLP #28 (`f1-netx-dlp2` @ `8a0cb66`): putaran 3 NETX-15/16, changes required

PR ini menulis ulang gerbang DLP demi kinerja: prefilter gram stride-4, decode ulang hanya pada span kandidat, probe stride untuk secret pendek, dan buffer pool.

Perintah yang dijalankan:
- `go test -race ./internal/netx/... ./internal/redact/...`: ok.
- CI 24/24 hijau.
- `BenchmarkDLPScan -count=5 -cpu=1` (12 kasus).
- Oracle diferensial terhadap `dlp.go` di `main`, total 288.526 kasus, 0 regresi:

  | Kelompok probe | Jumlah |
  |---|---:|
  | Set probe bypass putaran 1/2 | 6.512 |
  | Secret pendek 1–7 B × offset 0–39 × 5 filler × 5 encoding | 42.014 |
  | Split per karakter di batas layer | 40.000 |
  | Acak | 200.000 |

- 48 probe jendela: 28 regresi.
- `FuzzDiff` 125 s menemukan regresi JWT secara mandiri.
- Isolasi pool: 16 goroutine × 3.000 scan dengan `-race`, 0 verdict salah.

| ID | Status / Severity | Temuan | Bukti / repro | Rekomendasi |
|---|---|---|---|---|
| NETX-15 | **closed** | `next=QueryEscape²(url)`, `?v=%252541`, dan `"C++ … 50%25ab"` di 512 B–64 KiB kini lolos (di `main`: `dlp_limit` mulai 1.500/2.048 B). Batas kerja = (1+3)·len menurut lapisan decode. `TestDLPBounds` sudah diperbarui. | `internal/netx/dlp.go`; `dlp_test.go` | — |
| NETX-16 | **closed (toleransi ×1,5, margin tipis)** | i7-1255U `-cpu=1`, dalam µs: plain 13–28; pct (50/200 secret) 45–47; 50 secret pendek + pct 51–54; decode 67–83; repro plain+`+` 28, plain+`%2F` 28, hex-heavy 34–40. CI EPYC menurut PR: pct 65–74 µs (≤ 75 = 50 × 1,5). PR sendiri memperkirakan 90–120 µs di runner Xeon yang lebih lambat. | Benchmark | Diterima sesuai SPEC §17/ADR-077 (target persis diukur di Q14 saat RC). Pantau job `perf` di runner lambat. |
| **NETX-26** | **BLOCKER** | Regresi deteksi. `scan()` memeriksa lapisan hasil decode hanya dalam ±`d.region` (= max(256, bentuk terpanjang)) di sekitar setiap byte yang di-decode. (1) Jendela dihitung dalam byte sebelum fold, sedangkan pencocokan berjalan setelah fold membuang CR/LF, sehingga secret yang disisipi CR/LF melampaui jendela. (2) Pola §14.3 `jwt` (segmen tanpa batas) dan `url_password` (`://` … `@` terakhir) bisa butuh konteks > 256 B. Asumsi `dlpPatternReach` ("match terpendek < 256") tidak berarti setiap match muat dalam jendela. JWT dengan payload > 256 B adalah hal biasa untuk access token OAuth. | `dlp.go:236-281`, `:372-403` (`spanTrack`), `dlpPatternReach`. Lewat `check()`, secret 33 B, semua diblok `main` tetapi lolos di #28:<br>• query `t=eyJhbGciOiJIUzI1NiJ9.<payload 312 B>%2E<sig>`<br>• body `u=postgres://admin:hunter2<260×x>%40db.internal/app`<br>• body `%73ec` + 260×`\n` + `ret_tok_value_…`<br>Regresi jendela: 28/48 pada pad ≥ 200–260 B (raw/hex/base64 dengan LF/CRLF, JWT dengan `.` ter-encode, userinfo dengan `%40`/`%3A`). `FuzzDiff`: `"eyJ00000000.<~330×0>%2E00000000"`. | Hitung jendela dalam byte setelah fold (perluas melewati CR/LF sampai `region` byte non-CR/LF). Untuk pola, perluas span ke run non-whitespace yang melingkupinya plus reach, atau jalankan `d.pat.Redact` atas seluruh lapisan decode dan pakai jendela hanya untuk matcher secret. Kedua opsi tetap ≤ 4·len. Tambahkan test regresi untuk konteks > 256 B. Koreksi klaim "tanpa melemahkan deteksi" di ADR-100 butir 8 dan body PR. |
| NETX-27 | MINOR | `matchShort` kini memakai budget verifikasi (di `main`: `bytes.Contains` tanpa budget). ≥ 50 secret 2–7 B yang berbagi 2-gram menyebabkan `dlp_limit` pada prosa bersih: n=50 di 32 KiB dan n=200 di 8 KiB (di `main` lolos). Fail-closed. | `dlp.go:471-512` | Kecualikan secret pendek dari budget verifikasi, atau skalakan budget dengan jumlah secret. |
| NETX-28 | MINOR | Daftar kandidat `[]int` (8 B per `%`). Pada body 8 MiB berisi `%`: total alokasi 391 MiB (di `main` 47 MiB), dan 4 scan paralel butuh 433 ms vs 104 ms. Puncak `HeapInuse` setara. | `dlp.go:284-288`, `:317-370` | Gunakan `int32` atau beri cap. |
| NETX-29 | MINOR | CHANGELOG mengklaim "≤ 50 µs termasuk `+`/`%` dan 50 secret pendek", bertentangan dengan angka CI di PR sendiri (pct 65–74 µs). Angka CI diambil dari `5ca4e43`, bukan head. Body PR masih menulis "DRAFT". Branch tertinggal 4 commit dari `main` (merge-tree bersih). | CHANGELOG, body PR | Koreksi teks dan rebase. |
| NETX-17 | open, tidak memburuk | pct-all + `%25`×3/×4 lolos di `main` maupun #28 | — | Tetap MINOR dari putaran 2. |
| NETX-23/24/25 | open | NETX-23 memburuk untuk secret pendek (lihat NETX-27). NETX-24: amandemen ADR untuk Aho-Corasick belum ada. NETX-25: branch masih tertinggal dari `main`. | — | — |

Diperiksa tanpa temuan:
- Prefilter 4-gram stride-4 sound: setiap bentuk ≥ 8 B selalu mencakup probe di offset 0..3.
- Stride 2-gram untuk secret pendek sound: secret 1–7 B terdeteksi di setiap offset dan alignment.
- Decode ulang khusus kandidat pada lapisan ≥ 2 setara dengan `pctDecode` penuh.
- Buffer pool tidak bocor antar-request.
- Lapisan mentah tetap dipindai penuh.
- Tanpa regresi selama konteks ≤ region: pct-all, `QueryEscape`¹⁻⁴, `%25` bersarang, `json.Marshal` di dalam query, hex campuran case, base64 std/url/raw di 3 alignment, base64 terbungkus CRLF/LF/CR/`%0D%0A` di 8/76 kolom, secret uppercase, secret dengan karakter reserved.

## Daftar BLOCKER/MAJOR terbuka

| ID | Severity | Ringkas |
|---|---|---|
| ~~PROV-B1~~ | BLOCKER → **closed** (#36 `6fd631d`) | Rekursi `jscan.skip` tanpa batas kedalaman: satu event SSE membuat proses arm64 crash fatal |
| PROV-M1 | MAJOR | Penggabungan nama tool kuadratik (CPU/alokasi DoS) |
| PROV-M2 | MAJOR | Amplifikasi entri `{}` di `tool_calls` |
| PROV-M3 | MAJOR | Reasoning parts menyimpan sekitar 40× cap |
| PROV-M4 | MAJOR | Redirect meneruskan header kredensial kustom dan prompt ke host lain |
| PROV-M5 | MAJOR | Fallback yang gagal tidak terlihat (P7) |
| LED-1 | MAJOR | Test append-only vakum |
| LED-2 | MAJOR | `usage.cost` raksasa menghasilkan biaya negatif |
| LED-3 | MAJOR | Overflow token × harga menghasilkan biaya negatif atau mendekati 0 |
| NETX-26 | BLOCKER (PR #28, sebelum merge) | Jendela rescan lapisan decode melewatkan secret yang disisipi CR/LF serta JWT/userinfo dengan konteks > 256 B (regresi vs `main`) |

## Verifikasi perbaikan PROV-B1 (#36 @ `6fd631d`, sudah di `main`)

Perbaikan: `skip` kini iteratif (tumpukan jenis berbasis bit, bukan rekursi), dan `object`/`array`/`skip` berbagi penghitung kedalaman dengan batas `maxDepth = 10000` (sama dengan `encoding/json`) → `errJSON`.

| Pemeriksaan | Hasil |
|---|---|
| Frame berukuran maksimum `{"x":` + 4.194.293 × `[` (amd64) | `malformed JSON chunk at byte 10005`, `StackInuse` 512 KiB (sebelumnya 512 MiB) |
| Frame yang sama di arm64 (`qemu-aarch64`) | error yang sama, `StackInuse` 544 KiB; tidak ada fatal |
| Reasoning bersarang 800.000 × `{"a":` | error di byte 50.015, stack 512 KiB |
| Kedalaman 9.999 dan 10.000 (batas) | diterima (`err=nil`) |
| `go test -race ./internal/provider/` | ok |
| `FuzzDeltaDecode` 120 s (2,88 juta eksekusi) | PASS |
| Test bawaan PR | Test frame maksimum dan seed kedalaman 10.001 ada di `delta_test.go` (+54 baris) |

**PROV-B1: closed.** Sisa yang belum ditangani PR ini: PROV-m1 (duplikat key) dan MAJOR PROV-M1..M5 (lewat #38) serta LED-1..3 (lewat #37). Ketiganya diverifikasi setelah PR-nya siap.
