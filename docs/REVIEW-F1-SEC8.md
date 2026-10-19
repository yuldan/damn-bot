# REVIEW-F1-SEC8 — Review keamanan loop stack (#99–#103)

**Status: lengkap.** Review independen pertama atas `internal/loop` (event, turn core, tool-call assembly/render, dispatch/approval/taint/spill). Reviewer tidak menulis kode yang direview. Tanggal: 2026-10-20.

Head yang direview (belum di-merge): `#99` `beae644`, `#100` `5821828`, `#101` `d641c08`, `#102` `d8e360a`, `#103` `9b21692`. Pemeriksaan di Linux. Probe berupa test sementara di worktree terpisah (tidak di-commit); output probe dikutip di bawah.

## Verdict per PR

| PR | Isi | Verdict |
|---|---|---|
| #99 | event vocabulary + consumer interfaces | **changes required** (LP8-8) |
| #100 | turn core, streaming, budgets, stop reasons | **changes required** (LP8-6, LP8-8, LP8-11/12) |
| #101 | tool-call assembly, render, dispatch primitives, context guard | **changes required** (LP8-6, LP8-7) |
| #102 | tool dispatch, approval, taint, spill | **changes required — 2 BLOCKER** (LP8-1, LP8-2; LP8-3/4/5) |
| #103 | docs komentar + F1 report | **approve dengan errata** (menunggu perbaikan #102) |

Ringkasan: **2 BLOCKER, 6 MAJOR, 5 MINOR.**

## Temuan

`confirmed` = dijalankan; `static` = dibuktikan dari kode.

| ID | PR | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|---|
| LP8-1 | #102 | **BLOCKER** | confirmed | Turn yang dibatalkan setelah tool selesai membuang `ToolResult` dan tetap melaporkan `cancelled_confirmed`. `deliver` melihat `ctx.Err()` lalu `return StopCancelledConfirmed` **sebelum** append tool message; `res` yang sudah selesai tidak masuk history/event. History berikutnya berisi assistant tool-call tanpa tool result (invalid), dan user diberi tahu “confirmed” padahal tidak ada konfirmasi. | `dispatch.go:337-344`; probe `TestZZCancellationDropsResult`: `stop=cancelled_confirmed/cancelled kinds=[user_message turn_start llm_call assistant_message approval tool_dispatch turn_end]` (tidak ada `tool_result`), padahal tool mengembalikan sukses |
| LP8-2 | #102 | **BLOCKER** | confirmed | Jalur read paralel tidak menghormati `KillGrace`. `dispatchBatch` menunggu `tasks[i].Wait()` tanpa select `ctx.Done()`/timer; tool read yang mengabaikan ctx membuat `Cancel`/`Prompt` menggantung. Ini jalur normal (dua read), bukan hanya escapee. | `dispatch.go:310-333`; probe `TestZZParallelCancelHang`: dua read memblokir; cancel; `HANG: parallel reads ignored KillGrace` (1,5 s > KillGrace 1 s) |
| LP8-3 | #102 | MAJOR | confirmed | `allow_session` untuk `Prepared` tanpa subject mencetak grant kosong. `subjects` mengembalikan `[""]`; `grant` menyimpan `tool+"\x00"`; `cached` mencocokkan subject kosong. Satu kali `AllowSession` menjadi izin luas untuk semua call berikutnya dengan subject kosong. | `dispatch.go:208-240`; probe `TestZZAllowSessionEmptySubject`: `approver calls=1 ran=2 (empty subject cached)` |
| LP8-4 | #102 | MAJOR | confirmed | Approval tidak mengisi `Prepared.Approved`; `approve` hanya mencatat/cache grant. Tool yang membutuhkan `Env.Approved` (outside_root/hardlinked) menerima `false` setelah `allow_once`, sehingga approval user tidak sampai ke tool. | `dispatch.go:172-207`; `tools.Env.Approved`; probe `TestZZApprovedNotSet`: `Approved seen by dispatch=false (allow_once)` |
| LP8-5 | #102 | MAJOR | confirmed | `no_progress` menghentikan turn saat `deliver` result **pertama** mencapai batas, tanpa mengirim result call lain dalam batch. Dua call yang sudah dieksekusi menghasilkan satu tool message; history tidak lengkap. | `dispatch.go:350-356`; probe `TestZZNoProgressDropsRemaining`: `tool_results=1 ran=1` untuk dua call |
| LP8-6 | #100/#101 | MAJOR | confirmed | Redaksi tidak diterapkan di loop: `modelContent` tidak memanggil redactor; `toolTurn`/`finalAnswer` menyimpan `r.Content` mentah; stream delta diteruskan mentah. Secret dari provider/assistant masuk Store/UI. | `render.go:44-58`; `turn.go:352-377`; probe `TestZZRedaction`: `stream delta unredacted`, `assistant stored="sk-secret" redacted=false` |
| LP8-7 | #101 | MAJOR | confirmed | Budget inline per-turn dapat dilewati. Saat `inline+n > MaxInlineTurn`, `trimmed` mengembalikan preview 400 char tetapi **tidak menambah** `t.inline`; panggilan berikutnya mendapat konten penuh. Total melebihi batas. | `render.go:44-58`, `trimmed` `:80-95`; probe `TestZZInlinePreviewBudget`: `total_content_len=232` dengan `MaxInlineTurn=20` |
| LP8-8 | #99/#100 | MAJOR | static | `event.StopDetail` adalah closed set tetapi tidak memuat `max_wall`/`max_iterations`; `turn.go` memakai string literal untuk keduanya. Frontend yang switch atas konstanta tidak bisa mengenali dua stop reason penting, dan kontrak “closed” tidak ditegakkan. | `event.go:94-107`; `turn.go:68`, `:152` |
| LP8-9 | #102 | MINOR | static | Duplikat `tool_call.id` dipetakan ke `invalid_args`, bukan `idempotency_conflict` yang disebut SPEC untuk konflik idempotensi. | `dispatch.go:31-90` |
| LP8-10 | #100 | MINOR | static | Retry `empty_response` tidak menambahkan usage attempt pertama; akuntansi budget bisa kurang. | `turn.go` jalur `empty_response`/`ask` |
| LP8-11 | #100 | MINOR | confirmed | `h.user.Store(true)` di `Cancel` tidak pernah dibaca; `cancelled_confirmed` tidak membedakan cancel user vs caller/shutdown. | `loop.go:153-160`; grep `h.user` hanya write |
| LP8-12 | #100 | MINOR | static | `MaxWall` diperiksa hanya di `preflight` sebelum iterasi; satu provider/tool call yang panjang dapat melewati batas wall tanpa timer. | `turn.go:147-153` |
| LP8-13 | #102 | MINOR | static | `deliver` menandai `cancelled_confirmed` untuk semua `res.Status != "still_running"`, termasuk result yang selesai sebelum cancel; lihat LP8-1. Tidak ada bukti “confirmed” dari tool. | `dispatch.go:337-343` |
| LP8-14 | #100 | MINOR | static | `note`/`append` bersifat best-effort; event audit dapat hilang saat Store error/cancel. Ini didokumentasikan, tetapi tidak ada counter/telemetry untuk kehilangan event. | `turn.go` `note`/`end` |
| LP8-15 | #102 | MINOR | static | `ToolResult` untuk call yang belum di-`deliver` setelah terminal (cancel/no_progress) tidak pernah dikirim; pasangan assistant tool-call tidak lengkap. Sama akar dengan LP8-1/5. | `dispatch.go:337-356` |

### LP8-1 (BLOCKER) — hasil tool selesai dibuang, status “confirmed” salah

Urutan di `deliver`:

```go
if ctx.Err() != nil {
    if res.Status == "still_running" {
        return t.cancelled(), true
    }
    return outcome{stop: event.StopCancelledConfirmed, detail: event.DetailCancelled}, true
}
```

Tidak ada `t.append`/`t.history` sebelum return. Bila tool mengembalikan `Status:"exited"`/sukses tepat sebelum cancel, hasilnya dibuang. `cancelled_confirmed` seharusnya hanya bila tool melaporkan berhenti di dalam kill grace; di sini justru tool **selesai**. Dampak: transcript invalid (assistant tool_call tanpa tool result) dan audit kehilangan side effect yang sudah terjadi.

**Rekomendasi:** append/emit `ToolResult` lebih dulu (setidaknya untuk result yang sudah ada), lalu klasifikasikan cancel. `cancelled_confirmed` hanya bila status tool benar-benar `still_running`/`cancelled` terkonfirmasi; jika result sukses, simpan dan gunakan `cancelled` biasa. Tambah test yang mengembalikan result sukses setelah cancel dan assert `tool_result` ada + stop bukan confirmed.

### LP8-2 (BLOCKER) — read paralel mengabaikan KillGrace

`dispatchBatch` membuat `boundary.Go` per read lalu `tasks[i].Wait()` tanpa select. `boundary.Task.Wait` menunggu goroutine; `ctx.Done` tidak membatalkan `Wait`. Tool read yang tidak menghormati ctx (mis. syscall lambat) membuat `Cancel` menggantung. `runParallel` hanya memeriksa cancel setelah semua `Wait` selesai.

**Rekomendasi:** gunakan pola `dispatch` per call (`select` hasil vs `ctx.Done` + timer `KillGrace`), atau `Task.Wait` dengan context/timer; hasil yang belum selesai ditandai `still_running` dan turn selesai. Tambah test dua read yang memblokir + cancel; assert `Prompt` kembali dalam KillGrace.

### LP8-3 (MAJOR) — subject kosong menjadi grant global

`subjects(p)` mengembalikan `[]string{p.Subject}` bahkan bila `Subject==""`; `grant` menyimpan `tool+"\x00"`; `cached` mencocokkan subject kosong. Prepared tanpa subject (mis. tool tanpa path) mendapat satu grant untuk semua call tanpa subject. **Rekomendasi:** bila `Subjects` kosong dan `Subject==""`, jangan cache `AllowSession`; turunkan ke `AllowOnce` atau minta adapter mengisi subject. Tambah test di layer loop.

### LP8-4 (MAJOR) — approval tidak diteruskan

`approve` setelah `DecisionAllowOnce` hanya `recordApproval`; tidak ada `p.Approved = true` atau update `p.Env.Approved`. Tool yang membutuhkan flag ini menolak/mengabaikan approval. **Rekomendasi:** set `p.Approved` (dan Env yang dipakai Dispatch) untuk `AllowOnce`/`AllowSession`/`Edit` yang disetujui; tambah test dengan tool yang membaca `Env.Approved`.

### LP8-5 (MAJOR) — no_progress membuang sisa batch

`deliver` mengembalikan terminal pada result pertama yang mencapai `MaxIdentical`; `runSerial`/`runParallel` berhenti, result lain yang sudah dieksekusi tidak di-deliver. **Rekomendasi:** tandai terminal tetapi tetap deliver semua result yang sudah ada (atau jangan mulai call berikutnya); pastikan history punya satu tool message per tool_call yang dieksekusi.

### LP8-6 (MAJOR) — redaksi tidak ada di loop

`modelContent` mengembalikan `res.Content`/`errorEnvelope` tanpa Redactor; `toolTurn`/`finalAnswer` menyimpan `r.Content`; stream delta dari provider diteruskan. Tools layer meredaksi tool output, tetapi assistant/stream/tool result bisa membawa secret. **Rekomendasi:** semua konten yang masuk Store/UI (assistant, stream, modelContent, error) harus melalui redactor turn; test `sk-...` di assistant/stream/tool.

### LP8-7 (MAJOR) — budget inline bocor

`modelContent` memanggil `trimmed(ctx, content, 0, 400, false)` ketika budget turn terlampaui; `trimmed` mengembalikan preview + `[truncated: turn inline budget]` tanpa `t.inline += n`. Setiap call berikutnya melihat `t.inline` masih di bawah batas dan mengirim konten penuh. **Rekomendasi:** tambahkan `t.inline += len(preview)` (atau set `t.inline = MaxInlineTurn`) saat memotong; setelah budget habis, semua call harus spill/preview pendek. Tambah test dua call.

### LP8-8 (MAJOR) — StopDetail tidak tertutup

`event.StopDetail` berisi 14 nilai, tetapi `turn.go` memakai `"max_wall"` dan `"max_iterations"` yang tidak ada di set. Frontend yang memakai konstanta tidak dapat menampilkan/menangani dua stop reason; perubahan string tidak terdeteksi compiler. **Rekomendasi:** tambahkan `DetailMaxWall`/`DetailMaxIterations` (atau definisikan detail taksonomi lengkap), gunakan konstanta di `turn.go`, dan test switch exhaustive.

## Diperiksa dan bersih

- **Approval sekali per iterasi:** `prepareBatch` → `approve` per prepared; tidak ada jalur kedua di `dispatch`/`deliver`; tool call yang di-deny terminal/skip; `DecisionEdit` re-prepare sekali.
- **Taint:** `deliver` menyetel `t.tainted` dari `res.Tainted`; `NeedsApproval` memakai taint; `canParallel`/`cached` mematikan session untuk non-read saat tainted; `taintGate` di tools.
- **Tool error kritis:** error tool non-cancel menjadi `tool_error` terminal; `CodeDenied` menjadi tool message (model dapat memperbaiki); tidak ada tool yang dijalankan saat prepare error.
- **Stop normal:** `finish_assistant`/`completed`/`empty_response`/`max_iterations`/`max_tokens`/`context_overflow`/`content_filtered`/`output_truncated`/`provider_error`/`tool_error`/`approval_denied` punya baris; `max_iterations` mengembalikan detail (LP8-8 hanya konstanta).
- **Context guard:** `guard` compaction sekali (`t.compacted`), hard stop `OverflowAt`, `MaxInputTokens` pre-call; retry `context_length` tidak loop tanpa batas.
- **Spill nil:** `spill` mengembalikan error; `trimmed` fallback ke `too_large`/`[truncated]`; tidak ada path yang mengirim seluruh konten saat Spiller nil.
- **Finish length:** call dari `finish_reason=length` tidak dieksekusi; `invalid_args` dikirim ke model.
- **Tool result pairing (non-cancel/non-progress):** satu `tool` message per call; `ToolCallID` dipertahankan; tidak ada duplicate pada jalur normal.
- **Tidak ada approval yang dilewati** untuk call yang `NeedsApproval` di jalur serial/paralel (kecuali bug subject kosong LP8-3).

## Tidak dicakup

- Integrasi end-to-end dengan adapter produksi/approval UI (belum ada di stack ini).
- Windows/macOS (tidak ada loop OS-specific).
- Fuzz, `-race` seluruh paket, stress konkurensi Store, dan benchmark.
- Perbaikan setelah head di atas; verdict berlaku untuk head tersebut.
- Kebijakan `MaxIterations`/`MaxWall` di config dan pengalaman UI.
