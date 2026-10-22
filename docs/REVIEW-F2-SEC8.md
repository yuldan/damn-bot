# REVIEW-F2-SEC8 — Review keamanan carrier reasoning (GAP-PROV-01, ADR-131)

Review independen untuk carrier reasoning opaque (thinking signature, encrypted reasoning, dan sejenisnya). Stack: #190 `f2-reasoning-carriers` (`2289938` + `6c70493`, basis `main` `0128e6e`) → #193 `f2-carrier-projection` `15c2845`. Reviewer tidak menulis kode yang direview, dan kode produk tidak diubah. Tanggal: 2026-10-21.

## Round 1 — 2026-10-21

**Verdict per PR:**

| PR | Head | Verdict |
|---|---|---|
| #190 carrierstore | `6c70493` | **REQUEST CHANGES (kecil)**: `make loc` **GAGAL** di head ini (`ctxdb/carrierstore 263 > 250`; budget 300 baru masuk di #193). SEC8-6 MINOR. Logika store sendiri baik. |
| #193 proyeksi/capture | `15c2845` | **REQUEST CHANGES**: SEC8-1 (HIGH, setiap turn ber-carrier gagal di `run` nyata), SEC8-2 (MEDIUM, carrier yang ditolak tetap di-replay), SEC8-3 (MEDIUM, byte carrier yang terkena redaksi tetap tersimpan), SEC8-4/5 (MINOR) |

Gate di `15c2845`: `go test -race -tags goolm -count=1 ./internal/ctxdb/carrierstore/... ./internal/provider/... ./internal/loop/... ./cmd/damnbot/... ./internal/archtest/...` → **ok**. Namun lihat SEC8-1: test e2e memakai store palsu atau `Append` tanpa payload carrier.

Probe `TestZZCarrierRun` (`run` nyata + `providertest`, memeriksa body request) dijalankan lalu dihapus. Untuk mengamati perilaku replay di belakang SEC8-1, satu probe dijalankan dengan patch sementara (`nonRefSha64` menerima `"sha"`). Patch itu sudah dikembalikan (`git checkout`).

### Probe end-to-end

Skrip server: (1) jawaban tool call + carrier `{"type":"thinking","thinking":"plan SIG1","signature":"SIG1"}`; (2) 400 `invalid_signature`; (3) tool call + `SIG2`; (4) tool call + `SIG3`; (5) teks. Setelah itu `run` kedua dijalankan pada data dir yang sama (sesi baru).

| Kondisi | Hasil |
|---|---|
| `run` nyata, tanpa patch | Turn pertama yang menerima carrier gagal: `stop=error/internal`, `ctxdb: sha under non-ref key "payload.carriers[0].sha"` → **SEC8-1** |
| Dengan patch sementara: request 2 (iterasi 2, sesi yang sama, route yang sama) | berisi `SIG1` ✔ (replay ke asal) → 400 |
| Request 3 (retry sekali tanpa carrier) | tanpa SIG ✔; penolakan dicatat (`reasoning_carrier_rejections`: carrier 1 `invalid_signature`) ✔ |
| Request 4 (iterasi berikutnya) | **berisi `SIG1` lagi** + `SIG2` → **SEC8-2** (carrier yang ditolak tetap di-replay) |
| Request 5 (`run` kedua, sesi baru) | **tanpa** SIG apa pun ✔ (isolasi lintas sesi) |
| Family yang tersimpan untuk route `openai/m` dengan `--base-url` loopback | `openai` → **SEC8-4** (family = nama provider, bukan protokol atau base_url) |

### Temuan

| ID | PR | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|---|
| SEC8-1 | #193 | **HIGH** | confirmed | **Turn yang menerima carrier gagal di `run` nyata.** Payload event assistant memuat `carriers:[{ord,sha}]`, tetapi validator blob-ref ctxdb (`internal/ctxdb/append.go` `walkRefs`) menolak string 64-hex di bawah kunci selain `blob_sha`/`*_blob_sha(s)` atau allowlist `nonRefSha64` (`preview_sha`, `args_sha`, `payload_sha`). Kunci `sha` tidak ada di allowlist, sehingga `Append` mengembalikan `ErrInternal` dan turn berakhir `error/internal`. Akibatnya setiap model reasoning yang mengirim signature membuat turn gagal, dan carrier tidak pernah tersimpan. Test loop memakai store palsu, dan `TestCarrierHistoryIsolationEndToEnd` meng-append event dengan payload `{}`, sehingga jalur ini tidak teruji. | Pakai kunci non-ref yang sudah diizinkan (`payload_sha`) di `CarrierRef`, atau tambahkan `"sha"` ke allowlist dengan alasan di ADR, lalu tambahkan test `run` end-to-end dengan carrier di respons. |
| SEC8-2 | #193 | **MEDIUM** | confirmed (dengan patch) | **Carrier yang ditolak tetap di-replay.** `RejectCarriers` menulis `reasoning_carrier_rejections`, tetapi `ctxStore.loadCarriers` (`cmd/damnbot/carriers_wire.go:96`) dan `projectCarriers` (`internal/provider/carriers.go:85`) tidak pernah membaca `Rejected()`. Iterasi berikutnya mengirim lagi carrier yang sama, sehingga setiap request berikutnya kena 400 + retry: **dua request per call** sepanjang sesi, dan klaim ADR-131 "tidak pernah di-replay" tidak terpenuhi. | Saring id hasil `Rejected(sid)` di `loadCarriers` (atau set `Replayable=false` saat load), dan tambahkan test 3 iterasi: request ke-3 tidak memuat carrier yang ditolak. |
| SEC8-3 | #193 | **MEDIUM** | static | **Byte carrier tetap disimpan walaupun reasoning-nya terkena redaksi.** Bila `t.redact(ReasoningText)` mengubah teks, `accept` (`internal/loop/turn.go:222`) hanya menandai carrier `Replayable=false`, tetapi `saveCarriers` tetap menyimpan `Raw` byte-identik. Untuk kind `thinking`, `Raw` berisi teks thinking plaintext, sehingga secret yang terdeteksi redaksi masuk ke `reasoning_carriers.payload` (dan ke backup) tanpa redaksi. Pengecualian §14.3 dimaksudkan untuk replay, padahal carrier yang tidak replayable tidak punya kegunaan replay. | Bila tidak replayable karena redaksi, simpan hanya `sha` dan metadata (`payload` kosong atau hasil redaksi, sehingga skema perlu kolom nullable atau penanda), atau jangan simpan carrier sama sekali. Tambahkan test: secret di thinking → tidak ada byte secret di DB. |
| SEC8-4 | #193 | MINOR | confirmed | **Family jatuh ke nama provider.** `Route.CarrierFamily()` = `Family`, atau `Provider` bila kosong, dan `run` tidak pernah mengisi `Route.Family`. Route reseller `openai/<model>` dengan `--base-url` pihak ketiga memakai family **`openai`**, sama dengan route vendor. ADR-131 butir 1 justru menyatakan family adalah "family protokol, bukan nama reseller", dengan fallback ke `Provider` "sehingga reseller tidak mewarisi family vendor". Kenyataannya, nama provider dipilih pengguna dan sama untuk keduanya. Saat ini kebocoran dibatasi karena carrier di-scope per sesi dan `run` memakai satu route per sesi. Dengan resume/fork atau multi-route nanti, byte terenkripsi milik vendor dapat dikirim ke reseller dengan `(family, model)` yang sama. | Masukkan base_url ternormalisasi ke identitas carrier (`family = protokol + "@" + NormalizeBaseURL(base)`), atau isi `Route.Family` dari registry dan tolak replay bila base_url berbeda. Tambahkan test dua route bernama sama dengan base_url berbeda → carrier di-drop (`route_mismatch`). |
| SEC8-5 | #190/#193 | MINOR | static | **Tidak ada batas ukuran per carrier atau per sesi.** Satu respons dibatasi assembler `maxResponseBytes` 16 MiB (`reasoningCost` 3× raw → ±5 MiB raw) dan `captureCarriers` membatasi 256 part, tetapi `carrierstore.Save` tidak membatasi `len(payload)`. Setiap respons dapat menambah beberapa MiB ke DB, dan semua carrier riwayat di-replay di setiap request, sehingga body request dan biaya input membengkak. Provider yang buggy atau jahat dapat menggembungkan DB dan biaya. | Batasi per carrier (mis. 256 KiB) dan per pesan (mis. 1 MiB) di capture dan di `Save` (`ErrCarrier`). Carrier yang melebihi batas di-drop dengan `carrier_dropped{reason:"too_large"}`. |
| SEC8-6 | #190 | MINOR | confirmed | `make loc` **gagal** pada head #190: `ctxdb/carrierstore 263 > 250`. Budget naik ke 300 hanya di #193 (ADR-131 amandemen round 2). Jumlah `loc.budget` = Total core ENGINEERING (37.020) sudah konsisten, tetapi gate LOC gagal bila #190 di-merge sendirian. | Pindahkan kenaikan `carrierstore 250 → 300` beserta amandemennya ke #190, atau merge keduanya bersama. |

### Diperiksa dan bersih

- **Isolasi sesi** (`carrierstore`): `Save` memverifikasi bahwa `event_id` milik `sessionID`. `Load`/`BySHA`/`Rejected` difilter `events.session_id`. `Reject` menolak carrier dari sesi lain. Tabel bersifat append-only (trigger `carriers_no_update/no_delete`) dan dijaga `lease_fenced`/`session_purging` (0008). Event fork milik leluhur tidak dimuat ke anak (arah aman). Probe: `run` kedua (sesi baru) → 0 byte carrier di body request; `TestCarrierHistoryIsolationEndToEnd` lulus.
- **Proyeksi:** encoder hanya mengirim carrier dengan `(family, model)` persis sama dan `Replayable=true`, dan JSON harus valid. Selain itu carrier di-drop dengan `CarrierDrops` → `system_notice{kind:"carrier_dropped"}`. Saat retry tanpa carrier, `withoutCarriers` mengosongkan carrier di semua pesan. Family/model dicap dari route yang menghasilkan (`captureCarriers` per route di `call`), bukan dari pemanggil.
- **Pemisahan plaintext:** hanya part objek yang punya `thought_signature`/`signature`/`encrypted_content`/`reasoning_opaque` yang menjadi carrier; part lain tetap di `Reasoning` (plaintext). Carrier dihapus dari `Reasoning` sehingga tidak dikirim ganda. `reasoning_content` (string) tidak pernah menjadi carrier.
- **Batas retry:** signature-invalid hanya dikenali bila status 400 dan `error.code ∈ {invalid_signature, signature_invalid}`, dan hanya bila request memang membawa carrier (`len(sent) > 0`). Retry dilakukan **sekali** tanpa carrier lalu langsung kembali (tidak ada loop). 400 adalah `BadRequest` yang tidak di-retry oleh `retry.Do`, sehingga maksimal 2 request per call. `TestCarrierSignatureRetryOnce`, `TestCarrierSignatureIgnoredWithoutCarriers`.
- **Pencatatan penolakan:** id dipetakan lewat `BySHA` dalam sesi yang sama; PK `(carrier_id, session_id)` + `INSERT OR IGNORE` membuat pencatatan idempoten (tetapi lihat SEC8-2: hasilnya belum dipakai).
- **Budget/ADR:** #193 `provider 2000 → 2100` (terukur 2.005 oleh `make loc`; komentar menyebut 2.010), `loop 1800 → 1900` (1.851), `carrierstore 250 → 300` (263), semuanya didukung ADR-131 amandemen round 2 butir 6 (Total 37.020 → 37.270). Jumlah `loc.budget` = Total core tabel dan prosa ENGINEERING di kedua head (37.020 / 37.270).

**Ringkasan:** #190 REQUEST CHANGES (gate LOC; pindahkan kenaikan budget). #193 REQUEST CHANGES (SEC8-1 HIGH; SEC8-2/3 MEDIUM; SEC8-4/5 MINOR).

## Round 2 — 2026-10-21

Stack (basis `main` `0128e6e`): #190 `f2-reasoning-carriers` `8cc41b9` (`2289938`, `6c70493`, `824547f`, `8cc41b9`) → #193 `f2-carrier-projection` `255b220` (`b620cdf`, `255b220`) → #200 `f2-carrier-caps` `c5d92d2`. Range-diff terhadap Round 1: `2289938`/`6c70493` identik. `15c2845 → b620cdf` hanya memindahkan kenaikan budget `carrierstore` ke #190. Perbaikan ada di `824547f`, `255b220`, dan `c5d92d2`.

**Verdict per PR:**

| PR | Head | Verdict |
|---|---|---|
| #190 carrierstore | `8cc41b9` | **APPROVE dengan MINOR**: SEC8-1 (sisi ctxdb), SEC8-5 (store), dan SEC8-6 diperbaiki. SEC8-7 MINOR (allowlist `sha` berlaku global). |
| #193 proyeksi/capture | `255b220` | **REQUEST CHANGES**: SEC8-1/2 FIXED (dikonfirmasi `run` nyata), SEC8-4 FIXED, tetapi SEC8-3 hanya **parsial**: SEC8-8 MEDIUM (secret di teks carrier bersarang tetap tersimpan dan di-replay). SEC8-9 MINOR (klaim normalisasi family tidak sesuai kode). |
| #200 batas capture/proyeksi | `c5d92d2` | **APPROVE** (SEC8-5 lengkap untuk per carrier/per pesan; nit ADR) |

Gate di `c5d92d2`: `go test -race -tags goolm -count=1 ./internal/ctxdb/carrierstore/... ./internal/provider/... ./internal/loop/... ./cmd/damnbot/... ./internal/archtest/...` → **ok**. `GOOS=windows|darwin go vet ./cmd/damnbot ./internal/provider` → ok. Di setiap head (`8cc41b9`, `255b220`, `c5d92d2`), `go vet -tags goolm ./...` dan `make loc` lulus (`carrierstore 275/300`; `provider 1768/2000 → 2015/2100 → 2031/2100`; `loop 1757/1800 → 1849/1900`). Jumlah `loc.budget` = Total core ENGINEERING (tabel dan prosa): 37.070 / 37.270 / 37.270. Test paket yang relevan juga lulus di `8cc41b9` dan `255b220`.

Probe `TestZZSec8R2*` (`run` nyata + `providertest` + ctxdb nyata, memeriksa body request dan file DB/WAL) dan `TestZZShaAllowlist` (`ctxdb.BlobRefsOf`) dijalankan lalu dihapus. Tidak ada patch produk.

### Probe end-to-end (skrip yang sama dengan Round 1, argumen `todo` divariasikan agar tidak `no_progress`)

| Kondisi | Hasil |
|---|---|
| `run` nyata (route `openai/gpt-fake`, `--base-url` loopback) | exit 0, `stop=end_turn/completed` ✔. Tiga baris `reasoning_carriers` tersimpan → **SEC8-1 FIXED** |
| Request 2 (iterasi 2) | membawa `SIG1` → 400 `invalid_signature` |
| Request 3 (retry sekali) | tanpa carrier ✔; `reasoning_carrier_rejections` = `[1 invalid_signature]` ✔ |
| Request 4 / 5 | `[SIG2]` / `[SIG2 SIG3]`: **`SIG1` tidak di-replay lagi** → **SEC8-2 FIXED** |
| Request 6 (`run` kedua, sesi baru) | tanpa carrier ✔ (isolasi sesi) |
| `provider_family` tersimpan | `http://127.0.0.1:<port>/v1` (base URL route), bukan `openai` → **SEC8-4 FIXED** (reseller `openai/<model>` di base URL lain tidak berbagi family dengan vendor) |
| Redaksi: secret di `thinking` (`type:thinking` + `signature`) | 0 carrier, 0 byte secret di DB/WAL, tidak di-replay ✔ |
| Redaksi: secret di `text` part Gemini (`thought_signature`) | 0 carrier, 0 byte ✔ |
| Redaksi: secret terpecah di dua part `thinking` | 0 carrier, 0 byte ✔ (gabungan teks tetap terdeteksi) |
| Redaksi: secret di **`summary[].text`** carrier `encrypted_content` | **1 carrier tersimpan, byte secret ada di DB, dan di-replay di request berikutnya** → SEC8-8 |

### Status temuan Round 1

| ID | Verdict | Bukti |
|---|---|---|
| SEC8-1 | **FIXED** | `nonRefSha64` menerima `sha` (`824547f`). `TestCarrierRunPersistsWithRealStore` menjalankan `run` + ctxdb nyata. Probe: turn ber-carrier selesai (exit 0) dan carrier tersimpan. Lihat SEC8-7 untuk cakupan allowlist. |
| SEC8-2 | **FIXED** | `loadCarriers` menyaring `store.Rejected(sid)` (error `Load` dan `Rejected` keduanya diperiksa). Probe: `SIG1` tidak muncul lagi setelah penolakan. Penyaringan berlaku karena loop memuat ulang riwayat dari store setiap iterasi. `TestCarrierRejectedNotReplayed` ada. |
| SEC8-3 | **PARSIAL** | `accept` kini membuang `reply.Carriers` bila redaksi mengubah `ReasoningText` (tidak ada lagi `Replayable=false` + simpan `Raw`). Namun `ReasoningText` dibentuk `partText`, yang hanya mengambil string `text`/`thinking`/`content` tingkat atas per part, sehingga teks di field bersarang (`summary[].text` pada blok `encrypted_content`) tidak dipindai → SEC8-8. |
| SEC8-4 | **FIXED** | `run` mengisi `Route.Family: base`, dan hanya ada satu konstruksi `provider.Route{…}` (run.go:316). Replay exact-match tetap berfungsi karena capture (`captureCarriers`) dan proyeksi (`projectCarriers`) sama-sama memakai `rt.CarrierFamily()` dari route yang sama. Probe membuktikan replay `SIG2`/`SIG3` ke origin. Fallback `CarrierFamily() → Provider` masih ada bila `Family` kosong (laten; tidak ada pemanggil saat ini). Lihat SEC8-9. |
| SEC8-5 | **FIXED** (per carrier/per pesan) | Store (#190): `Save` → `ErrCarrier` untuk >256 KiB/carrier atau >1 MiB/pesan. Capture (#200): blok berlebih dibuang (tidak dikembalikan ke `Reasoning`) + `too_large`. Proyeksi (#200): `too_large`, tidak dikirim. Residual: tidak ada batas total per request/sesi (`projectCarriers` dipanggil per pesan), sehingga riwayat panjang tetap bisa membawa N × 1 MiB. Ini bukan blocker; catat di ADR atau pertimbangkan batas per request. |
| SEC8-6 | **FIXED** | `carrierstore 250 → 300` + amandemen pindah ke #190, dan `make loc` lulus di `8cc41b9`. |

### Temuan baru

| ID | PR | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|---|
| SEC8-7 | #190 (`ctxdb/append.go` `nonRefSha64`) | MINOR | confirmed | **Allowlist `sha` berlaku global berdasarkan nama kunci, bukan path.** `BlobRefsOf` kini menerima 64-hex di bawah **`sha` di mana saja**, di semua dokumen dan owner (events.payload, `actions.inverse`, dst). Probe: `{"sha":H}`, `{"attachments":[{"sha":H}]}`, dan `{"blob":{"sha":H}}` → `refs=0, err=nil` (sebelumnya ditolak), sedangkan `x_sha` tetap ditolak dan `blob_sha` tetap menjadi ref. Saat ini tidak ada writer yang menaruh blob sha di kunci `sha` (`grep`: hanya `CarrierRef`/`carrierstore.Ref`), jadi belum ada kehilangan data. Namun guard fail-loud yang mencegah blob ref "tak terlihat" oleh GC (§11.6) melemah: writer berikutnya yang memakai `sha` untuk blob akan diterima diam-diam, lalu blob-nya bisa di-GC. | Batasi ke path: hanya `payload.carriers[N].sha` (cocokkan `path`, bukan `key`), atau ganti kunci payload menjadi `carrier_sha`/`payload_sha` yang sudah ada. Tambahkan test: `{"x":{"sha":H}}` tetap ditolak. |
| SEC8-8 | #193 (`loop/turn.go accept` + `provider/assemble.go partText`) | **MEDIUM** | confirmed | **Redaksi carrier tidak memindai teks bersarang.** Pemeriksaan redaksi memakai `ReasoningText`, yang dibentuk `partText`. Fungsi itu hanya membaca `text`/`thinking`/`content` tingkat atas tiap part. Blok `{"type":"reasoning","summary":[{"type":"summary_text","text":"…"}],"encrypted_content":"…"}` (bentuk reasoning item OpenAI) membawa ringkasan plaintext di `summary[].text`, sehingga secret di sana tidak terdeteksi. Probe (secret `<redacted>…`): **1 baris `reasoning_carriers` dengan byte secret di file DB, dan secret ikut di request berikutnya**. Ini bertentangan dengan CHANGELOG/ADR-131 amandemen SEC8 butir 3 ("tidak ada byte secret di `reasoning_carriers`/backup"). | Pindai seluruh string di `Raw` setiap carrier secara rekursif, kecuali field opaque (`signature`, `thought_signature`, `encrypted_content`, `reasoning_opaque`, `data` milik `redacted_thinking`): buang carrier bila `redact(s) != s`. Atau buat `partText` rekursif. Tambahkan kasus `summary[].text` dan teks bersarang lain ke `TestCarrierRedactionDropsBytes`. |
| SEC8-9 | #193 (`run.go:316`, ADR-131 amandemen SEC8 butir 4, CHANGELOG) | MINOR | confirmed | **Family = string `base` mentah, bukan "identitas route ternormalisasi (scheme/host/port/path)" seperti klaim ADR/CHANGELOG.** Probe: `provider_family` = `http://127.0.0.1:<port>/v1` persis seperti input. (a) Ejaan berbeda untuk endpoint yang sama (`…/v1` vs `…/v1/`, host berhuruf besar, port default eksplisit), padahal request memakai `TrimSuffix(base,"/")`, menghasilkan family berbeda → carrier di-drop `route_mismatch` (fail-safe, tidak bocor). Saat ini `run` selalu memakai sesi baru, jadi belum terpicu. (b) Userinfo/query di base URL (`https://user:pass@host/v1`, `?key=`) **tersimpan plaintext** di `reasoning_carriers.provider_family` (dan ikut backup). `--base-url` saat ini berlabel internal/test, sehingga dampaknya rendah. | Normalisasi sekali di `provider` (parse URL: scheme+host lowercase, port default dihapus, path tanpa trailing slash, **tolak/hapus userinfo, query, dan fragment**), dan pakai hasil yang sama untuk request dan `Family`. Sebaiknya set di `NewOpenAI` bila `Family` kosong, sehingga fallback ke `Provider` hilang. Samakan teks ADR/CHANGELOG. |

Nit: ADR-131 amandemen SEC8 butir 5 menyebut batas capture/proyeksi ada di "PR bertumpuk #194", padahal PR-nya #200 (#194 adalah restore backup).

**Ringkasan:** #190 **APPROVE** (dengan SEC8-7 MINOR; disarankan path-scoped sebelum merge). #193 **REQUEST CHANGES** (SEC8-8 MEDIUM: secret di `summary[].text` tersimpan dan di-replay; SEC8-9 MINOR). #200 **APPROVE**. Setelah diperbaiki, jalankan ulang probe redaksi dengan `summary[].text` dan teks bersarang, test allowlist berbasis path, dan probe family dengan `…/v1/` + userinfo.

## Round 3 — 2026-10-21

Stack (basis `main` `0128e6e`): #190 `bd96e44` (+ `bd96e44` di atas `8cc41b9`) → #193 `1997987` (`d775b16`, `1997987`) → #200 `2bc1601`. Range-diff terhadap Round 2: `2289938`..`8cc41b9` identik. `d775b16`/`1997987` hanya berubah di test (`carrierReplyValue`; `TestCarrierRedactionDropsBytes` memeriksa History + file DB/WAL). `c5d92d2` diganti `2bc1601` (batas ukuran + redaksi rekursif + `NormalizeBaseURL`).

**Verdict per PR:**

| PR | Head | Verdict |
|---|---|---|
| #190 carrierstore | `bd96e44` | **APPROVE**: SEC8-7 FIXED (nit path) |
| #193 proyeksi/capture | `1997987` | **APPROVE bersyarat: hanya di-merge bersama #200** (atomik, atau squash #200 ke #193). Sendirian, SEC8-8 dan SEC8-9 masih terbuka (dikonfirmasi probe di bawah). |
| #200 batas + redaksi + family | `2bc1601` | **APPROVE dengan MINOR**: SEC8-8/9 FIXED, SEC8-10 MINOR, SEC8-11 nit |

**Apakah #193 dan #200 harus di-merge bersama? Ya.** Di `1997987` saja: secret di `summary[].text` / field bersarang **tersimpan di DB dan di-replay** (SEC8-8), dan **password userinfo base URL tersimpan plaintext** di `reasoning_carriers.provider_family` (SEC8-9). Keduanya persisten: baris carrier bersifat append-only (trigger `carriers_no_update/no_delete`) dan ikut backup, sehingga data yang terlanjur tertulis di antara merge #193 dan #200 tidak terhapus oleh #200. Karena itu, jangan ada build/rilis `main` yang memuat #193 tanpa #200. Merge keduanya berurutan dalam satu jendela merge (tanpa rilis di antaranya), atau gabungkan #200 ke #193.

Gate di `2bc1601`: `go test -race -tags goolm -count=1 ./internal/ctxdb/carrierstore/... ./internal/provider/... ./internal/loop/... ./cmd/damnbot/... ./internal/archtest/...` → **ok**; `make deadcode` → 0 fungsi tak terjangkau. Di setiap head (`bd96e44`, `1997987`, `2bc1601`): `go vet -tags goolm ./...` + `GOOS=windows|darwin go vet` bersih, test paket relevan ok, dan `make loc` lulus (`carrierstore 275/300`, `ctxdb 3428/3500`; `provider 1768 → 2015 → 2048 / 2100`; `loop 1757/1800 → 1849/1900 → **1893/1900**`, sisa 7 baris). Jumlah `loc.budget` = Total core ENGINEERING: 37.070 / 37.270 / 37.270.

Probe `TestZZSec8R3*` (`run` nyata + `providertest` + ctxdb nyata; memindai semua file data dir), `TestZZSha3` (`ctxdb.BlobRefsOf`), dan `TestZZNorm` (`provider.NormalizeBaseURL`) dijalankan di `2bc1601`, dan probe e2e juga di `1997987`, lalu dihapus.

### Redaksi bersarang (secret `<redacted>…` di reasoning carrier; tool call → request ke-2)

| Kasus | `1997987` (#193 saja) | `2bc1601` (#200) |
|---|---|---|
| `summary[].text` pada blok `encrypted_content` | 1 carrier, **secret di disk, di-replay** | 0 carrier, 0 byte di disk, tidak di-replay; notice `carrier_dropped{reason:"redacted",count:1}` ✔ |
| String bersarang dalam (`x.y[].z`) | **bocor** | di-drop ✔ |
| Secret terpecah di dua entri `summary` | **bocor** | di-drop ✔ (setiap potongan masih cocok pola) |
| Secret di field `data` pada blok `thinking` (bukan `redacted_thinking`) | **bocor** | **bocor**: 1 carrier, secret di disk, di-replay → SEC8-10 |
| Secret sebagai **nama kunci** objek | **bocor** | **bocor** → SEC8-10 |
| Carrier bersih (`summary` tanpa secret) | tersimpan + di-replay ✔ | tersimpan + di-replay ✔ (tidak ada false drop) |

### Family / base URL (tool loop 2 iterasi, `SIGF1` harus di-replay ke route yang sama)

| `--base-url` | `1997987` family | `2bc1601` family | Replay `SIGF1` (req 2, 3) | Password di disk |
|---|---|---|---|---|
| `http://127.0.0.1:P/v1` | `…/v1` | `…/v1` | ✔ / ✔ | – |
| `http://127.0.0.1:P/v1/` | `…/v1/` | **`…/v1`** | ✔ / ✔ | – |
| `http://user:PASSWORDCANARY@127.0.0.1:P/v1/` | **`http://user:PASSWORDCANARY@…/v1/`** | **`http://127.0.0.1:P/v1`** | ✔ / ✔ | `1997987`: **ya**; `2bc1601`: tidak ✔ |

`NormalizeBaseURL` (unit): `/v1` = `/v1/` = `HTTPS://API.OpenAI.com:443/v1//` → `https://api.openai.com/v1`; userinfo, `?key=SECRET`, dan `#frag` dibuang ✔. Path tidak di-`Clean` (`/v1/../v1`) dan huruf path dipertahankan (`/V1`), sehingga hasil yang berbeda hanya membuat carrier di-drop `route_mismatch` (fail-safe).

### Status temuan

| ID | Verdict | Bukti |
|---|---|---|
| SEC8-7 | **FIXED** (#190) | `nonRefSha64(key, path)`: `sha` dikecualikan hanya bila path cocok `(^|\.)carriers\[\d+\]\.sha$`. Probe: `payload.sha`, `payload.attachments[0].sha`, `payload.blob.sha`, dan `payload.xcarriers[0].sha` → **ditolak** lagi; `payload.carriers[0].sha` → diterima (0 ref). Nit: regex tidak dijangkar ke `^payload\.`, sehingga `payload.result.carriers[0].sha` dan `inverse.carriers[0].sha` (`actions`) juga dikecualikan. Saat ini tidak ada writer seperti itu. Pertimbangkan `^payload\.carriers\[\d+\]\.sha$`. |
| SEC8-8 | **FIXED di #200**, terbuka di #193 | `filterCarriers` + `carrierHasSecret` memindai setiap string secara rekursif (field opaque dilewati) dan men-drop per carrier dengan notice `redacted`. Lihat tabel; residual di SEC8-10. |
| SEC8-9 | **FIXED di #200**, terbuka di #193 | `Family: provider.NormalizeBaseURL(base)`. Capture dan proyeksi memakai nilai yang sama, sehingga replay exact-match tetap berfungsi untuk `/v1`, `/v1/`, dan userinfo (probe). Residual: SEC8-11. |
| SEC8-5 | **FIXED** | Tetap seperti Round 2 (store #190; capture/proyeksi #200). Residual non-blocking: tidak ada batas total per request. |

### Temuan baru

| ID | PR | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|---|
| SEC8-10 | #200 (`loop/turn.go carrierHasSecret`) | MINOR | confirmed | (a) Kunci **`data` dilewati di semua objek**, padahal hanya opaque untuk `type:"redacted_thinking"`. Blok `thinking`/`reasoning` yang membawa teks di `data` tidak dipindai → secret tersimpan dan di-replay (probe). (b) **Nama kunci** objek tidak dipindai (probe: bocor). Kedua bentuk ini tidak lazim pada provider saat ini, tetapi carrier dari reseller atau provider yang tidak dikenal adalah input tak tepercaya. | (a) Lewati `data` hanya bila `type == "redacted_thinking"` (field opaque lain tetap per nama). (b) Pindai juga kunci map. Tambahkan kedua kasus ke `TestCarrierNestedSecretDropped`. |
| SEC8-11 | #200 (`provider/baseurl.go`) | nit | confirmed | `NormalizeBaseURL` mengembalikan **input mentah** bila `url.Parse` gagal atau host kosong. Contoh: `https://user:pa ss@host/v1` → userinfo (password) tetap utuh di family. Di jalur `run`, URL seperti itu juga gagal saat request, sehingga carrier tidak terbentuk (tidak ada kebocoran yang teramati). IPv6 `https://[::1]:443/v1` → `https://::1/v1` (bracket hilang; tetap unik). | Saat parse gagal, kembalikan string kosong/error (route ditolak di `NewOpenAI`) alih-alih input mentah. Pakai `net.JoinHostPort` untuk host IPv6. |

**Ringkasan:** #190 **APPROVE**. #193 **APPROVE bersyarat**: harus di-merge bersama #200 (tanpa build/rilis di antaranya), karena sendirian SEC8-8/9 menulis secret dan password secara permanen ke tabel append-only. #200 **APPROVE** dengan SEC8-10 MINOR + SEC8-11 nit; boleh ditangani sebelum merge atau di follow-up yang dicatat. Catatan: budget `loop` tersisa 7 baris (1893/1900).

## Round 4 — 2026-10-21 (konfirmasi singkat)

Stack: #190 `a651254` → #193 `a64648c` → #200 `0697eac` (`b81c0c8` + `0697eac`). Range-diff `0128e6e..2bc1601` vs `0128e6e..b81c0c8`: semua commit lama **identik** (`d775b16 = 8403b62`, `1997987 = a64648c`, `2bc1601 = b81c0c8`). Yang baru hanya `a651254` (#190) dan `0697eac` (#200). **#193 hanya di-restack.**

**Verdict per PR:**

| PR | Head | Verdict |
|---|---|---|
| #190 carrierstore | `a651254` | **APPROVE** (nit SEC8-7 path ditutup) |
| #193 proyeksi/capture | `a64648c` | **APPROVE bersyarat**: tetap hanya di-merge bersama #200 (alasan Round 3 tidak berubah, karena #193 sendiri identik dengan `1997987`) |
| #200 batas + redaksi + family | `0697eac` | **APPROVE**: SEC8-10/11 FIXED; SEC8-12 MINOR (boleh follow-up) |

Gate di `0697eac`: `go test -race -tags goolm -count=1 ./internal/ctxdb/carrierstore/... ./internal/provider/... ./internal/loop/... ./cmd/damnbot/... ./internal/archtest/...` → **ok**; `make deadcode` → 0. Di setiap head (`a651254`, `a64648c`, `0697eac`): `go vet -tags goolm ./...` + `GOOS=windows|darwin go vet` bersih, test paket relevan ok, dan `make loc` lulus (`loop 1757/1800 → 1849/1900 → 1913/1950`, `provider 1768/2000 → 2015/2100 → 2056/2100`, `carrierstore 275/300`, `ctxdb 3428/3500`). **Budget `loop` 1.900 → 1.950 didukung ADR-131 amandemen SEC8 butir 6** (terukur 1.913; Total core 37.270 → 37.320). Jumlah `loc.budget` = Total core ENGINEERING (tabel dan prosa) = **37.070 / 37.270 / 37.320** ✔.

Probe `TestZZSec8R4` (`run` nyata + `providertest` + ctxdb nyata; memindai semua file data dir) dijalankan lalu dihapus.

| Kasus (secret `<redacted>…`) | Hasil di `0697eac` |
|---|---|
| `data` pada blok `type:"thinking"` | 0 carrier, 0 byte di disk, tidak di-replay ✔ (Round 3: bocor) |
| Secret sebagai **nama kunci** di `summary[]` | di-drop ✔ (Round 3: bocor) |
| `summary[].text` pada `encrypted_content` | di-drop ✔ |
| `encrypted_content` pada `type:"message"` (bukan `reasoning`) | dipindai → di-drop ✔ |
| Carrier bersih (`reasoning` + `encrypted_content`; `thinking` + `signature` base64) | tersimpan + di-replay ✔ (tidak ada false drop pada signature) |

| `--base-url` | Family | Replay `SIGF1` (req 2/3) | Password di disk |
|---|---|---|---|
| `http://127.0.0.1:P/v1/` | `http://127.0.0.1:P/v1` | ✔ / ✔ | – |
| `http://user:PASSWORDCANARY@127.0.0.1:P/v1` | `http://127.0.0.1:P/v1` | ✔ / ✔ | tidak ✔ |
| `HTTP://127.0.0.1:P/v1` | `http://127.0.0.1:P/v1` | ✔ / ✔ | – |
| `https://user:PASSWORDCANARY@host:bad/v1`, `https://user:PASSWORD CANARY@host/v1`, `host.example/v1` | ditolak, `run` exit 2 ✔; tidak ada apa pun di disk | – | **password muncul di stderr** → SEC8-12 |

### Status temuan

| ID | Verdict | Bukti |
|---|---|---|
| SEC8-7 nit | **FIXED** (#190) | `reCarrierSha = ^payload\.carriers\[\d+\]\.sha$`; `inverse.carriers[…]` dan `payload.result.carriers[…]` tidak lagi dikecualikan. |
| SEC8-10 | **FIXED** (#200) | `walkCarrier` memindai setiap string **dan nama kunci**. `opaqueCarrierField` hanya melewati pasangan persis: `thought_signature`/`reasoning_opaque` (semua tipe), `encrypted_content` (`reasoning`), `data` (`redacted_thinking`), `signature` (`thinking`/`redacted_thinking`). Test: `TestCarrierNonOpaqueDataScanned`, `TestCarrierSecretInKeyDropped`, `TestCarrierOpaquePairsKept`. |
| SEC8-11 | **FIXED** (#200) | `NormalizeBaseURL` → `(string, error)`. URL yang tidak bisa diparse, tanpa host, atau tanpa scheme ditolak di `providerEndpoint` (route tidak terbentuk). IPv6 lewat `net.JoinHostPort`/bracket (`[::1]:443` → `https://[::1]`). Path memakai `EscapedPath()` tanpa trailing slash. |

### Temuan baru

| ID | PR | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|---|
| SEC8-12 | #200 (`provider/baseurl.go`) | MINOR | confirmed | Pesan error `provider: invalid base_url %q` **mengutip URL mentah**, termasuk userinfo. `run` mencetaknya ke stderr (`run: provider endpoint: base_url: provider: invalid base_url "https://user:PASSWORDCANARY@host:bad/v1"`), sehingga kredensial masuk log/CI/terminal. Saat ini `--base-url` berlabel internal/test, tetapi `base_url` dari config akan memakai jalur yang sama. | Jangan mengutip input: kembalikan error tanpa nilai, atau redaksi userinfo/query sebelum format (mis. `url.Redacted()` bila parse berhasil; selain itu cukup "invalid base_url"). Tambahkan test bahwa stderr tidak memuat password. |

**Ringkasan:** #190 **APPROVE**; #193 **APPROVE bersyarat** (merge bersama #200, tanpa build/rilis di antaranya); #200 **APPROVE** (SEC8-12 MINOR dapat ditangani di follow-up). Seluruh temuan SEC8-1..11 tertutup di ujung stack. Koordinasi dengan stack kripto (enkripsi `reasoning_carriers.payload_enc`) dibahas di REVIEW-F2-SEC7 Round 6.

## Round 5 — 2026-10-21 (konfirmasi SEC8-12)

#200 `6f5f644` (di atas `0697eac`): `NormalizeBaseURL` kini memformat error dengan `redactBaseURL(raw)`, yaitu userinfo, query, dan fragment dibuang, sedangkan nilai yang tidak bisa di-parse menjadi `"<invalid>"`. #190 `a651254` dan #193 `a64648c` tidak berubah.

**Verdict:** #200 **APPROVE** (SEC8-12 FIXED untuk URL ber-authority; residual SEC8-13 MINOR, follow-up). #190 **APPROVE**; #193 **APPROVE bersyarat** (merge bersama #200, sama seperti Round 3/4).

Gate di `6f5f644`: `go test -race -tags goolm -count=1 ./internal/provider/... ./cmd/damnbot/` → ok; `go vet` bersih; `make loc` `provider 2064/2100`.

Probe `TestZZRedact` (`NormalizeBaseURL` → teks error) dijalankan lalu dihapus:

| Input | Error |
|---|---|
| `https://user:PASSWORDCANARY@host:bad/v1` | `invalid base_url "<invalid>"` ✔ (Round 4: password tercetak) |
| `https://user:PASSWORD CANARY@host/v1` | `"<invalid>"` ✔ |
| `https://user:PASSWORDCANARY@/v1`, `//user:PASSWORDCANARY@/v1` | `"https:///v1"` / `"/v1"` ✔ |
| `https:///v1?key=PASSWORDCANARY`, `api.example.com/v1?key=PASSWORDCANARY` | query dibuang ✔ |
| **`user:PASSWORDCANARY@host/v1`** (tanpa `//`) | **`"user:PASSWORDCANARY@host/v1"`**: di-parse sebagai scheme `user` + `Opaque`, sehingga password tercetak → SEC8-13 |
| **`https:user:PASSWORDCANARY@host/v1`** | **tercetak utuh** (opaque) → SEC8-13 |
| `sk-live-PASSWORDCANARY` (kunci yang salah ditempel ke `base_url`) | tercetak utuh → SEC8-13 |

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC8-12 | – | **FIXED** | URL ber-authority yang mengandung userinfo/query tidak lagi tercetak. | – |
| SEC8-13 | MINOR | confirmed | `redactBaseURL` hanya mengosongkan `User`/`RawQuery`/`Fragment`. URL **opaque** (tanpa `//`) menyimpan kredensial di `u.Opaque`, sehingga tetap dicetak, begitu juga string tanpa scheme. | Cetak hanya `scheme://host[:port]` bila `u.Host != ""` dan `u.Opaque == ""`; selain itu `"<invalid>"`. Tambahkan kedua bentuk opaque ke `TestNormalizeBaseURL`. |
