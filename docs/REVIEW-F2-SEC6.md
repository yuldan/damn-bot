# REVIEW-F2-SEC6 — Review keamanan tabel harga registry (#178, ADR-128)

**Status: lengkap. Verdict: REQUEST CHANGES — 1 HIGH, 2 MEDIUM, 2 MINOR.** Review independen atas batas cost-ledger PR #178 (`w5-pricing`, head `7ef797a`, basis `main` `58e286b`). Komponen yang direview: `internal/provider/registry/providers.toml` + `prices.go`, `provider.RoutePrices` → `budget.Prices`, pemuatan di `cmd/damnbot/run.go`, dan ADR-128. Reviewer tidak menulis kode yang direview, dan kode produk tidak diubah. Tanggal: 2026-10-21.

Metode: worktree terpisah di Linux amd64, `GOMAXPROCS=2 GOFLAGS=-p=1 nice -n 19`, `ulimit -v 4 GiB`. Probe `TestZZ*` (`internal/provider/registry/zz_sec6_probe_test.go`, `cmd/damnbot/zz_sec6_probe_test.go`) dijalankan lalu dihapus.

Gate: `go test -race -tags goolm -count=1 ./internal/provider/... ./internal/budget/... ./internal/ctxdb/ledgerstore/... ./cmd/damnbot/... ./internal/archtest/...` → **semua ok** (provider 1,8 s, registry 1,0 s, budget 1,0 s, ledgerstore 3,1 s, cmd/damnbot 6,2 s, archtest 3,6 s). PR tidak menambah blob > 1 MiB.

## Temuan

`confirmed` berarti dibuktikan dengan probe; `static` berarti dibuktikan dari kode.

| ID | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|
| SEC6-1 | **HIGH** | confirmed | **Harga route tidak pernah sampai ke ledger `run`.** `newProviderClient` memasang `Route.Prices`, dan `OpenAI.Do` mengisi `resp.Price`. Namun `providerAdapter.Stream` hanya meneruskan `resp.Usage.CostUSD` (`run.go:460`), sementara `ctxLedger.Record` (`run.go:572-600`) menulis `cost_source="unknown"`, `cost_usd_micros=NULL`, dan `pricing_version="none"` (`:591`) untuk setiap panggilan tanpa biaya dari provider. `budget.Entry.Account` (yang memakai `budget.Cost` + `Price.Version`) tidak pernah dipanggil. Akibatnya, klaim ADR-128 butir 1/4 bahwa "ledger mencatat `pricing_version`" dan "guard dan ledger memakai jalur lookup yang sama" **tidak berlaku di `run`**. Tidak ada charge yang tercatat dengan `pricing_version`, dan biaya tabel tidak pernah dibukukan. Selain itu, `run` di `main` belum memasang `Guard` (`provider.Config` tanpa `Guard`), sehingga di PR ini harga hanya dihitung lalu dibuang. Begitu guard dipasang (#173), setiap panggilan TeamRouter menghasilkan baris NULL, dan guard (`guard.go:220`, `unknown > 0`) menolak semua panggilan berikutnya di bawah cap moneter. Arahnya gagal-tertutup, tetapi cap moneter menjadi tidak dapat dipakai pada route yang sudah diberi harga. | Probe `TestZZLedgerIgnoresPrice`: `ctxLedger.Record` untuk TeamRouter 1.000/500 token → baris `NULL unknown none`. Untuk usage yang sama, `Account` akan mencatat `450 pricing_table providers-2026-10-21/teamorouter-gpt-5.4-mini`. Test PR `TestRoutePricesTeamRouter` hanya memanggil `Entry.Account` secara langsung, bukan jalur `run`. |
| SEC6-2 | MEDIUM | confirmed (pra-ada, di jalur yang sama) | `ctxLedger.Record` mengonversi biaya yang dilaporkan provider dengan `int64(*u.CostUSD*1e6 + 0.5)` tanpa validasi `budget.Cost`. Nilai negatif, NaN, ±Inf, atau `> MaxProviderCostUSD` diterima dan tercatat sebagai `cost_source="provider"`. `cached > input` juga tidak ditolak. Biaya negatif dari reseller atau provider yang buggy/jahat **mengurangi spend periode**, sehingga cap harian/bulanan dapat terlewati (fail-open). NaN menjadi `MinInt64`. | Probe: `CostUSD=-0.75` → baris `-749999 provider none`. `NaN` → `-9223372036854775808 provider none`. `CachedInput=50 > Input=10` direkam tanpa error. Perbaikan SEC6-1 (memakai `budget.Entry.Account`) sekaligus menutup temuan ini. |
| SEC6-3 | MEDIUM | confirmed | **Kolom harga yang tidak diisi diam-diam bernilai 0.** `priceEntry.Input/Output` adalah `int64` biasa, dan `ParsePriceTable` hanya menolak kunci yang tidak dikenal (`md.Undecoded`). Kunci `input`/`output` yang terhapus atau terlupa menghasilkan harga 0 → **under-charge** dan cap tidak pernah tercapai. Ini bertentangan dengan komentar `ErrPrice` ("a typo must not silently turn … a price into zero"). Typo nama kunci (`ouput`) memang tertangkap, tetapi kunci yang tidak ada sama sekali tidak. `cached_input` yang tidak diisi aman, karena ditagih dengan tarif input (over-charge). | Probe `TestZZTable`: tanpa `output` → `OutputPerMTok:0`, err=nil. Tanpa satu pun kolom harga → entri `0/0/0` diterima. Perbaikan: wajibkan `md.IsDefined("price", i, "input")`/`"output"` (atau gunakan pointer), dan izinkan harga 0 hanya bila ditulis eksplisit. |
| SEC6-4 | MINOR | confirmed | Validasi `id`/`model` tidak konsisten. `id` di-trim saat cek kosong, tetapi `pricing_version` memakai `id` mentah, sehingga `"a"` dan `"a "` dianggap dua id berbeda (duplikat secara visual dalam ledger). Escape seperti `\u0007` di `id` juga lolos ke `pricing_version`. `model` hanya ditolak bila `""`, sehingga `model = " "` diterima. Model yang diberi spasi tidak akan pernah cocok (arah gagal-tertutup). | Probe: `dup id whitespace` → err=nil. `model spaces` → err=nil. Perbaikan: tolak `id`/`model` yang mengandung spasi di tepi atau karakter di luar `[A-Za-z0-9._:/-]`. |
| SEC6-5 | MINOR | static | Belum ada gate kedaluwarsa untuk entri tanpa `until`. Entri TeamRouter bersifat open-ended, sehingga perubahan harga kontrak tidak terdeteksi. ADR-128 sudah mencatat ini sebagai konsekuensi (`TestPricingTableNotExpired`, `doctor` > 60 hari). Dicatat di sini hanya agar dapat dilacak. | `providers.toml:21` (tanpa `until`) |

## Diperiksa dan bersih

- **Gagal-tertutup untuk route tanpa harga:** `Lookup` yang gagal atau error normalisasi → `nil` → `budget.Prices{}` → `At` = nil → `estimateMicros = -1` → guard menolak `budget_unknown_cost` sebelum request (`TestRoutePricesResellerRouteFailsClosed`: 0 request). Entri yang belum berlaku (`from` di masa depan) atau sudah kedaluwarsa juga menghasilkan `At` nil.
- **Normalisasi `base_url`** (probe 30 varian). Varian berikut **dikenai harga** dan semuanya menuju origin dan path yang sama: `HTTPS://API.TeamoRouter.COM/v1/`, `:443/v1//`, `:/v1` (port kosong). Varian berikut **tidak dikenai harga** (arah aman): `/V1`, tanpa path, `/v1/chat`, host dengan titik di akhir, `:0443`, `:8443`, `http://`, `/v1/../v1`, `/%76%31`, `/v1%2f`, `/v1/.`, `/v1;x`, punycode/IDN, IPv6. Bentuk berikut **ditolak**: userinfo (`user:pw@`, `evil.com@host`, `host@evil.com`), query, fragment, backslash, `%2e` di host, spasi, URL tanpa scheme atau opaque. Tidak ditemukan cara membuat route reseller cocok dengan kunci harga lain, atau sebaliknya. Model dicocokkan persis (`GPT-…`, spasi di akhir, prefix `openai/`, NUL → tidak dikenai harga).
- **Aritmetika micro-USD** (`budget/ledger.go` `tableCost`, tidak diubah PR ini): `cached ⊂ input` ditegakkan oleh `Cost` (`CachedInput > Input` → `ErrInvalidUsage`). Perkalian dan penjumlahan memakai `bits.Mul64/Add64` dengan cek overflow. Pembulatan memakai ceil atas total (`sum/1e6 + min(sum%1e6,1)`), sehingga tidak pernah under-charge (1 token input = 1 µUSD). Harga `int64` maksimum di tabel diterima, tetapi `Cost` mengembalikan overflow → unknown → gagal-tertutup. Contoh `MaxTokens` × TeamRouter = 615.000.000.000 µUSD tanpa overflow. Contoh test: 1.000/200/500 token = 120 + 3 + 300 = **423 µUSD** ✔.
- **Validasi saat start:** TOML rusak, kunci tak dikenal (termasuk `price.extra` bersarang), float atau string di kolom harga, integer di luar rentang `int64`, datetime TOML di `from`, `version` kosong, `kind` tak dikenal, `source` kosong, `until ≤ from`, harga negatif, `id` duplikat, dan rentang tumpang tindih per route (termasuk alias `HTTPS://X.TEST:443/v1/` vs `https://x.test/v1`) → `ErrPrice`. `run` gagal start bila tabel embedded tidak valid. `budget.NewPrices` memvalidasi ulang. Pengecualian: kolom harga yang tidak diisi (SEC6-3).
- **Injeksi harga:** satu-satunya sumber adalah `go:embed providers.toml`. Pemanggil `ParsePriceTable`/`DefaultPriceTable`/`RoutePrices` hanya `run.go:256-260` dan test. Config, env, dan file pengguna tidak bisa menambah atau mengganti harga. Override `[[providers.custom]]` (Q22) secara eksplisit belum ada (ADR-128 Konsekuensi). `--base-url` dari pengguna hanya memilih route, dan harga tetap diambil dari tabel.
- **Tanggal:** `YYYY-MM-DD` diperlakukan sebagai tengah malam UTC. RFC3339 dengan offset dinormalkan ke UTC (`+07:00` → 17:00 UTC hari sebelumnya, benar).

## Rekomendasi

1. **SEC6-1/SEC6-2:** teruskan `resp.Price` (dan estimasi raw) ke `loop.Reply`/`event.Usage`, lalu jadikan `ctxLedger.Record` sebagai `budget.Entry.Account(u, price, est)` + `ledgerstore.Insert` dari `Entry`. Hapus konversi float manual dan literal `"none"`. Tambahkan test `run` end-to-end dengan route yang diberi harga (tabel injeksi khusus test atau base_url `providertest` yang diberi harga di test) yang memeriksa `cost_source=pricing_table`, `cost_usd_micros`, dan `pricing_version=<version>/<id>`. Tambahkan juga test bahwa biaya provider negatif/NaN ditolak.
2. **SEC6-3:** wajibkan `input` dan `output` (`md.IsDefined`), tambahkan kasus negatif di `TestParsePriceTableRejectsInvalid`.
3. **SEC6-4:** validasi karakter `id`/`model` dan bandingkan duplikat setelah trim.

## Verdict

**REQUEST CHANGES** untuk PR #178. Blocker: SEC6-1 (HIGH), karena inti PR adalah mencatat `pricing_version` pada setiap charge dan jalur `run` tidak melakukannya. SEC6-2 dan SEC6-3 (MEDIUM) sebaiknya diperbaiki dalam PR yang sama. Lookup, normalisasi, aritmetika, dan validasi tabel di luar SEC6-3/4 sudah benar dan gagal-tertutup.

## Tidak dicakup

- Wiring `budget.Guard` di `run` (#173 stack) di luar efek SEC6-1.
- Provider live, harga vendor (belum ada di tabel), override config Q22, `doctor`.
- Windows/macOS (hanya analisis statis).

---

## Round 2 — 2026-10-21

**Verdict: REQUEST CHANGES.** SEC6-1…5 sudah FIXED atau terdokumentasi. Probe pada merge sementara #173 + #178 menunjukkan dua celah baru: provider/reseller dapat **menghindari cap moneter** dengan melaporkan biaya rendah (SEC6-6, HIGH), dan respons dengan usage tidak masuk akal tidak meninggalkan baris ledger (SEC6-8, MEDIUM). Selain itu, kedua PR tidak dapat digabung secara mekanis tanpa memindahkan harga ke `newRunProvider` (SEC6-7, MINOR).

Head yang direview: #178 `w5-pricing` `43646ea` (basis `58e286b`) dan #173 `feat/f2-wire-budget` `11221de`. Gate di `43646ea`: `go test -race -tags goolm -count=1 ./internal/provider/... ./internal/budget/... ./internal/ctxdb/ledgerstore/... ./internal/loop/... ./cmd/damnbot/... ./internal/archtest/...` → **semua ok**. `TestRunLedgerRecordsRoutePrice`, `TestRunLedgerValidatesProviderCost`, dan `TestParsePriceTableRejectsInvalid` lulus.

Probe integrasi `TestZZ*` dijalankan pada merge sementara `11221de` + `43646ea`, lalu merge, worktree, dan probe dihapus tanpa commit. Konflik `run.go` diselesaikan dengan memindahkan `DefaultPriceTable` + `RoutePrices` ke `newRunProvider` (lihat SEC6-7). Untuk probe, satu entri harga sementara ditambahkan ke tabel: `probe-local`, `http://127.0.0.1:18081/v1` + `m`, 1.000.000 µUSD/Mtok untuk input, cached, dan output. Server SSE palsu dijalankan di port tersebut, dengan `[budget] daily_usd = 0.05` (50.000 µUSD).

### Verdict per temuan round 1

| ID | Verdict | Bukti |
|---|---|---|
| SEC6-1 | **FIXED** | `providerAdapter.Stream` meneruskan `resp.Price` → `event.Usage.Price` (`eventPrice`, `run.go:570`). `ctxLedger.Record` (`:591`) memanggil `budget.Entry.Account` (`:604`) lalu menyimpan `ledgerstore.Row(e)`. Probe integrasi: setiap call TeamRouter-style 10.000/5.000 token tercatat **`15000 pricing_table providers-2026-10-21/probe-local`**. `TestRunLedgerRecordsRoutePrice` (423 µUSD, `pricing_table`, versi entri). |
| SEC6-2 | **FIXED (catatan SEC6-8)** | Biaya negatif/NaN/−Inf tidak lagi mengurangi spend; `Cost:69` jatuh ke harga tabel atau unknown. `+Inf`, nilai `> MaxProviderCostUSD`, token negatif, dan `cached > input` → `ErrInvalidUsage` → turn gagal. `TestRunLedgerValidatesProviderCost`. Akibat dari "turn gagal tanpa baris" dibahas di SEC6-8. |
| SEC6-3 | **FIXED** | `input`/`cached_input`/`output` sekarang `*int64`, dan `columns()` menolak kolom yang hilang atau negatif. Kasus kolom hilang ada di `TestParsePriceTableRejectsInvalid`. Komentar `providers.toml` diperbarui. |
| SEC6-4 | **FIXED** | `plainToken` (`[A-Za-z0-9._:/-]+`) untuk `id` dan `model` menolak spasi (termasuk di tepi), karakter kontrol, dan non-ASCII. Nit: `/` dan `:` diizinkan di `id`, sehingga `pricing_version = <version>/<id>` dapat memuat `/` tambahan. Ini tidak ambigu karena `version` tidak berisi `/`, tetapi sebaiknya `/` dikeluarkan dari himpunan id. |
| SEC6-5 | **DOCUMENTED** | ADR-128 amandemen butir 4 memilih entri open-ended tanpa batas umur dan beralasan. Gate `TestPricingTableNotExpired`/`doctor` tetap tercatat sebagai pekerjaan berikutnya. Diterima. |

### Integrasi dengan guard #173 (merge sementara)

| Skenario (cap harian 50.000 µUSD; proyeksi per call ≈ input est + 8.192 × 1 µUSD) | Hasil |
|---|---|
| Biaya hanya dari tabel (usage 10.000/5.000, tanpa `cost`) | Call 1–3 `end_turn/completed`, masing-masing ditagih **15.000 µUSD `pricing_table`**. Call 4 dan 5 **ditolak `max_tokens/budget_daily`** ("45000 of 50000 µUSD") tanpa request. ✔ Sesuai harapan: ditagih dan diizinkan sampai cap, lalu ditolak. |
| Provider melaporkan `"cost": 0` | **6/6 call diizinkan**, masing-masing tercatat `0 provider providers-2026-10-21/probe-local`. Cap tidak pernah tercapai. → SEC6-6 |
| Provider melaporkan `"cost": 0.000001` | **6/6 diizinkan**, masing-masing tercatat `1 µUSD provider`. → SEC6-6 |
| Provider melaporkan `"cost": 2000000` (> `MaxProviderCostUSD`) | 5/5 call **dikirim ke provider**, setiap turn `error/internal` "invalid usage", **ledger kosong**. → SEC6-8 |
| `cached_tokens` 200 > `prompt_tokens` 100 | Sama: 5/5 call dikirim, ledger kosong. → SEC6-8 |

### Temuan baru

| ID | Severity | Status | Temuan | Bukti / perbaikan |
|---|---|---|---|---|
| SEC6-6 | **HIGH** | confirmed | **Biaya yang dilaporkan provider mengalahkan harga tabel, sehingga cap moneter dapat dilewati.** `budget.Cost` (`ledger.go:69`, pra-ada ADR-125) memakai biaya provider yang valid (`0 ≤ cost ≤ 1e6`) **apa pun nilai tabelnya**. Sejak #178, route ber-harga di `run` memiliki harga kontrak, tetapi reseller, proxy, atau gateway yang mengirim `usage.cost = 0` (atau nilai kecil) membuat setiap call tercatat ≈0, sehingga guard #173 tidak pernah menolak. Untuk route `operator-contracted`, angka tagihan adalah kontrak, bukan nilai yang dilaporkan reseller. Selain itu, `Account` (`:142`) mengisi `pricing_version` dengan versi tabel walaupun `cost_source = provider`, padahal tabel tidak dipakai, sehingga jejak audit menyesatkan. | Probe: 6/6 call di bawah cap dengan `cost: 0` → ledger `0 provider providers-…/probe-local`. **Keputusan reviewer:** bila ada entri tabel yang berlaku, charge = **max(tabel, provider)**. Harga tabel adalah batas bawah yang diaudit dan biaya provider yang lebih tinggi tetap dihormati, sehingga hasilnya tidak pernah under-charge terhadap kontrak dan over-charge dibatasi oleh nilai yang dilaporkan. `cost_source` mengikuti sisi yang menang, dan `pricing_version` hanya diisi versi tabel bila tabel yang menang (atau dicatat keduanya). Tanpa entri tabel, perilaku ADR-125 tetap. Untuk `kind = operator-contracted`, tabel saja juga dapat diterima. Perbarui ADR-125 butir 3 / ADR-128 dan tambahkan test `cost: 0` serta `cost` < tabel di bawah cap. |
| SEC6-7 | MINOR | confirmed | **#173 dan #178 tidak berkomposisi secara mekanis.** `git merge` menghasilkan konflik di `run.go`: #173 membangun provider di `newRunProvider` (dengan Guard), sedangkan #178 di `newProviderClient` (dengan Prices). Resolusi "ambil HEAD" meninggalkan `newProviderClient` sebagai kode mati, sehingga route tanpa `Prices`: guard menolak setiap call di bawah cap dan ledger kembali `unknown` (fail-closed, tetapi fitur harga hilang tanpa test yang gagal). Komentar `TestRunBudgetGuardDenies` ("run has no registry price yet") juga perlu diperbarui. | Resolusi yang benar dan dipakai probe: `DefaultPriceTable` + `RoutePrices(table, base, modelName)` di dalam `newRunProvider`, `Prices` dipasang pada `Route` yang sama dengan `Guard`, dan `newProviderClient` dihapus. Tambahkan test `run` end-to-end dengan route ber-harga + cap (urutan diizinkan → ditolak) pada PR yang di-merge terakhir. |
| SEC6-8 | MEDIUM | confirmed | **Usage tidak masuk akal tidak meninggalkan baris ledger.** `ctxLedger.Record` mengembalikan error dari `Account` sebelum menulis, padahal request sudah terjadi dan sudah dibayar. Turn berakhir `error/internal`, tetapi spend-nya **tidak terlihat oleh guard**, sehingga provider yang terus mengirim `cost > 1e6` atau `cached > input` mendapat call tak terbatas di bawah cap (setiap `run` gagal secara terlihat, tetapi otomasi seperti cron/gateway tetap membayar). Klaim ADR-128 amandemen butir 1 "tidak ada baris yang bisa menutupi cap" terbalik: justru ketiadaan baris yang menutupi spend. | Probe: 5/5 call dikirim, ledger kosong. Perbaikan: pada `ErrInvalidUsage`, tetap tulis baris dengan `cost_usd_micros = NULL` (`cost_source = unknown`, token dijepit atau 0 + `est_input_tokens`) agar guard menolak call berikutnya di bawah cap moneter (`unknown > 0`), atau tagih proyeksi konservatif (`estimateMicros`). Tambahkan test: invalid usage → baris unknown → call berikutnya ditolak. |

### Diperiksa dan bersih

- `event.Price` di `loop/event` tetap leaf (tanpa import `budget`), konversi dilakukan di composition root. `ledgerstore.Row(e)` adalah konversi field identik (ADR-101 butir 5).
- Biaya provider negatif/NaN/−Inf tidak mengurangi spend (jatuh ke tabel). Pembulatan tabel tetap ceil. Overflow tetap diperiksa.
- Route tanpa harga di bawah cap tetap ditolak sebelum request (`TestRunBudgetGuardDenies` di #173, `TestRoutePricesResellerRouteFailsClosed`).
- Tidak ada jalur injeksi harga baru: tabel tetap hanya dari `go:embed`.

**Verdict: REQUEST CHANGES** untuk #178. Blocker: SEC6-6 (HIGH). SEC6-8 (MEDIUM) dan SEC6-7 (MINOR, terkait urutan merge dengan #173) sebaiknya diselesaikan sebelum kedua PR digabung.

---

## Round 3 — 2026-10-21

**Verdict: APPROVE.** SEC6-6 (HIGH), SEC6-7, dan SEC6-8 FIXED; nit id `/` FIXED. Probe integrasi guard dengan biaya provider 0 / kecil / lebih tinggi / negatif / terlalu besar, `cached > input`, dan token > `MaxTokens` semuanya **gagal-tertutup**: ditagih sampai cap lalu ditolak, atau ditolak setelah baris unknown. Satu residual pra-ada dicatat sebagai MINOR (SEC6-9, di luar PR ini).

Head yang direview: #178 `w5-pricing` `b464939`, sekarang di-stack di atas `feat/f2-wire-modules` `4220f05` (commit `7bbeb41`, `35caa71`, `b464939`). Gate: `go test -race -tags goolm -count=1 ./internal/provider/... ./internal/budget/... ./internal/ctxdb/ledgerstore/... ./internal/loop/... ./cmd/damnbot/... ./internal/archtest/...` → **semua ok**. `TestRunBudgetGuardPricedRoute`, `TestRunLedgerRecordsRoutePrice`, `TestRunLedgerValidatesProviderCost`, `TestRunBudgetGuardDenies`, `TestParsePriceTableRejectsInvalid`, `TestCost`, `TestAccount`, dan `TestBudgetTableSum` lulus. Tidak ada blob > 1 MiB.

Probe `TestZZ3*` dijalankan di worktree sementara pada `b464939` dengan satu entri harga sementara (`probe-local`, `http://127.0.0.1:18081/v1` + `m`, 1.000.000 µUSD/Mtok untuk ketiga kolom) dan server SSE palsu, cap `[budget] daily_usd = 0.05` (50.000 µUSD), 10.000/5.000 token per call. Worktree dan probe sudah dihapus.

### Verdict per temuan

| ID | Verdict | Bukti |
|---|---|---|
| SEC6-6 | **FIXED** | `budget.Cost` (`internal/budget/ledger.go`): bila entri tabel berlaku, charge = `max(tableCost, providerCost)`. `providerCost` mengembalikan nil untuk nilai negatif, NaN, atau tidak ada, dan `ErrInvalidUsage` untuk nilai `> MaxProviderCostUSD` atau `+Inf`. `Account` hanya mengisi `pricing_version` dengan versi tabel bila `src == pricing_table`; bila provider yang menang, `provider`/`none`. ADR-128 amandemen round 2 butir 1 + catatan amandemen ADR-125. Probe: `cost: 0` dan `cost: 0.000001` → ditagih **15.000 µUSD `pricing_table providers-2026-10-21/probe-local`**, call 1–3 diizinkan, call 4–5 ditolak `budget_daily` (3 request). `cost: 0.02` (> tabel 15.000) → **20.000 µUSD `provider none`**, call 4 ditolak. `cost: -5` → jatuh ke tabel, hasil identik dengan tabel saja. |
| SEC6-7 | **FIXED** | PR di-restack ke atas #174, dan `newRunProvider` → `newRunProviderWithTable` membangun `DefaultPriceTable` + `RoutePrices` lalu memasang `Prices` dan `Guard` pada route yang sama. `newProviderClient` dihapus. `TestRunBudgetGuardPricedRoute`: 3 call masing-masing 450 µUSD `pricing_table`, call ke-4 `budget_daily`, `srv.Requests() == 3`. Komentar `TestRunBudgetGuardDenies` sudah diperbarui. Seam `newRunProviderWithTable` hanya dipanggil test; composition root selalu memakai tabel embedded. |
| SEC6-8 | **FIXED** | Saat `Account` gagal, `ctxLedger.Record` tetap menulis baris `NULL unknown none` dengan token yang di-clamp (negatif → 0, `cached ≤ input`), lalu mengembalikan error (turn `error/internal`). Probe: `cost: 2e6` → 1 request, baris `10000/0/5000 NULL unknown none`, call 2–4 ditolak "cost unknown for cap daily" tanpa request. `cached_tokens 200 > prompt 100` → baris `100/100/5000 NULL`, lalu ditolak. `prompt_tokens = 2e12` (> `MaxTokens`) → baris `2000000000000/0/5 NULL` tetap tersimpan (tidak ada CHECK yang menolak), lalu ditolak. |
| SEC6-4 (nit) | **FIXED** | `plainToken(id, slash=false)` → `[A-Za-z0-9._:-]+`, sehingga `/` hanya menjadi pemisah `pricing_version`. `model` tetap boleh memuat `/` (`org/model`). |

### LOC dan ADR

- `loc.budget`: `budget 560 → 580` dengan komentar `ADR-128 amandemen SEC6-6 … terukur 571; Σ core +20`. ADR-128 amandemen (butir 6 dan amandemen round 2 butir 6) mencatat kenaikan yang sama, dan tabel ENGINEERING §2.4 memuat `budget 580` serta **Total core 36.540**.
- Jumlah semua baris core `loc.budget` (tanpa `testutil*`, `archtest`, `analyzers`) = **36.540** (basis `4220f05`: 36.520), sama dengan Total core. `TestBudgetTableSum` lulus. `make loc`: `budget 571 / 580` ok, dan tidak ada grup yang melewati batas.

### Probe guard + ledger pada `b464939`

| Usage dari provider | Request | Ledger | Hasil |
|---|---|---|---|
| tanpa `cost` | 3 | 3 × `15000 pricing_table` | call 4–5 `budget_daily` ✔ |
| `cost: 0` | 3 | 3 × `15000 pricing_table` | call 4–5 `budget_daily` ✔ |
| `cost: 0.000001` | 3 | 3 × `15000 pricing_table` | call 4–5 `budget_daily` ✔ |
| `cost: 0.02` | 3 | 3 × `20000 provider none` | call 4 `budget_daily` ✔ |
| `cost: -5` | 3 | 3 × `15000 pricing_table` | call 4–5 `budget_daily` ✔ |
| `cost: 2000000` | 1 | `NULL unknown none` | call 1 `error/internal`; call 2–4 `budget_daily` (cost unknown) ✔ |
| `cached 200 > prompt 100` | 1 | `100/100/5000 NULL unknown` | idem ✔ |
| `prompt_tokens 2e12` | 1 | `2000000000000/0/5 NULL unknown` | idem ✔ |
| `prompt_tokens -10` | 12 (3 per run × 4 run) | **kosong** | `error/provider_error` "delta: malformed" → SEC6-9 |

### Residual

| ID | Severity | Status | Temuan |
|---|---|---|---|
| SEC6-9 | MINOR | confirmed, **pra-ada (bukan PR ini)** | Respons yang gagal di parser stream (mis. `prompt_tokens` negatif → `delta: malformed` → `stream_broken`) diulang 3× oleh kebijakan retry dan **tidak menulis baris ledger apa pun**, sehingga request yang mungkin sudah ditagih provider tidak terlihat oleh guard. Probe: 4 run → 12 request, ledger kosong, dan tidak ada penolakan. Setiap run gagal secara terlihat (`provider_error`), sehingga dampaknya terbatas pada otomasi yang terus mengulang. Rekomendasi follow-up (provider/loop): catat attempt yang sudah menerima byte respons sebagai baris `unknown`, sehingga cap moneter menolak sesudahnya. |

**Verdict: APPROVE** untuk #178 (`b464939`). Sebelum merge, #178 harus menunggu stack #180…#174 karena basisnya `feat/f2-wire-modules`. SEC6-9 dapat ditangani sebagai follow-up terpisah.

---

## Round 4 — 2026-10-21 (follow-up #184, SEC6-9)

**Verdict #184: REQUEST CHANGES.** Pencatatan attempt berbayar bekerja untuk jalur error, retry, sukses-setelah-gagal, dan stream yang putus di tengah tool call: tidak ada pencatatan ganda, dan retry berhenti di bawah cap. Namun klaim "baris tetap ditulis saat ctx dibatalkan" **tidak berlaku pada jalur sukses**. `turn.accept` keluar lebih dulu ketika ctx batal, sehingga attempt berbayar yang gagal **dan** call sukses yang dibayar tidak tercatat (**SEC6-10, MEDIUM**). Ada juga dua catatan semantik/hardening (SEC6-11 MINOR, SEC6-12 nit). #178 tetap **APPROVE** (round 3).

Head yang direview: #184 `w5-unknown-attempts` `e206aed` (basis `w5-pricing` `b464939`, satu commit). Gate: `go test -race -tags goolm -count=1 ./internal/provider/... ./internal/budget/... ./internal/ctxdb/ledgerstore/... ./internal/loop/... ./cmd/damnbot/... ./internal/archtest/...` → **semua ok**. Test berikut lulus: `TestBilledFailedAttempts`, `TestBilledRetryStopsUnderCap`, `TestBilledAttemptsRecorded`, `TestRunBrokenStreamRecordsAttempts`, `TestRunBrokenStreamStopsUnderCap`, `TestBudgetTableSum`. `make loc`: `loop 1805 / 1850`, `provider 1833 / 2000`, `budget 571 / 580`.

Probe `TestZZ4*` dijalankan di worktree sementara dengan entri harga `probe-local` (1.000.000 µUSD/Mtok) dan server SSE palsu yang diskrip per request. Mode server: `broken` = 2xx, satu delta, lalu frame rusak; `toolbroken` = delta `tool_calls` lalu frame rusak; `500`; `ok` = 10.000/5.000 token. Worktree sudah dihapus.

### Probe

| Skenario | Request | Ledger | Hasil |
|---|---|---|---|
| broken → ok, tanpa cap | 2 | `NULL unknown` + `15000 pricing_table` | sukses; **tidak ada pencatatan ganda** ✔ |
| broken, broken → ok | 3 | 2 × `NULL` + 1 × `15000` | ✔ |
| broken ×3, 2 run | 6 | 6 × `NULL unknown none` | setiap attempt tercatat ✔ |
| toolbroken ×3 (gagal di tengah tool call) | 3 | 3 × `NULL` | ✔ |
| broken → (ok), cap harian | **1** | 1 × `NULL` | run 1 `budget_daily` (retry dihentikan), run 2–3 ditolak "cost unknown" **tanpa request** ✔ fail-closed (lihat SEC6-11) |
| 500 → ok, cap harian | 2, lalu 3 | 2 × `15000 pricing_table` | non-2xx tidak dianggap berbayar; retry normal ✔ |

### Verdict per klaim

| Klaim | Verdict | Bukti |
|---|---|---|
| `Response.BilledFailed` menghitung attempt 2xx yang gagal | **OK** | `readStream` mengembalikan `Response{responded: true}` pada setiap error sesudah stream 2xx terbuka, termasuk `a.result()`. `call` (`client.go:214`) menghitung `billed++` hanya untuk `err != nil && responded`. Akumulasi lintas route fallback ada di `Do` (`billed += resp.BilledFailed`) dan ikut dikembalikan pada jalur denial, `ErrNoRoute`, error non-retryable, dan error terakhir. `limit` default = `retry.MaxAttempts` (3), sama dengan `retry.Do`. |
| Guard dikonsultasi ulang sebelum retry attempt berbayar | **OK** | `retryDenied` (`:186`) dipanggil dengan `EstimateMicros = -1`. Di bawah cap moneter, penolakan dikembalikan sebagai `BadRequest` sehingga tidak di-retry. Tanpa cap, retry tetap. Probe: 1 request di bawah cap, 3 tanpa cap. |
| Satu baris NULL/unknown per attempt berbayar, `WithoutCancel` | **PARSIAL → SEC6-10** | `recordBilledAttempts` (`turn.go:485`) dipanggil di `streamError` (`:256`) **sebelum** cek `ctx.Err()` ✔, dan di `accept` (`:220`) **sesudah** cek `ctx.Err()` ✘. |
| Tidak ada pencatatan ganda pada sukses-setelah-gagal | **OK** | `BilledFailed` hanya menghitung attempt yang gagal, sedangkan attempt sukses dicatat sekali oleh `t.record`. Re-call milik loop (respons kosong, context length) masing-masing membawa `BilledFailed` miliknya sendiri. |
| LOC `loop` 1.800 → 1.850 | **OK** | Komentar `loc.budget` dan ADR-128 amandemen round 3 butir 4 (terukur 1.805). ENGINEERING §2.4 baris `loop` = 1.850. Jumlah core `loc.budget` = **36.590** (basis 36.540), sama dengan Total core di tabel dan di prosa. `TestBudgetTableSum` lulus. |

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC6-10 | **MEDIUM** | static | `turn.accept` (`internal/loop/turn.go:220`) mengembalikan `cancelled` **sebelum** `recordBilledAttempts` dan sebelum `t.record` untuk usage call yang sukses. Bila ctx dibatalkan setelah `Do` berhasil (Ctrl-C atau timeout turn tepat saat stream selesai), attempt gagal sebelumnya **dan** call sukses yang sudah dibayar tidak menulis baris apa pun, sehingga guard tidak melihat spend tersebut. Klaim PR "rows written even on ctx cancel" hanya benar untuk jalur error. Hilangnya usage call sukses saat cancel sudah ada sebelumnya, tetapi PR ini menambahkan pencatatan `BilledFailed` yang ikut hilang di jalur yang sama. | Di `accept`, catat `BilledFailed` dan usage reply (keduanya dengan `context.WithoutCancel`) **sebelum** cek `ctx.Err()`, lalu kembalikan `cancelled`. Tambahkan test: reply sukses dengan `BilledFailed = 2` + ctx batal → 3 baris. |
| SEC6-11 | MINOR | confirmed (desain) | Semantik cap: satu stream putus pada route **ber-harga** di bawah cap moneter menulis baris NULL, dan guard (`unknown > 0`) lalu menolak **semua** call berikutnya sampai periode cap berakhir (harian, atau sebulan untuk cap bulanan). Arahnya gagal-tertutup, tetapi provider yang flaky (atau sengaja memutus stream) dapat mengunci pemakaian. Untuk route ber-harga, biaya maksimum attempt dapat diketahui. | Untuk route dengan entri tabel, catat attempt berbayar dengan **proyeksi konservatif** (`estimateMicros`: estimasi input + `max_tokens` × harga, `cost_source = pricing_table` atau estimasi). NULL hanya untuk route tanpa harga. Dokumentasikan pilihan ini di ADR-128. |
| SEC6-12 | nit | static | Pada `attempt` (`client.go:331`), bila stream sukses tetapi `st.Close()` gagal, defer menimpa hasilnya dengan `Response{}, toError(cerr)` (`:351`). Flag `responded` hilang, sehingga attempt yang sudah dibayar tidak dihitung, lalu mungkin di-retry. Jarang terjadi. Selain itu, request yang diterima upstream tetapi gagal sebelum header (timeout TTFB sebelum 2xx) tidak dihitung; ini sesuai definisi "2xx stream opened". | Pertahankan `responded` (dan hasilnya) saat hanya `Close` yang gagal. |

**Verdict: REQUEST CHANGES** untuk #184 (SEC6-10). SEC6-11 sebaiknya diputuskan dan didokumentasikan di ADR-128 pada PR yang sama. #178 tetap APPROVE.

---

## Round 5 — 2026-10-21 (#184 head `a1ad64a`)

**Verdict #184: APPROVE.** SEC6-10 dan SEC6-12 FIXED. SEC6-11 sebagian (PARTIAL, MINOR): route ber-harga kini memakai proyeksi, tetapi kegagalan retryable yang menghabiskan semua attempt tanpa cap tetap mencatat NULL. Ada juga satu catatan MINOR bahwa proyeksi tidak dijamin ≥ biaya aktual (SEC6-13). Tidak ada temuan BLOCKER, HIGH, atau MEDIUM yang terbuka.

Head yang direview: #184 `w5-unknown-attempts` `a1ad64a` (basis `w5-pricing` `b464939`, commit `e206aed` + `a1ad64a`). Gate: `go test -race -tags goolm -count=1 ./internal/provider/... ./internal/budget/... ./internal/ctxdb/ledgerstore/... ./internal/loop/... ./cmd/damnbot/... ./internal/archtest/...` → **semua ok**. Test berikut lulus: `TestBilledAttemptsCancelled`, `TestBilledAttemptsPriced`, `TestBilledAttemptsRecorded`, `TestBilledFailedAttempts`, `TestBilledRetryStopsUnderCap`, `TestRunBrokenStream*`, `TestBudgetTableSum`. `make loc`: `loop 1811 / 1850`, `provider 1833 / 2000`. Komentar `loc.budget` diperbarui menjadi terukur 1.811; Σ core tetap 36.590.

Probe `TestZZ5*` (route ber-harga `probe-local` 1.000.000 µUSD/Mtok, server SSE palsu) dijalankan lalu dihapus.

| Klaim | Verdict | Bukti |
|---|---|---|
| SEC6-10: usage dan attempt berbayar dicatat sebelum cek ctx | **FIXED** | `turn.accept` sekarang lebih dulu memanggil `addUsage` → `recordBilledAttempts` → `append(LLMCall)` → `record(usage)`, semuanya dengan `context.WithoutCancel`, dan **baru sesudah itu** memeriksa `ctx.Err()`. Fallback, peringatan budget, dan emit dijalankan setelah cek ctx. `TestBilledAttemptsCancelled`. |
| SEC6-11: proyeksi untuk route ber-harga, NULL untuk tanpa harga | **PARSIAL** | `billedUsage` (`run.go`) = `{Input: EstInputTokens, Output: max_tokens (default 8.192), Price}` bila `resp.Price != nil`, dan `nil` (baris NULL/unknown) bila route tanpa harga. Probe: broken → ok **tanpa cap** = `1348/8192 9540 pricing_table` + `10000/5000 15000` ✔. Broken **di bawah cap** (retry ditolak, jalur non-retryable) = proyeksi 9.540, lalu run 2–3 **diizinkan** ✔ (sebelumnya terkunci sepanjang periode). Namun broken ×3 tanpa cap (retry habis) menghasilkan **3 × `NULL unknown none`**: pada jalur ini `Do` mengembalikan `Response{BilledFailed}` tanpa `Price`/`EstInputTokens` (`client.go`: `return Response{BilledFailed: billed}, lastErr`), sehingga proyeksi tidak dipakai. Baris NULL itu tetap mengunci cap moneter bila cap diaktifkan dalam periode yang sama. |
| SEC6-12: `Close` gagal tetap `responded` | **FIXED** | `attempt` defer: `resp, err = Response{responded: true}, toError(cerr)`. |
| NULL tetap untuk route tanpa harga | **OK** | `billedUsage` → `nil` → `Usage{}` → `Account` tanpa harga = NULL/unknown. |

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC6-11b | MINOR | confirmed | Proyeksi tidak dipakai pada jalur "retry habis". `Do` membuang `Price`/`EstInputTokens` saat mengembalikan `lastErr` setelah rantai route. Akibatnya route ber-harga yang gagal 3× tanpa cap menulis NULL, dan cap yang dikonfigurasi kemudian dalam periode yang sama akan menolak semua call. Probe: 3 × NULL. | Bawa `Price` dan `EstInputTokens` dari attempt terakhir (atau route utama) di `Response` error pada semua jalur `Do`. |
| SEC6-13 | MINOR | analisis + probe | **Proyeksi bukan batas atas.** Input diproyeksikan dari `rawEstimate` (estimasi statis tidak terkalibrasi; faktor kalibrasi ADR-101 sampai 1,5×), tidak memuat overhead per pesan atau gambar, dan seluruhnya ditagih dengan harga input. Bila attempt gagal setelah upstream memproses input penuh dan output hampir `max_tokens`, biaya aktual dapat melebihi proyeksi. Contoh: input sebenarnya > raw + (slack `max_tokens` × rasio harga out/in). Probe: proyeksi 9.540 µUSD vs call setara yang sukses 15.000 µUSD (server palsu melaporkan 10.000 token input untuk raw 1.348). Selain itu, bila provider menolak `max_tokens` dan parameter di-drop (`rejectedParam`), output tidak lagi dibatasi. | Proyeksikan input dengan `raw × CalibrationMax` (atau estimator terkalibrasi + 50 %), dan hanya pakai `max_tokens` bila parameter itu benar-benar terkirim (bila di-drop, fallback ke NULL atau window − input). Dokumentasikan bahwa nilainya adalah estimasi atas, bukan jaminan. |
| SEC6-14b | nit | static | `retryDenied` memakai `EstimateMicros = -1` juga untuk route ber-harga. Di bawah cap, retry selalu ditolak ("cost unknown") walaupun proyeksi cukup kecil (probe: cap $0,10, proyeksi 9.540 µUSD, retry tetap ditolak). | Untuk route ber-harga, pakai proyeksi `estimateMicros` saat konsultasi ulang guard. |

**Verdict #184 (`a1ad64a`): APPROVE**, dengan SEC6-11b/13 sebagai follow-up MINOR.

---

## Round 6 — 2026-10-21 (#188 `f2-cost-limits-wiring` `e5676df`, ADR-129)

**Verdict #188: REQUEST CHANGES — 1 MEDIUM, 2 MINOR, 1 nit.** Window yang *learned* diambil dari teks error provider **tanpa batas, tanpa uji kewajaran, dan dipersistenkan**. Provider atau reseller yang buggy maupun jahat dapat (a) menaikkan window menjadi 10¹⁴, atau (b) menurunkannya di bawah `MinWindow`, sehingga **semua run berikutnya gagal saat start** sampai pengguna menulis override config (SEC6-15). Precedence, gate kedaluwarsa, dan "tanpa request probe" sudah benar.

Head yang direview: #188 `f2-cost-limits-wiring` `e5676df` (basis `w5-unknown-attempts` `a1ad64a`, satu commit). Gate: `go test -race -tags goolm -count=1 ./internal/provider/... ./internal/config/... ./internal/ctxdb/limitstore/... ./internal/budget/... ./internal/loop/... ./cmd/damnbot/... ./internal/archtest/...` → **semua ok**. `make loc`: `provider/registry 353 / 400`, `provider 1870 / 2000`, `config 741 / 1200`. Jumlah core `loc.budget` = **36.690** (basis 36.590), sama dengan kedua Total core ENGINEERING. Komentar `loc.budget` mencantumkan ADR-129 `provider/registry 300 → 400`.

Probe `TestZZLearned`/`TestZZParser` (server 400 `context_length_exceeded` dengan pesan diskrip, `run` dua kali per pesan dengan data dir yang sama) dijalankan lalu dihapus.

### Precedence dan gate

- **Precedence** `Limits.Window` (`internal/provider/registry/limits.go:94`): config `[[providers.custom]]` (`name/models[].id`) → registry `[[model]]` (`provider/model`) → store (`discovery`/`learned`) → fallback 32.768. Upsert `limitstore` (`ON CONFLICT … WHERE excluded.source = 'discovery' OR model_limits.source = 'learned'`) membuat discovery menang atas learned, sesuai urutan ADR-126/129. Override config divalidasi `8.192 ≤ context_length ≤ 10.000.000` (`validateCustomModel`).
- **Tanpa probe:** tidak ada kode `/models` atau HTTP di `registry`/`limitstore`. Window hanya dipelajari dari error request nyata (`run.go:522`). Probe: run kedua dengan window terlalu kecil gagal saat start dengan **0 request**.
- **COST-01:** `PriceTable.ExpiredAt(now)` menandai route yang entri terakhirnya sudah lewat `until`. `TestPricingTableNotExpired` memakai tabel embedded + `time.Now()`, yaitu gate yang disengaja untuk gagal saat harga berakhir tanpa pengganti. Saat ini tabel open-ended, sehingga tidak ada risiko gagal palsu di CI.
- **Konsumsi window:** `window.ContextWindow` hanya dipasang pada `Route.ContextWindow`, yang di provider hanya dipakai untuk skip route fallback (`client.go:123`, `i > 0`). Guard budget memakai estimasi + cap, dan truncation/compaction tetap memakai `loop.turn_max_input_tok`. **Window yang membengkak saat ini tidak melemahkan guard budget maupun truncation.** Namun nilai tersebut sudah tersimpan di `model_limits`, sehingga akan berdampak begitu ADR-126 menyambungkan `Limits` ke assemble (mode degradasi).

### Probe

| Pesan error provider (400 `context_length_exceeded`) | `model_limits` | Run berikutnya |
|---|---|---|
| "maximum context length is 99999999999999 tokens" (request "hi") | `openai/gpt-fake 99999999999999 learned` | lanjut (window 10¹⁴ tersimpan) |
| "maximum context length is 100 tokens" | `… 100 learned` | **gagal saat start**: `context window below 8192 tokens … learned window 100`, 0 request |
| "Your prompt mentions 'context window is 5000000' …" (teks bebas/echo) | `… 5000000 learned` | lanjut |
| "maximum context length is 9223372036854775808" | (tidak disimpan: `ParseInt` overflow) | — |

Parser (`internal/provider/contextlength.go:16`): `"input 200000 tokens > 128000"` → 128.000 ✔. `"…context window of 10000000 in your prompt"` → 10.000.000 (teks bebas diterima). `0` atau `-5` → ditolak ✔. Regex RE2 berjalan linear, dan body dibatasi 64 KiB.

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC6-15 | **MEDIUM** | confirmed | **Window learned dari teks error yang dikendalikan provider, tanpa batas dan tanpa uji kewajaran, lalu dipersistenkan.** `ContextWindow` (`contextlength.go:16`) memindai seluruh `Body` error (termasuk teks bebas atau gema prompt) dengan pola longgar (`context window (?:is\|of) (\d+)`). `Limits.Observe` (`limits.go:118`) hanya menolak `window ≤ 0`, dan upsert menimpa nilai learned lama tanpa batas atas atau bawah. Akibatnya: (a) **DoS persisten**: window < 8.192 membuat setiap `run` untuk `provider/model` tersebut gagal dengan `ErrWindowTooSmall` sampai ada override config atau baris DB dihapus; (b) **window membengkak** (10¹⁴) tersimpan sebagai sumber kebenaran untuk konsumen berikutnya; (c) kunci `provider/model` tidak memuat `base_url`, sehingga reseller di bawah nama provider yang sama dapat meracuni window route vendor. | Terima window learned hanya bila **lebih kecil dari ukuran request yang gagal** (error context_length berarti request > window): `window < est_input × CalibrationMax + max_tokens`, dan berada dalam `[MinWindow, 10.000.000]`. Nilai di bawah `MinWindow` dicatat sebagai peringatan, bukan dipersistenkan sebagai fakta yang memblokir start. Hanya izinkan *penurunan* terhadap window learned atau fallback saat ini. Parse hanya `error.message` dengan pola "maximum context length". Kunci window learned per `base_url` (atau tolak learned untuk nama provider yang punya entri registry). Tambahkan test untuk ketiga kasus probe. |
| SEC6-16 | MINOR | static | **Konflik nomor ADR.** #188 mendefinisikan `ADR-129` sebagai wiring harga/limit, sedangkan ADR-124 di #185 (`f2-crypto`) sudah merujuk "ADR-129" sebagai catatan keputusan owner/scope F2 (enkripsi at-rest default mati). Setelah keduanya di-merge, rujukan ADR-124 akan menunjuk ke ADR yang salah. Branch #188 juga belum memuat ADR-122/127 dari `main` (stack lama), sehingga perlu rebase. | Tetapkan satu nomor untuk masing-masing (mis. ADR-130 untuk wiring limit, atau ubah rujukan di ADR-124), lalu periksa dengan `adrgate` setelah rebase ke `main`. |
| SEC6-17 | MINOR | static | `[[providers.custom]]` menerima `base_url`, `max_tokens`, dan `capabilities` yang belum dipakai. `base_url` divalidasi hanya untuk whitespace (bukan URL), dan override `context_length` dikunci pada `name`, bukan `base_url`, sehingga `--base-url` lain dengan nama provider yang sama memakai override yang sama. Tidak ada jalur injeksi harga dari config (harga tetap hanya dari `go:embed`). | Validasi `base_url` dengan `NormalizeBaseURL`, dan dokumentasikan bahwa override window berlaku per nama provider (atau kunci per `base_url`). |
| SEC6-18 | nit | static | `ContextWindow` meng-compile 5 regex pada setiap panggilan (`regexp.MustCompile` di dalam loop). | Jadikan variabel paket. |

**Verdict #188 (`e5676df`): REQUEST CHANGES** (SEC6-15 MEDIUM; SEC6-16 perlu diselesaikan sebelum merge bersama #185).

---

## Round 7 — 2026-10-21 (#188 head `5741027`)

**Verdict #188: APPROVE.** SEC6-15, SEC6-16, SEC6-17, dan SEC6-18 FIXED. SEC6-11b/13/14b (dipindah dari #184) FIXED dengan satu residual MINOR. Seluruh probe peracunan window kini gagal-tertutup atau terbatas.

Head yang direview: #188 `f2-cost-limits-wiring` `5741027` (commit `4d66225` wiring + `b296283` perbaikan + `5741027` ADR). Gate: `go test -race -tags goolm -count=1 ./internal/provider/... ./internal/config/... ./internal/ctxdb/limitstore/... ./internal/budget/... ./internal/loop/... ./cmd/damnbot/... ./internal/archtest/...` → **semua ok**. `make loc`: `provider/registry 383 / 400`, `provider 1872 / 2000`, `config 753 / 1200`. Jumlah core `loc.budget` = **36.690** = Total core tabel dan prosa ENGINEERING. `globals.allow` menambah `contextWindowPatterns` (E-10). Probe `TestZZ7Poison` (server 400 `context_length_exceeded`, `run` dua kali per pesan) dijalankan lalu dihapus.

### Probe

| Pesan `error.message` | Prompt | `model_limits` setelah 2 run | Run kedua |
|---|---|---|---|
| "maximum context length is 99999999999999" | "hi" | **kosong** (> 10 jt, juga > request) | jalan normal (tanpa abort) |
| "maximum context length is 100" | "hi" | **kosong** (< 8.192) | **tidak abort** (round 6: abort permanen) |
| "Your prompt mentions 'context window is 5000000'" | ±60 rb token | **kosong** (hanya pola "maximum context length") | — |
| "maximum context length is 500000" | "hi" | **kosong** (≥ input × 1,5 + `max_tokens`) | — |
| "maximum context length is 32000" | ±60 rb token | `http://127.0.0.1:<port> gpt-fake 32000 learned` ✔ (wajar, disimpan per base_url) | memakai 32.000 |
| Server A: 20.000 lalu 60.000; server B: 9.000 | ±60 rb token | A = **20.000** (kenaikan ke 60.000 ditolak); B = 9.000 terpisah | — |

### Verdict per temuan

| ID | Verdict | Bukti |
|---|---|---|
| SEC6-15 | **FIXED** | `ContextWindow` (`internal/provider/contextlength.go`) hanya membaca `Error.Message` terstruktur (`env.Error.Message`, `openai.go:167`) dengan satu pola terkompilasi `maximum context length\s*(?:is|of|:)\s*(\d+)`. `Limits.Learn` (`internal/provider/registry/limits.go`): `[MinWindow 8.192, MaxWindow 10.000.000]`, `window < estInput + estInput/2 + max_tokens` (`EstInputTokens` dihitung sendiri, bukan dari provider), sumber `discovery` tidak pernah ditimpa, hanya penurunan atas nilai learned, dan nilai tidak wajar dibuang (tidak pernah dipersistenkan sebagai fakta yang memblokir). Kunci store = **base_url ternormalisasi + model** (`WindowFor`), sehingga reseller tidak dapat meracuni window vendor; override config juga dikunci per base_url. Baris lama berkunci nama provider dari round 6 tidak lagi dibaca. |
| SEC6-16 | **FIXED** | Wiring limit kini **ADR-130**. `DECISIONS.md` memuat ADR-122/127 dari `main` dengan urutan 120, 122, 123, 125, 126, 127, 128, 130. ADR-129 tetap milik keputusan owner yang dirujuk ADR-124 (#185). |
| SEC6-17 | **FIXED** | `validBaseURL` (config) = http(s), ada host, tanpa userinfo/query/fragment/opaque. Composition root menormalkan ulang dengan `registry.NormalizeBaseURL`, dan nilai yang tidak valid menggagalkan start. |
| SEC6-18 | **FIXED** | Regex dikompilasi sekali (variabel paket, ada di allowlist globals). |
| SEC6-11b | **FIXED** | `Do` menyimpan `lastResp` (dengan `Price`/`EstInputTokens`) pada jalur "retry habis", sehingga attempt berbayar di route ber-harga mendapat proyeksi, bukan NULL. |
| SEC6-13 | **FIXED (residual)** | `billedUsage` = `est × 1,5` (batas atas kalibrasi ADR-101) + 8 token per pesan + `max_tokens`. Residual MINOR: bila provider menolak parameter `max_tokens` dan parameter itu di-drop (`rejectedParam`), output tidak lagi dibatasi tetapi proyeksi tetap memakai `max_tokens`; gambar juga tidak dihitung. |
| SEC6-14b | **FIXED** | `retryDenied` memakai `estimateMicros(price, …)` untuk route ber-harga (retry diizinkan selama proyeksi masih muat di cap), dan `-1` (fail-closed) untuk route tanpa harga. |

**Residual:** provider dapat menurunkan window learned **pada base_url-nya sendiri** sampai ≥ 8.192, bila request cukup besar sehingga lolos uji kewajaran. Dampaknya terbatas pada route tersebut, dan saat ini window hanya dipakai untuk skip fallback.

**Verdict #188 (`5741027`): APPROVE.**
