# REVIEW-F2-SEC10 — Review keamanan gerbang skill pembayaran + lint skill (GAP-SEC-15/SEC-26, ADR-133)

Review independen untuk #207 `f2-skills-gaps` `ac65fdd` (basis `main` `68dbec3`): front matter `side_effects`/`requires`, `skills.PaymentDirs` → `classify.Env.MonetaryPaths` (kelas `Monetary` untuk perintah shell yang menjalankan atau menyebut file skill pembayaran), default testnet, dan `skills.Lint` di `Service.Install`. Reviewer tidak menulis kode yang direview, dan kode produk tidak diubah. Tanggal: 2026-10-21.

## Round 1 — 2026-10-21

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #207 skills side_effects + lint | `ac65fdd` | **REQUEST CHANGES**: SEC10-1 (HIGH: script skill pembayaran berjalan tanpa approval lewat `cd <skill>; ./script`), SEC10-2/3 (MEDIUM), SEC10-4..7 (MINOR) |

Gate di `ac65fdd`:
- `go test -race -count=1 ./internal/skills/... ./internal/exec/classify/... ./internal/tools/... ./internal/archtest/...` → **ok**.
- `go vet -tags goolm ./...` + `GOOS=windows|darwin go vet` → bersih.
- `make deadcode` → 0.
- `make loc`: `exec/classify 1755/1800`, `tools 2153/2200`, `memory+skills 1079/1200`. Jumlah `loc.budget` = Total core (36.770) ✔.

Probe `TestZZSec10*` (`classify.Classify` + `Decide` untuk 5 mode × `shell_allow` cocok/tidak), `TestZZSec10Tool` (dispatcher `tools` nyata + `fakeRunner`, mode `auto` dan `run --approval auto --unsafe-auto-shell`), dan `TestZZLintBypass` (`skills.Lint` atas `fstest.MapFS`) dijalankan lalu dihapus. Kode decision: P = ask+preview, Q = ask, A = auto, D = denied, O = outside_workspace. Kolom: ask, auto-edit, auto, headless, headless `--unsafe-auto-shell`.

### Klasifikasi Monetary: yang sudah benar

| Bentuk (P = direktori skill pembayaran) | Kelas | Decision (allow / tanpa allow) |
|---|---|---|
| `$P/scripts/x.sh`, `python3 $P/…`, `env`/`timeout python3 $P/…`, `bash -c "$(cat $P/…)"`, `cat $P/… \| python3`, `python3 - < $P/…`, `source`/`. $P/…`, `find $P … -exec`, `git -C $P` | monetary | PPPDD / PPPDD ✔ |
| `cp $P/x.py /tmp/x.py && python3 /tmp/x.py`, `ln $P/x.py <ws>/h.py`, `ln -s $P /tmp/s && …` | monetary (operand baca) | PPPDD ✔ |
| `$P/../stripe-pay/…`, `//`, `~/…`, `$P\/…`, `strip\e-pay`, `'$P'/…`, `$(echo $P)/…`, `` `echo $P`/… ``, glob `stripe-*/…` (resolver) | monetary | PPPDD ✔ |
| Indireksi: `D=$P; python3 $D/…`, `"$SKILL_DIR/…"`, `export D=…`, here-doc, `python3 -c "exec(open('$P/…'))"`, `perl -e 'do …'`, `PATH=$P/scripts:$PATH x.py`, `xargs`, `pushd`, `cd $P && python3 scripts/x.py` (operand relatif setelah `cd`) | unknown | PPPDD ✔ (gagal-tertutup: unknown tidak pernah auto) |
| `curl -X POST`/`-d`/`wget --post-data` ke `api.stripe.com` (juga huruf besar); `api.stripe.com.:443` | monetary / irreversible | PPPDD ✔ |
| `MonetaryPaths` kosong | `$P/scripts/x.sh` → mutating (tanpa deklarasi tidak ada Monetary, sesuai desain) | – |

`Monetary`: preview di semua mode interaktif, `denied` headless dan `--unsafe-auto-shell`, `allow_session` tidak ditawarkan dan tidak berlaku (`TestShellPaymentSkillGate`, `TestMonetaryAlwaysPrompts`) ✔ sesuai ADR-088/P26.

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC10-1 | **HIGH** | confirmed | **Script skill pembayaran berjalan tanpa approval lewat argv0 relatif setelah `cd`.** `command.go` hanya memeriksa `paymentFile` untuk argv0 bila `s.abs(args[0])` berhasil. Aturan "relative operand after cd → unknown" hanya berlaku untuk **operand**, bukan argv0. Classifier: `cd $P/scripts; ./x.py`, `cd $P/scripts && ./x.py`, `cd $P/scripts\n./x.py`, `cd $P; scripts/x.py`, `cd $P; ./scripts/x.py`, `cd ~/…/stripe-pay/scripts; ./x.py` → **mutating**, decision allow `AAAAA`, tanpa allow `QQQDA` (**auto di `--unsafe-auto-shell`**). Dispatcher nyata (`tools`): `run --approval auto --unsafe-auto-shell` + `cd <payDir>/scripts; ./charge.sh --amount 100` → **runs=1, prompts=0** (kontrol `<payDir>/scripts/charge.sh` → `denied`, runs=0). Di mode `auto`, permintaan berlabel `shell:mutating` (bukan monetary), tanpa preview payment, sehingga `allow_session` ditawarkan dan berlaku untuk pengulangan. Ini melanggar konsekuensi ADR-133 "Script skill pembayaran tidak pernah berjalan tanpa approval manual, di semua mode". (Pra-ada dan lebih umum: argv0 relatif setelah `cd` ke luar workspace juga tetap mutating/auto, mis. `cd /etc; ./x`.) | Perlakukan argv0 relatif setelah `cd`/`pushd` sama seperti operand: unknown (atau resolve terhadap cwd yang dilacak, lalu `paymentFile`). Tambahkan kasus `cd $P/scripts; ./x.py` dan `cd $P; scripts/x.py` ke `TestPaymentSkillMonetary` dan `TestMonetaryAlwaysPrompts` (mode headless unsafe: denied, runs=0). |
| SEC10-2 | **MEDIUM** | confirmed | **Lint hanya memindai file yang dirujuk body dalam bentuk tertentu.** File di direktori skill yang dirujuk tanpa prefix `scripts/` atau placeholder (`python3 tools/pay.py`, `bin/pay` tanpa ekstensi), modul yang di-import script (`scripts/helper.py` dari `run.py`), dan file yang tidak dirujuk sama sekali **tidak pernah dibaca**. Probe: semuanya berisi `gh auth token`, `~/.aws/credentials`, dan `stripe`, dengan hasil `findings=[]`. Akibatnya `credential_access` (high, menolak skill) dan `payment_undeclared` dapat dihindari hanya dengan menaruh kode di file lain. | Pindai **setiap file reguler** di direktori skill (lewat `os.Root`, batas jumlah/ukuran total, fail-closed bila terlampaui), bukan hanya referensi body. Referensi body tetap divalidasi keberadaannya. |
| SEC10-3 | **MEDIUM** | static | **Skill pembayaran yang tidak dideklarasikan tidak pernah di-gate.** `payment_undeclared` hanya temuan medium. Skill sumber trusted (`user`/`builtin`) diindeks langsung, dan direktorinya tidak masuk `PaymentDirs`, sehingga script-nya mutating (auto di `--unsafe-auto-shell`/`shell_allow`). Selain itu, ADR-133 butir 8 menyebut `PaymentDirs` dari skill yang **approved**. Skill pembayaran yang masih `scanned`/quarantine tetap punya file di disk tetapi tidak di-gate. | `PaymentDirs` = semua skill terpasang (status apa pun) yang mendeklarasikan `payment` **atau** punya temuan `payment_undeclared`. Alternatif: `payment_undeclared` → `invalid_skill` untuk sumber trusted. |
| SEC10-4 | MINOR | confirmed | Lint berbasis pola huruf kecil tanpa normalisasi: base64 (`echo Z2gg… \| base64 -d \| sh`), konkatenasi (`a=gh; $a auth tok""en`, `${S}ipe`), dan homoglyph (`stгipe`, `gh аuth token` Kiril) → `findings=[]`. Ini sudah diakui ADR-133 (obfuscation), tetapi belum ada mitigasi murah. | Normalisasi NFKC + pemetaan confusable sebelum pencocokan; flag `base64 -d \| sh`/`eval "$(…)"`/`exec(base64…)` sebagai temuan `obfuscated_exec` (medium/high). |
| SEC10-5 | MINOR | confirmed | False reject: `python3 "$SKILL_DIR"/scripts/run.py` dipecah tokenizer pada `"`, sehingga token `/scripts/run.py` dianggap absolut lalu `invalid_skill: … outside the skill directory`. Bentuk ber-kutip adalah gaya shell yang benar untuk path yang mengandung spasi. | Kenali `"$SKILL_DIR"/`, `"${SKILL_DIR}"/` sebelum memecah token. |
| SEC10-6 | MINOR | static | **Gerbang belum tersambung dan tidak gagal-tertutup saat lookup tidak dikonfigurasi.** `cmd/damnbot` belum mengisi `ShellConfig.MonetaryPaths`/`MonetaryHosts`, `skills.Config.Known`/`Files` (ADR-133 butir 8). Tanpa `MonetaryPaths`, classifier tidak pernah mengembalikan Monetary untuk file skill (probe), dan error memuat skill di wiring nanti akan mematikan gerbang secara diam-diam. Lint sendiri gagal-tertutup bila `Files`/`Known` nil ✔. | Pada wiring: bila skill dimuat tetapi `PaymentDirs` gagal dihitung, tandai seluruh root skill sebagai Monetary (gagal-tertutup). Tambahkan test integrasi `run` + skill pembayaran di disk. |
| SEC10-7 | MINOR (pra-ada, ADR-079) | confirmed | `MonetaryHosts` hanya mencocokkan host URL: `curl https://1.2.3.4/v1/charges -H 'Host: api.stripe.com'` dan GET ke host monetary → mutating (allow `AAAAA`). Klien di dalam interpreter (`python3 -c 'urllib…api.stripe.com'`) → unknown (P). | Catat sebagai residual ADR-079. Opsional: header `Host:` yang cocok `MonetaryHosts` → Monetary. |

Catatan non-blocking:
- Salin script ke workspace lewat `read_file`/`write_file` (bukan shell), lalu `python3 <ws>/x.py` → mutating (allow `AAAAA`; `--unsafe-auto-shell` auto). Ini setara dengan model menulis kode pembayaran sendiri dan sudah diakui ADR-133 (−) "pembayaran lewat perintah yang tidak menyebut file skill". Mitigasinya ada di luar classifier: kredensial pembayaran tidak diekspor ke shell (`SecretVars`), atau `MonetaryHosts` di egress.
- Batas 256 KiB: script tepat di bawah batas tetap dipindai (temuan muncul), dan script di atas batas yang dirujuk → `invalid_skill` ✔. File di atas batas yang **tidak** dirujuk diabaikan (termasuk SEC10-2).
- Symlink keluar direktori skill tidak dibaca (`os.Root`, `TestLintFailsClosed`) ✔; path `..`/absolut/`~`/drive di referensi → `invalid_skill` ✔.

**Ringkasan:** #207 **REQUEST CHANGES**. SEC10-1 (HIGH) membuat script skill pembayaran tetap auto-run di `--unsafe-auto-shell`, dan tanpa preview Monetary di mode interaktif, hanya dengan `cd` + argv0 relatif. SEC10-2/3 (MEDIUM) membuat lint dan gate mudah dihindari lewat file yang tidak dirujuk atau skill yang tidak mendeklarasikan `payment`. Yang sudah benar: klasifikasi path absolut/symlink/interpreter/pipe/redirect/substitusi, indireksi variabel/here-doc → unknown (tidak pernah auto), semantik P26 (tanpa `allow_session`, headless denied), dan lint gagal-tertutup untuk referensi menggantung/symlink/ukuran.

## Round 2 — 2026-10-21

Stack: #207 `462989c` (gate: SEC10-1/3/7) → #212 `f2-skills-sec10b` `d131e95` (scan seluruh direktori, normalisasi, wiring `run`: SEC10-2/4/5/6).

**Verdict per PR:**

| PR | Head | Verdict |
|---|---|---|
| #207 gate classifier + `Sensitive` | `462989c` | **APPROVE dengan MINOR**: SEC10-1/7 FIXED (dispatcher nyata). SEC10-3 parsial (SEC10-9, diperbaiki di #212 atau di sini). Gate belum disambungkan di PR ini, sehingga dampak produksinya nol. |
| #212 lint mendalam + wiring | `d131e95` | **REQUEST CHANGES**: SEC10-8/9/10 (MEDIUM). SEC10-5/6 FIXED; SEC10-4 parsial (MINOR) |

Gate di kedua head: `go test -race -tags goolm -count=1 ./internal/skills/... ./internal/exec/classify/... ./internal/tools/... ./internal/archtest/... ./cmd/damnbot/` → ok; `go vet -tags goolm ./...` + `GOOS=windows|darwin go vet` bersih; `make deadcode` 0; `go mod tidy -diff` bersih (`golang.org/x/text` sudah dependency langsung di `main`, sudah ada di `deps.allow`). `make loc`: `exec/classify 1786/1800`, `memory+skills 1088 → 1160/1200`, `tools 2153/2200`. Jumlah `loc.budget` = Total core (36.770) ✔.

Probe (dijalankan lalu dihapus): `TestZZR2Classify` (30 bentuk perintah × 5 mode × `shell_allow`), `TestZZR2Tool` (**dispatcher `tools` nyata**, mode `auto` dengan jawaban `allow_session` dan `run --approval auto --unsafe-auto-shell`), `TestZZR2Lint` (16 kasus `skills.Lint`), dan `TestZZR2Wire` (`paymentSkillDirs` atas `<config>/skills` nyata di disk).

### Dispatcher nyata (P = `<ws>/.skills/stripe-pay`, `MonetaryPaths=[P]`, `MonetaryHosts=[api.stripe.com]`)

| Perintah | `auto` (jawaban `allow_session`) | `--unsafe-auto-shell` |
|---|---|---|
| `P/scripts/charge.sh --amount 100` | prompt `shell:monetary`, `allow_session` tidak ditawarkan | `denied`, runs=0 |
| `cd P/scripts; ./charge.sh --amount 100` (Round 1: runs=1, prompts=0) | prompt `shell:monetary` | **`denied`, runs=0** ✔ |
| `cd P && scripts/charge.sh`, `pushd P/scripts && ./charge.sh`, `(cd P/scripts && ./charge.sh)`, `cd .skills/stripe-pay/scripts && ./charge.sh` | prompt `shell:monetary` | `denied`, runs=0 ✔ |
| `cd .skills && cd stripe-pay && ./scripts/charge.sh` | prompt `shell:unknown` (tanpa `allow_session`) | `denied`, runs=0 ✔ |
| `curl -H 'Host: api.stripe.com' https://1.2.3.4/v1/charges` | prompt `shell:monetary flags:network` | `denied`, runs=0 ✔ |

Classifier (tambahan): `cd $(echo P); ./x.py`, `sh -c 'cd P/scripts; ./x.py'`, `env -C P/scripts ./x.py`, `git -C P`, `make -C P`, `curl https://api.stripe.com/v1/balance` (GET), `-HHost:…`, `wget --header='Host: …'`, `curl --resolve api.stripe.com:443:1.2.3.4 https://api.stripe.com/…` → monetary. `cd "$D"; ./x.py`, `cd -; ./x.py`, `cd …/stripe-*; ./x.py` → unknown (tidak pernah auto). `X-Host: api.stripe.com`, `api.stripe.com.evil.com`, `evilapi.stripe.com` → mutating (benar, bukan host monetary).

### Lint dan wiring

| Kasus | Hasil |
|---|---|
| Kode di `.lib/pay.py` (`gh auth token`, `import stripe`), dirujuk body `python3 .lib/pay.py` | **`findings=[]`**, `Sensitive=false` → SEC10-8 |
| Kode di `lib/.x/pay.py` | **`findings=[]`** → SEC10-8 |
| File tersembunyi di root (`.pay.py`) | `credential_access high` ✔ |
| `tools/pay.py` berisi `import stripe` (tanpa deklarasi) | temuan `payment_undeclared` medium, tetapi **`Sensitive=false`** → SEC10-9 |
| File biner (`bin/pay`, ELF + string) | `credential_access high` ✔ |
| `echo … \| base64 -d \| sh` | `obfuscated_exec` medium ✔ |
| `c=$(… \| base64 -d); $c`, `os.system(base64.b64decode(…))`, `a=gh; $a auth tok""en`, `… \| rev`, `zcat p.gz \| sh` | `findings=[]` → SEC10-4 (residual) |
| `gh аuth token` (Kiril), fullwidth `ｇｈ ａｕｔｈ ｔｏｋｅｎ` | `credential_access high` ✔ (NFKC + fold) |
| `gh au\u200bth token` (zero-width space) | `findings=[]` → SEC10-4 |
| `stгipe` (Kiril `г`) di body | `Sensitive=false` (`г` tidak ada di tabel fold) → SEC10-4 |
| `python3 "$SKILL_DIR"/scripts/run.py` | diterima ✔ (SEC10-5) |
| 64 file / > 64 file / > 1 MiB | 64 diterima; batas → `invalid_skill` (`TestLintBounds`) ✔ |
| `paymentSkillDirs`: skill `declared` (payment), `files-only` (stripe di `tools/pay.py`), `hidden` (`.lib/`), `linked` (**symlink** ke dir skill yang mendeklarasikan payment), `nomd` (script tanpa `SKILL.md`) | **hanya `[declared]`** → SEC10-9 (`files-only`), SEC10-8 (`hidden`), SEC10-10 (`linked`) |
| Skill rusak (`scripts/missing.sh`) | `run` gagal: `invalid_skill … does not exist` ✔ (gagal-tertutup) |

### Status temuan Round 1

| ID | Verdict | Bukti |
|---|---|---|
| SEC10-1 | **FIXED** (#207) | `changeDir` menaikkan Monetary untuk `cd`/`pushd`/`chdir`/`set-location` literal ke direktori pembayaran, dan argv0 relatif yang tidak ter-resolve → Unknown. Dispatcher nyata: 0 eksekusi di `--unsafe-auto-shell`; di `auto` berlabel monetary tanpa `allow_session`. |
| SEC10-2 | **PARSIAL** (#212) | Seluruh file dipindai (termasuk file biner dan file tersembunyi di root), tetapi **direktori ber-titik dilewati** → SEC10-8. |
| SEC10-3 | **PARSIAL** (#207) | `Sensitive()` = deklarasi atau pola pembayaran di **deskripsi/body**, tanpa filter status (test `pending`) ✔. Pola di file **tidak** membuat skill sensitif → SEC10-9. |
| SEC10-4 | **PARSIAL** (MINOR) | NFKC + fold Kiril/Yunani + `obfuscated_exec` untuk `base64 -d \| sh`/`eval "$(`/`exec(base64`. Residual: lihat tabel. Diakui ADR-133 butir 13 sebagai heuristik. |
| SEC10-5 | **FIXED** (#212) | Placeholder ber-kutip dibuka sebelum tokenisasi. |
| SEC10-6 | **FIXED dengan residual** (#212) | `run` memuat + me-lint `<config>/skills/*` (os.Root, temuan high → error) dan meneruskan `PaymentDirs` ke `MonetaryPaths`. Skill rusak → `run` gagal. Residual: SEC10-10; `security.monetary_hosts` belum punya kunci config (ADR butir 14). |
| SEC10-7 | **FIXED** (#207) | Semua metode ke host monetary + header `Host:` (termasuk `-HHost:`, `wget --header=`) → Monetary. |

### Temuan baru

| ID | PR | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|---|
| SEC10-8 | #212 (`lint.go scanDir`) | **MEDIUM** | confirmed | `scanDir` memanggil `fs.SkipDir` untuk setiap direktori yang namanya diawali `.`. Kode di `.lib/`, `lib/.x/`, `.hidden/` dan sejenisnya tidak pernah dipindai, sehingga `credential_access` dan pola pembayaran lolos (probe: `findings=[]`), dan direktorinya tidak masuk gate. Ini bertentangan dengan ADR-133 butir 12 ("setiap file reguler"). | Pindai juga direktori ber-titik (batas file/byte tetap berlaku), atau tolak skill yang memuat direktori ber-titik (`invalid_skill`). Tambahkan kasus `.lib/` ke korpus lint. |
| SEC10-9 | #207 (`Skill.Sensitive`) / #212 | **MEDIUM** | confirmed | `Sensitive()` hanya membaca deskripsi + body. Skill yang menaruh kode pembayaran di file (`tools/pay.py`: `import stripe`) hanya mendapat temuan `payment_undeclared` (medium, tidak memblokir sumber trusted), dan direktorinya **tidak** masuk `PaymentDirs` (probe wiring: `files-only` tidak ter-gate). Script-nya lalu mutating dan auto di `--unsafe-auto-shell`/`shell_allow`. Komentar kode menyatakan "Files are covered by the lint finding", padahal temuan itu tidak menggerakkan gate. | Turunkan status sensitif dari hasil lint: skill dengan temuan `payment`/`payment_undeclared` (dari file mana pun) masuk `PaymentDirs` (mis. `Lint` mengembalikan flag yang dipakai `paymentSkillDirs`). Test wiring: `files-only` ter-gate. |
| SEC10-10 | #212 (`skills_wire.go loadSkills`) | **MEDIUM** | confirmed | `loadSkills` melewati entri yang `!e.IsDir()`. Entri `os.ReadDir` untuk **symlink** ke direktori berarti `IsDir()=false`, sehingga skill yang dipasang lewat symlink (pola umum: `ln -s ~/src/my-skill ~/.config/damnbot/skills/`) **tidak di-lint dan tidak di-gate**, termasuk yang mendeklarasikan `side_effects: [payment]` (probe: `linked` tidak ada di hasil). Direktori berisi script tanpa `SKILL.md` juga diabaikan. | Untuk symlink: `os.Stat` target; bila direktori, lint lewat `os.OpenRoot` pada **target** dan masukkan target (dan path link) ke `MonetaryPaths`. Atau tolak symlink di root skill dengan error (gagal-tertutup). Pertimbangkan seluruh `<config>/skills` sebagai monetary bila ada entri yang tidak dapat diklasifikasikan. |
| SEC10-4 (residual) | #212 | MINOR | confirmed | Tidak terdeteksi: base64 lewat variabel, `b64decode` Python, konkatenasi string, `rev`, `zcat \| sh`, zero-width space (U+200B tidak dihapus NFKC), Kiril `г` (tidak ada di tabel fold). | Hapus rune kategori `Cf` sebelum pencocokan; tambah `г→r` dan confusable umum lain; tambahkan pola `b64decode`/`base64.b64decode`/`\| *(ba)?sh` setelah `zcat`/`gunzip`/`rev`. Tetap catat sebagai heuristik. |

**Ringkasan:** #207 **APPROVE dengan MINOR**: bypass `cd` (SEC10-1, HIGH) tertutup di dispatcher nyata, begitu juga Host/GET (SEC10-7). SEC10-9 (sensitivitas dari file) dapat diperbaiki di #207 atau #212. #212 **REQUEST CHANGES**: SEC10-8 (direktori ber-titik tidak dipindai), SEC10-9 (gate tidak mengikuti temuan file), dan SEC10-10 (skill lewat symlink tidak di-gate) membuat gate pembayaran yang baru disambungkan dapat dihindari tanpa obfuscation. Setelah diperbaiki, jalankan ulang `TestZZR2Wire` (`declared`, `files-only`, `hidden`, `linked` → semua ter-gate) dan dispatcher `--unsafe-auto-shell` atas script ketiganya.

## Round 3 — 2026-10-21

#212 `756a8bb` (di atas `d131e95`): scan direktori ber-titik, temuan pembayaran tingkat file menggerakkan gate (`Skill.AddFindings`), skill lewat symlink di-resolve dan kedua path di-gate, direktori tanpa `SKILL.md` di-gate, normalisasi (NFKC, hapus `Cf`, `г`, ekspansi assignment literal, `obfuscated_exec` untuk `b64decode`/`zcat|rev … | sh`). #207 `462989c` tidak berubah.

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #207 | `462989c` | **APPROVE** (tidak berubah; SEC10-9 kini ditutup di #212) |
| #212 | `756a8bb` | **REQUEST CHANGES**: SEC10-8/9/10 FIXED, tetapi SEC10-11 (MEDIUM: lint kuadratik → `run`/install macet beberapa menit) dan SEC10-12 (MEDIUM: file symlink di dalam skill tidak dipindai/di-gate) |

Gate di `756a8bb`: `go test -race -tags goolm -count=1 ./internal/skills/... ./internal/exec/classify/... ./internal/tools/... ./internal/archtest/... ./cmd/damnbot/` → ok; `go vet` + `GOOS=windows|darwin` bersih; `make deadcode` 0; `make loc` `memory+skills 1184/1200` (sisa 16 baris); jumlah `loc.budget` = Total core (36.770) ✔.

Probe (dijalankan lalu dihapus): `TestZZR3Wire` (`paymentSkillDirs` atas `<config>/skills` nyata) dan `TestZZR3Perf`/`TestZZR3Big` (`skills.Lint` dengan input besar).

| Skenario | Hasil |
|---|---|
| `declared`, `files-only` (stripe di `tools/pay.py`), `hidden` (`.lib/pay.py`), `linked` (symlink → dir di luar root), `nomd` (script tanpa `SKILL.md`) | **semua masuk `PaymentDirs`**, termasuk **target** symlink ✔ (Round 2: hanya `declared`) |
| Entri symlink menggantung di root skill | `run` gagal (`stat … no such file or directory`), gagal-tertutup ✔ |
| Skill `plain` dengan **`helper.py` = symlink ke file di luar** (`import stripe`, `gh auth token`) | lint **lolos tanpa temuan**, `plain` **tidak** di-gate → SEC10-12 |
| Lint: 20.000 assignment `v=x;` (80 KB) di script yang dirujuk | **3,2 s** |
| Lint: 40.000 assignment (160 KB) | **9,4 s** |
| Lint: file **tidak dirujuk** 480 KB (120.000 assignment), di bawah batas scan 1 MiB | **49,9 s** → SEC10-11 |
| Konten > 256 KiB pada script yang dirujuk | `invalid_skill` (batas `MaxScriptBytes`) ✔ |

### Status

| ID | Verdict | Bukti |
|---|---|---|
| SEC10-8 | **FIXED** | `scanDir` tidak lagi melewati direktori ber-titik; korpus `hidden-cred`/`hidden-pay`; wiring: `hidden` ter-gate. |
| SEC10-9 | **FIXED** | `AddFindings` → `filePayment`; wiring: `files-only` ter-gate. |
| SEC10-10 | **FIXED** | `os.Stat` mengikuti symlink dir; `resolveDirs` menambah target (`EvalSymlinks`); dir tanpa `SKILL.md` di-gate; link menggantung → gagal-tertutup. |
| SEC10-4 | **FIXED** (heuristik) | Zero-width, konkatenasi literal, `tok""en`, `b64decode`, `zcat \| sh`, `г` tercakup di `TestLintNormalization` + korpus. Tetap heuristik sesuai ADR-133. |

### Temuan baru

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC10-11 | **MEDIUM** | confirmed | **Lint kuadratik → DoS `run`/install.** `expandLiterals` meng-compile regexp baru dan menjalankan `ReplaceAllString` atas **seluruh teks gabungan** untuk **setiap** assignment. Kompleksitasnya O(assignment × ukuran), padahal teks bisa sampai `MaxScanBytes` 1 MiB. Terukur: 80 KB → 3,2 s, 160 KB → 9,4 s, 480 KB (file tidak dirujuk) → **49,9 s**; perkiraan ~1 MiB → beberapa menit. Karena `run` me-lint setiap skill di `<config>/skills` saat start (SEC10-6), dan `Service.Install` me-lint skill hub/import, satu skill pihak ketiga dapat membuat setiap `run` macet beberapa menit sebelum turn pertama. | Satu pass linear: kumpulkan assignment literal ke map (batasi misalnya 256 entri dan panjang nilai), lalu satu `ReplaceAllStringFunc` dengan satu regexp untuk `$name`/`${name}`, atau ekspansi per baris. Tambahkan test waktu (mis. 1 MiB < 1 s). |
| SEC10-12 | **MEDIUM** | confirmed | **File symlink di dalam direktori skill tidak dipindai dan tidak menggerakkan gate.** `scanDir` melewati entri non-reguler, termasuk symlink ke file. `os.Root` juga tidak mengikutinya, sehingga `plain/helper.py → /luar/evil.py` (berisi `import stripe`, `gh auth token`) lolos lint tanpa temuan dan direktori `plain` tidak di-gate. Classifier me-resolve `plain/helper.py` ke `/luar/evil.py`, yang tidak ada di `MonetaryPaths`, sehingga mutating dan auto di `--unsafe-auto-shell`/`shell_allow`. Ini setara SEC10-8 lewat jalur lain. | Tolak skill yang memuat symlink (file atau dir) di dalam direktorinya (`invalid_skill`), atau pindai target lewat `filepath.EvalSymlinks` dengan batas yang sama dan tandai skill sensitif bila targetnya sensitif. Tambahkan kasus korpus `symlink-helper`. |

**Ringkasan:** gate pembayaran kini mengikuti deklarasi, temuan file, dir ber-titik, symlink dir, dan dir tanpa `SKILL.md` (wiring nyata). #212 masih **REQUEST CHANGES** karena SEC10-11 (lint O(n²) membuat `run` macet oleh satu skill) dan SEC10-12 (symlink file di dalam skill menghindari scan dan gate).

## Round 4 — 2026-10-21

Stack di-rebase ke `main` `8117bc1`: #207 `43787db` (basis `98bf3ad`) → #212 `b4c90aa` (`1e9f2c1`, `93624cb`, `b4c90aa`). `git range-diff 68dbec3..756a8bb 8117bc1..93624cb`: `ac65fdd = 98bf3ad`. Pada `462989c → 43787db`, `d131e95 → 1e9f2c1`, dan `756a8bb → 93624cb`, perbedaannya **hanya konteks CHANGELOG** (entri `assemble` dari #166), tanpa perubahan kode. Jadi #207 murni rebase dari head yang sudah disetujui. Commit baru: `b4c90aa` (SEC10-11/12).

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #207 | `43787db` | **APPROVE** (rebase murni) |
| #212 | `b4c90aa` | **APPROVE dengan MINOR** (SEC10-11/12 FIXED; SEC10-13 MINOR) |

Gate: `go test -race -tags goolm -count=1 ./internal/skills/... ./internal/exec/classify/... ./internal/tools/... ./internal/archtest/... ./cmd/damnbot/` → ok di `43787db` dan `b4c90aa`; `go vet` + `GOOS=windows|darwin` bersih; `make deadcode` 0; `make loc`: `43787db` `memory+skills 1088/1200`; `b4c90aa` `memory+skills 1215/1230`, `exec/classify 1786/1800`. Jumlah `loc.budget` = Total core ENGINEERING: 36.970 (`43787db`, termasuk `assemble 1200` dari #166) dan **37.000** (`b4c90aa`, ADR-133 butir 21) ✔.

Probe (dijalankan lalu dihapus): `TestZZR4Lint` (`skills.Lint`) dan `TestZZR4Wire` (`paymentSkillDirs` atas `<config>/skills` nyata).

| Skenario | Round 3 | `b4c90aa` |
|---|---|---|
| File tidak dirujuk 480 KB (120.000 assignment) | 49,9 s | **0,16 s** ✔ |
| File tidak dirujuk ~1 MiB (260.000 assignment) | (menit) | **0,33 s** ✔ |
| ~1 MiB referensi `$abcdefghijklmno` + 1 assignment | – | 0,21 s ✔ |
| `a=gh; $a auth token` | – | `credential_access high` ✔ |
| 40 assignment umpan, lalu `a=gh; $a auth token` | – | **`findings=[]`** → SEC10-13 |
| `helper.py` = symlink ke file di luar (stripe + `gh auth token`) | lolos, tidak di-gate | `run` gagal: `invalid_skill: symlink helper.py in skill directory` ✔ |
| Symlink ke file di dalam skill (`alias.sh → scripts/pay.sh`) | – | `invalid_skill` ✔ |
| Symlink direktori di dalam skill (`lib → /tmp/…`) | – | `invalid_skill` ✔ |
| `SKILL.md` = symlink ke luar | – | `openat SKILL.md: path escapes from parent` (gagal-tertutup) ✔ |
| Direktori skill = symlink (pola `ln -s`) mendeklarasikan payment | link + target di-gate | link + target di-gate ✔ (tetap didukung) |

| ID | Verdict | Bukti |
|---|---|---|
| SEC10-11 | **FIXED** | `expandLiterals`: satu `FindAllStringSubmatchIndex` (≤ 32 assignment) + satu `ReplaceAllStringFunc`; dilewati bila teks > 2 MiB. Waktu terukur linear (tabel). |
| SEC10-12 | **FIXED** | `scanDir` menolak symlink apa pun di dalam skill; `SKILL.md` dibaca lewat `os.Root`; direktori skill yang berupa symlink tetap di-resolve, dan kedua path masuk `MonetaryPaths`. |

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC10-13 | MINOR | confirmed | Batas 32 assignment diambil dari **awal** teks gabungan. Umpan ≥ 32 assignment literal sebelum `a=gh; $a auth token` membuat ekspansi berhenti, sehingga `credential_access` tidak terdeteksi. Ini heuristik (ADR-133 butir 18/19), jadi dampaknya sama dengan obfuscation lain. | Ekspansi per file atau per baris dengan jendela lokal (assignment yang mendahului referensi dalam N baris), atau prioritaskan nama yang memang direferensikan. Alternatif: temuan `obfuscated_exec` bila jumlah assignment melebihi batas. |

**Ringkasan:** #207 **APPROVE** (rebase tanpa perubahan kode). #212 **APPROVE dengan MINOR**: lint kini linear (1 MiB < 0,4 s) dan symlink apa pun di dalam skill ditolak (gagal-tertutup), sementara gate symlink direktori skill tetap benar. SEC10-13 (umpan > 32 assignment) adalah residual heuristik.

## Round 5 — 2026-10-22

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #212 payment gate: kunci path lintas platform | `b6172bb` (di atas `f263e6e`, basis `92a4e58`) | **REQUEST CHANGES**: SEC10-14 (MEDIUM, Windows: junction tidak di-resolve). Perbaikan inti untuk macOS (`/var` ↔ `/private/var`, case) dan Windows (8.3, case) benar. Sisa SEC10-15..18 MINOR |

Lingkup: `classify.Env.MonetaryKey` (disuntik `ShellConfig.Canonicalize`), `monetaryKey` per build tag (`canonicalBase` = `Abs` → `EvalSymlinks` → `Clean` → `ToSlash`; Windows menambah `GetLongPathName` + lowercase; darwin lowercase), dan `paymentSkillDirs` yang kini mengembalikan direktori mentah (`resolveDirs` dihapus).

Gate di `b6172bb`: `go test -race -tags goolm -count=1 ./internal/exec/classify ./internal/tools ./internal/skills/... ./internal/archtest` → ok; `./cmd/damnbot -run 'Skill|Payment|Monetary|PathKey|Canon'` → ok; `go vet ./...` + `GOOS=windows|darwin` (cmd/damnbot, classify, tools) bersih; `GOOS=windows go build ./cmd/damnbot` ok; `make deadcode` 0; `make loc` `exec/classify 1798/1800` (hampir penuh; lihat Perbaikan SEC10-15). **Verifikasi sesungguhnya untuk darwin/Windows adalah CI mac/win pada head ini.** Di sini hanya ada probe Linux, simulasi dialek `Cmd`, dan review statis.

Probe `TestZZSec10R5*` (`cmd/damnbot` dengan `monetaryKey` Linux asli; `internal/exec/classify` dengan dialek `Cmd` dan kunci simulasi lowercase) dihapus setelah dijalankan.

| Probe (Linux, `monetaryKey` asli) | Hasil |
|---|---|
| gate = dir nyata; perintah via path nyata / via symlink ke dir skill / `./c.py` (symlink di workspace ke script) / `//` dan `./` / `../scripts/` | **monetary** ✔ |
| gate = symlink; perintah via path nyata / via symlink | **monetary** ✔ |
| sibling `stripe-pay-v2/x.py` (gate nyata maupun link) | mutating ✔ (prefix `kd + "/"`) |
| `stripe-pay/scripts/nonexist/../charge.py` | monetary ✔ (`Abs` membersihkan `..` lebih dulu) |
| `linkdir/scripts/new.py` (file **belum ada**, parent symlink) | mutating: `EvalSymlinks` gagal → fallback `Abs` mentah → SEC10-18 |
| `MonetaryKey == nil`, gate = symlink, perintah via path nyata | **mutating** → SEC10-17 |

| Probe (dialek `Cmd`, input ke `MonetaryKey` diamati) | Input kunci | Kelas | Keputusan ask/…/unsafe |
|---|---|---|---|
| `python C:\…\stripe-pay\scripts\charge.py` / case berbeda / slash maju | `C:/…/charge.py` | monetary ✔ | P…D |
| `…\charge.py::$DATA` (ADS) | `…/charge.py::$DATA` | **monetary** ✔ (prefix dir utuh) | — |
| `stripe-pay-v2\x.py` | — | mutating ✔ | — |
| `python \\?\C:\…\charge.py` | **`/`** (`?` dibaca glob; `path.Clean` meruntuhkan `//`) | mutating | `PPPPPPOOOO` → SEC10-15 |
| `python \\.\C:\…\charge.py` | `/C:/…` | mutating | `PPPPPPOOOO` → SEC10-15 |
| `python \\localhost\C$\…\charge.py` | `/localhost/C$/…` | mutating | `PPPPPPOOOO` → SEC10-15 |
| `python C:\work\j\charge.py` (path di workspace yang tidak di-resolve ke skill; model junction) | `C:/work/j/charge.py` | mutating | **`QAQAQADAAA`** (auto bila `shell_allow` cocok, dan di unsafe headless) → SEC10-14 |

### Jawaban per pertanyaan

- **`Canonicalize` nil di produksi?** Tidak terjangkau. Satu-satunya `tools.NewShell` non-test ada di `cmd/damnbot/run.go`, dan pemanggil itu menyetel `Canonicalize: monetaryKey`. Namun cabang fallback leksikal kini lebih lemah daripada sebelumnya karena `paymentSkillDirs` tidak lagi menambahkan target symlink (probe). Lihat SEC10-17.
- **`EvalSymlinks` gagal pada path yang tidak ada:** fallback ke `Abs` bersih tanpa resolusi. Tidak ada panic dan tidak ada kunci kosong. Path baru di bawah parent symlink tidak tergate, tetapi file yang belum ada tidak bisa dieksekusi. Penulisan ke dir skill lewat link tetap berstatus `Destructive`/ask karena berada di luar workspace. Risikonya rendah (SEC10-18).
- **`\\?\`, UNC, titik/spasi di akhir, ADS (Windows):** ADS `::$DATA` tetap monetary. Bentuk `\\?\`, `\\.\`, dan `\\host\share` **dirusak oleh `classify.clean`** sebelum sampai ke kunci, sehingga label Monetary hilang. Namun hasilnya selalu "reads outside workspace" → ask+preview / headless `outside_workspace`, tidak pernah auto-approve. Dampaknya hanya hilangnya label (SEC10-15). Titik tunggal di akhir segmen dibuang oleh `GetFullPathName` (lewat `filepath.Abs`), lalu `EvalSymlinks`/`toNorm` (FindFirstFile) menormalkan nama. Simulasi Linux tidak dapat memodelkan ini [INFERENCE], sehingga perlu test CI Windows (`stripe-pay.\scripts\charge.py`).
- **Case folding darwin:** lowercase pada volume APFS case-sensitive menyebabkan over-gate (dir `Pay` dan `pay` sama-sama tergate), yang dapat diterima. Masalah sebenarnya ada di normalisasi Unicode (SEC10-16).
- **Prefix `/` setelah slash-clean di Windows:** benar. `Clean` + `ToSlash` menghasilkan `c:/…`, dan root drive `C:\` menjadi `c:` lalu prefix `c:/`. Root UNC `\\srv\sh\` menjadi `//srv/sh`. Kunci `/` (root) menjadi `""`, sehingga semua path tergate (over-gate, aman).
- **Sibling prefix:** tetap ditolak (`stripe-pay-v2`, Linux dan `Cmd`).

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC10-14 | **MEDIUM** | static + simulasi | **Windows: junction (mount point) tidak di-resolve.** Sejak Go 1.23 (`winsymlink=1`; `go.mod` 1.26.10), `filepath.EvalSymlinks` **tidak lagi mengevaluasi mount point** (`doc/godebug.md`), dan `GetLongPathName` juga tidak. Junction dapat dibuat tanpa hak admin (`mklink /J`, `New-Item -ItemType Junction`). Junction `C:\work\j` → dir skill pembayaran membuat `python C:\work\j\charge.py` berkunci `c:/work/j/charge.py`, sehingga perintah terbaca sebagai `mutating` di dalam workspace dan menjadi **auto-approve** bila `shell_allow` cocok atau di `--unsafe-auto-shell` (`QAQAQADAAA`, dibanding Monetary `P…D`). Ini padanan Windows dari SEC10-12 (symlink di workspace), yang di Linux/darwin sudah tertutup. `subst X:` adalah varian lain. | `monetaryKey` Windows: `CreateFile(path, 0, share all, OPEN_EXISTING, FILE_FLAG_BACKUP_SEMANTICS)` + `GetFinalPathNameByHandle(VOLUME_NAME_DOS)`. Fungsi ini me-resolve junction, symlink, `subst`, 8.3, dan case asli. Buang awalan `\\?\` (`\\?\UNC\` → `//`), lalu lowercase. Untuk path yang belum ada, resolve parent terpanjang yang ada lalu gabungkan sisanya. Tambahkan test CI Windows dengan `mklink /J` (tanpa admin). |
| SEC10-15 | MINOR | confirmed (simulasi `Cmd`) | Bentuk device/UNC (`\\?\C:\…`, `\\.\C:\…`, `\\localhost\C$\…`) diubah `classify.clean` menjadi `/`, `/C:/…`, `/localhost/C$/…` sebelum `MonetaryKey`. Label Monetary hilang. Keputusan tetap ask+preview (headless `outside_workspace`), jadi tidak ada auto-approve, tetapi preview tidak menandai pembayaran. | Di dialek `Cmd`/`Pwsh`: kenali dan lepaskan awalan `\\?\`/`\\.\`/`\??\`, perlakukan `\\host\share` sebagai volume, **atau** naikkan `Unknown` untuk bentuk-bentuk itu. Catatan: `exec/classify` 1798/1800, sehingga perbaikan mungkin butuh ruang `loc.budget`. |
| SEC10-16 | MINOR | static | darwin: `strings.ToLower` tanpa normalisasi Unicode. APFS tidak membedakan NFC/NFD, sedangkan `EvalSymlinks` mempertahankan byte input. Path dengan komponen non-ASCII (mis. `/Users/José/…`) yang ditulis dalam bentuk lain menghasilkan kunci berbeda. Untuk dir skill di luar workspace dampaknya hanya hilangnya label (tetap ask). Untuk skill proyek di dalam workspace, perintah bisa auto-approve dengan `shell_allow`. | darwin: kunci dari `fcntl(F_GETPATH)` pada fd yang dibuka (nama tersimpan kanonik), atau normalisasi NFD sebelum lowercase. |
| SEC10-17 | MINOR | confirmed | Fallback `MonetaryKey == nil` tidak terjangkau di produksi, tetapi kini lebih lemah: `resolveDirs` dihapus, sehingga gate berupa symlink + perintah via path nyata → `mutating` (probe). Shell baru (TUI/ACP/gateway) yang lupa menyetel `Canonicalize` akan fail-open untuk skill yang di-symlink. | `NewShell`: tolak config dengan `MonetaryPaths` non-kosong tanpa `Canonicalize` (fail-closed), atau hapus cabang leksikal. |
| SEC10-18 | MINOR | confirmed | Bila `EvalSymlinks` gagal (komponen akhir belum ada), kunci memakai path tanpa resolusi, sehingga path baru di bawah parent symlink ke skill tidak tergate. Risikonya rendah (file belum ada; tulis lewat link tetap ask). | Resolve parent terpanjang yang ada (berlaku juga untuk Windows di SEC10-14). |

**Ringkasan Round 5:** #212 `b6172bb` **REQUEST CHANGES**. Perbaikan CI-nya benar dan tidak fail-open di Linux/darwin. Kunci kanonik me-resolve symlink, `/private/var`, case, dan 8.3. Prefix `kd + "/"` menolak sibling. ADS tetap tergate. `Canonicalize` nil tidak terjangkau di produksi. Namun di Windows, junction (dapat dibuat tanpa admin) tidak di-resolve oleh `EvalSymlinks` Go ≥ 1.23, sehingga path workspace yang mengarah ke skill pembayaran dapat di-auto-approve (SEC10-14). Perbaikannya terlokalisasi di `pathkey_windows.go` (`GetFinalPathNameByHandle`). SEC10-15..18 MINOR. Hasil CI mac/win pada head ini tetap verifikasi utama untuk jalur darwin/Windows.
