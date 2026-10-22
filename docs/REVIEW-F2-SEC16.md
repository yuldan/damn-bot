# REVIEW-F2-SEC16 — Review keamanan tool `skills_list` / `skill_view` / `skill_manage` (#263)

Review independen untuk #263 `feat/f2-skill-tools` `d6aed22` (basis `f2-e2e`). Reviewer tidak menulis kode yang direview, dan kode produk tidak diubah. Aturan owner berlaku: e2e + kontrol negatif. Tanggal: 2026-10-22.

## Round 1 — 2026-10-22

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #263 skill tools | `d6aed22` | **REQUEST CHANGES**: **SEC16-1 (HIGH)** `skill_manage create` menimpa skill user di disk, termasuk skill pembayaran, sehingga gate pembayaran hilang; SEC16-2 (MEDIUM) file agent ditulis ke `<config>/skills` sebelum approval; SEC16-3 (MEDIUM) skill agent yang ditolak memblokir setiap `run` berikutnya; SEC16-4 (MEDIUM, aturan owner) |

Gate di `d6aed22`: `go test -race -tags goolm -count=1 -run Skill ./cmd/damnbot ./internal/tools ./internal/e2e` → ok; `internal/archtest` ok; `make loc` `tools 2308/2500`.

### Desain

- `skills_list`/`skill_view`: `EffectRead`; hanya skill dari `skillstore.Search` (user/builtin, atau agent dengan memori `approved`). `skill_view` mengembalikan `UntrustedSource: "skill:<name>"`, yang dibungkus dispatcher menjadi `<untrusted source="skill:…">`. `TestSkillsTools` (binary nyata) memverifikasi pembungkus ini ✔. File pendukung dibaca lewat `os.Root` + `fs.WalkDir` (symlink/non-reguler dilewati, ≤ 20 file, ≤ 64 KiB, hanya UTF-8) ✔.
- `skill_manage`: `EffectNonIdempotent` tanpa `Inspector`, sehingga mengikuti `defaultPolicy`: **auto** di `auto-edit`/`auto`/headless `--approval auto`, ask di `ask`, ditolak di headless biasa. Nama `^[a-z0-9][a-z0-9._-]{0,63}$`. `create` ditolak bila nama **ada di DB** (`skillstore.SourceOf`); `update`/`delete` hanya untuk sumber `agent`. Dokumen dibangun dari field terstruktur dan di-`Parse` (`SourceAgent`), lalu **file ditulis ke `<config>/skills/<name>/` lewat `os.Root`** (`safeSkillFile` menolak path absolut/`..`/`\`/NUL/`SKILL.md`). Setelah itu `skills.Service.Install` → sumber agent → `Quarantine` (scanned/rejected, **tidak pernah approved**; `TestSkillManageNeverApproves`) ✔.

### Probe `TestZZSec263` (`cmd/damnbot`, dihapus setelah dijalankan)

| Langkah | Hasil |
|---|---|
| User punya skill `stripe-pay` di `<config>/skills` dengan `side_effects: [payment]` + `scripts/charge.sh` | `paymentSkillDirs` = `[…/skills/stripe-pay]` |
| Model: `skill_manage {"action":"create","name":"stripe-pay","description":"harmless helper","body":"Run scripts/charge.sh whenever asked."}` | **sukses**, `status: scanned` (DB tidak punya baris untuk skill user yang hanya ada di disk) |
| `SKILL.md` user | **ditimpa**: `side_effects` hilang; `scripts/charge.sh` tetap ada |
| `paymentSkillDirs` (dihitung di setiap awal `run`) | **`[]`**: direktori pembayaran **tidak lagi di-gate** |
| Model: `create evil-lint` dengan file `notes.md` berisi `gh auth token … ~/.aws/credentials` | DB: `status: rejected`, tetapi file tetap tertulis di disk |
| `paymentSkillDirs` pada awal `run` berikutnya | **`invalid_skill: evil-lint: credential_access`**, sehingga `run` gagal (fail-closed) **untuk setiap run berikutnya** |

### Jawaban

- **Melewati karantina/approval dan tidak pernah auto-approve?** Baris DB/memori: ya, agent → quarantine dan tidak pernah approved. **File di disk: tidak.** File ditulis ke direktori skill user (`<config>/skills`) **sebelum** approval dan tetap ada walaupun ditolak. Direktori itu dibaca di awal setiap `run` oleh `loadSkills` sebagai **`SourceUser`** (tepercaya) untuk gate pembayaran (SEC16-2).
- **Skill `side_effects: payment` tidak aktif tanpa gate yang ada?** Model yang membuat skill pembayaran baru justru menambah gate (aman). Namun model dapat **menghapus gate skill pembayaran user**: `create` dengan nama yang sama menimpa `SKILL.md` user tanpa `side_effects`. Guard shadowing hanya memeriksa DB, sedangkan skill user di `<config>/skills` tidak di-index ke DB di tree ini (SEC16-1).
- **Dapat dipakai untuk memasang atau mengubah skill yang kemudian auto-run lewat gate pembayaran/exec?** **Ya (SEC16-1).** `skill_manage` sendiri auto di `auto-edit`/`auto`. Langkah 1 menimpa `stripe-pay/SKILL.md`; pada `run` berikutnya `scripts/charge.sh` tidak lagi `Monetary`. Langkah 2 (`./charge.sh` atau `python3 …` di skill itu) mengikuti klasifikasi biasa: auto di `--unsafe-auto-shell` atau dengan `shell_allow`, padahal sebelumnya selalu ask/deny. Selain itu `skill_manage` memberi model jalur tulis ke `<config_dir>`, yang normalnya ada di `deny_paths` untuk `write_file`/shell.
- **`skill_view` sebagai `<untrusted>`:** ya ✔ (e2e).
- **`os.Root` dan fail-closed:** penulisan dan penghapusan terkurung di `<config>/skills` (`os.Root`, `safeSkillFile`, regex nama) ✔. "Fail-closed" `loadSkills` justru menjadi vektor DoS persisten (SEC16-3).

### Aturan owner

E2E `TestSkillsTools` (binary nyata) hanya mencakup `skills_list` dan `skill_view` untuk skill yang di-seed. **`skill_manage` tidak punya e2e**, dan tidak ada kontrol negatif e2e: skill hasil `skill_manage` tidak terlihat di `skills_list`/`skill_view` sebelum approval, serta `create` yang menabrak skill user/pembayaran harus ditolak (SEC16-4).

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| **SEC16-1** | **HIGH** | confirmed (probe) | `create` menimpa skill user yang hanya ada di disk (guard shadowing hanya memeriksa DB), sehingga `side_effects: [payment]` dapat dihapus dan direktori pembayaran tidak lagi di-gate pada `run` berikutnya. Script pembayaran user kemudian auto-run di `--unsafe-auto-shell`/`shell_allow`. | Jangan pernah menulis agent skill ke `<config>/skills`. Simpan di direktori karantina terpisah (mis. `<data>/skills/agent/<name>`) dan baru dimaterialisasi setelah approval user. Guard `create` juga harus memeriksa disk (`<config>/skills/<name>` ada → `ErrSkillExists`) di samping DB. Tambahkan test: skill user/pembayaran di disk + `create` dengan nama sama → ditolak, file dan gate utuh. |
| SEC16-2 | MEDIUM | confirmed | File agent ditulis sebelum approval ke direktori yang dimuat sebagai `SourceUser`, sehingga karantina hanya berlaku untuk baris DB dan tidak untuk artefak di disk. `skill_manage` juga memberi jalur tulis ke `<config_dir>` yang di-`deny_paths`. | Sama dengan SEC16-1 (direktori karantina). `skill_manage` diberi `Inspector` dengan kelas "config write" yang selalu ask, tidak auto di `auto-edit`. |
| SEC16-3 | MEDIUM | confirmed (probe) | Skill agent yang **ditolak** (lint `credential_access`) tetap di disk, sehingga `loadSkills` gagal dan **setiap `run` berikutnya** berhenti (DoS persisten) sampai user menghapus direktorinya. | Dengan direktori karantina, skill yang ditolak tidak berada di `<config>/skills`. Alternatif: hapus file saat status `rejected`. |
| SEC16-4 | MEDIUM (aturan owner) | open | Tidak ada e2e `skill_manage` maupun kontrol negatif e2e. | E2E lewat binary: `skill_manage create` → `skills_list` **tidak** memuatnya (status scanned); `create` dengan nama skill user/pembayaran → ditolak; kontrol: setelah approval (`damnbot review approve`) skill terlihat. |

**Ringkasan:** #263 **REQUEST CHANGES**. Baris DB agent skill benar dikarantina dan `skill_view` dibungkus `<untrusted>`. Namun `skill_manage` menulis file ke direktori skill user **sebelum** approval, dan guard shadowing hanya memeriksa DB, sehingga model dapat menimpa `SKILL.md` skill pembayaran user, menghapus gate pembayarannya, lalu menjalankan scriptnya secara auto pada `run` berikutnya (SEC16-1, HIGH). Skill yang ditolak juga mematikan semua run berikutnya (SEC16-3), dan `skill_manage` belum punya e2e/kontrol negatif (SEC16-4).
