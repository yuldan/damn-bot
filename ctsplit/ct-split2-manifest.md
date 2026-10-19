# F1 codetools stacked split — manifest

Base: origin/main @339d92a (base of reference f1-codetools-1 @b2fec95). Final tree: `git diff b2fec95 f1-ct-13` empty. Builder: /tmp/ctsplit (stages.txt, ov/, st/, mk.sh, build.sh, check.sh).

| Stage | Branch | SHA | pr-meta size | test -race | deadcode | coverage codetools | lint | loc codetools | vet l/d/w | tidy |
|---|---|---|---:|---|---|---:|---|---:|---|---|
| 1 | f1-ct-01 | a844f38 | 458 | ok | ok | n/a | ok | n/a | ok/ok/ok | ok |
| 2 | f1-ct-02 | 53862da | 588 | ok | ok | 87.2% | ok | codetools 459 / 4500 | ok/ok/ok | ok |
| 3 | f1-ct-03 | 84b14fc | 499 | ok | ok | 86.1% | ok | codetools 851 / 4500 | ok/ok/ok | ok |
| 4 | f1-ct-04 | 95abd51 | 529 | ok | ok | 85.9% | ok | codetools 1297 / 4500 | ok/ok/ok | ok |
| 5 | f1-ct-05 | bc522cf | 556 | ok | ok | 86.9% | ok | codetools 1753 / 4500 | ok/ok/ok | ok |
| 6 | f1-ct-06 | b682a47 | 597 | ok | ok | 86.3% | ok | codetools 2266 / 4500 | ok/ok/ok | ok |
| 7 | f1-ct-07 | 172e603 | 431 | ok | ok | 84.7% | ok | codetools 2628 / 4500 | ok/ok/ok | ok |
| 8 | f1-ct-08 | eb4e5e0 | 590 | ok | ok | 87.6% | ok | codetools 3156 / 4500 | ok/ok/ok | ok |
| 9 | f1-ct-09 | b20bacc | 538 | ok | ok | 85.9% | ok | codetools 3568 / 4500 | ok/ok/ok | ok |
| 10 | f1-ct-10 | b08d7c8 | 461 | ok | ok | 83.3% | ok | codetools 3920 / 4500 | ok/ok/ok | ok |
| 11 | f1-ct-11 | 2476388 | 598 | ok | ok | 84.6% | ok | codetools 4437 / 4500 | ok/ok/ok | ok |
| 12 | f1-ct-12 | 83898bb | 371 | ok | ok | 88.3% | ok | codetools 4728 / 4800 | ok/ok/ok | ok |
| 13 | f1-ct-13 | 021e032 | 454 | ok | ok | 88.3% | ok | codetools 4786 / 4800 | ok/ok/ok | ok |

## f1-ct-01 (a844f38)

PR base: main

**Title:** `fix(sandbox): walker opens without symlinks, refuse FIFOs/devices`

**Body:**

```text
Stage 1/13 of the F1 codetools stack (base: main). Sandbox API used by the
codetools walker and ignore loader, plus the REVIEW-F1-SEC2 sandbox fixes.

- `Sandbox.Denied`: lexical deny_paths match for walk entries (no syscall,
  literal-component and `*suffix` fast path, same results as `path.Match`).
- `Sandbox.OpenResolved`: read-only open with no symlink anywhere on the
  path (one `openat2(RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS)` on Linux,
  per-component `O_NOFOLLOW` elsewhere) + post-open deny/identity/nlink
  re-check, for both the inside and the approved branch.
- `ErrNotRegular`: FIFOs/devices/sockets are refused before open (lstat in
  `Check`) and after open (fstat on the handle); the last component opens
  `O_NONBLOCK` (cleared for regular files), so no open blocks past ctx.
- `vet` merges recheckDeny/recheckIdentity/refuseLinked into one post-open
  check shared by OpenFile/OpenApproved/OpenResolved.

ADR-103 (butir 2, 15) is added to docs/DECISIONS.md with this stage.

REVIEW-F1-SEC2 closed here: CT27-1 (approved OpenResolved followed
ancestor symlinks), CT27-4 (sandbox part: blocking opens of FIFOs),
CT27-6 (sandbox part: OpenResolved/Denied untested: swap race in both
modes, hard links, Denied table), CT27-12 (deny fast path now exists),
CT27-13 (OpenResolved checks nlink).
```

**Gates:** prmeta: ok size: 458;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/sandbox );deadcode: ok;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;;tidy: ok;tidy-bench: ok;

## f1-ct-02 (53862da)

PR base: f1-ct-01

**Title:** `feat(codetools): engine core and read_file for UTF-8 text`

**Body:**

```text
Stage 2/13 of the F1 codetools stack (base: f1-ct-01). New package
`internal/codetools` (CODE-TOOLS §4, ADR-103):

- `Engine` (`New`/`Close`), `Config`, `Opts` (page caps of ADR-031:
  16.000 / 4.000 chars), `Result`, `*ToolError` with SPEC §4.3 codes.
- Every tool path opens through `internal/sandbox` (`OpenFile`, and
  `OpenApproved` only after an approved non-Inside verdict); sandbox and
  OS failures map to codes by sentinel (`mapOpenErr`); content read
  outside the workspace is wrapped as untrusted.
- `read_file` for text: default 400 / max 2.000 lines, negative offsets
  from the end, lines cut at 2.000 chars, invalid UTF-8 escaped
  injectively (`\xNN`, never U+FFFD), page cut at a whole line, file tag
  = sha256 prefix recorded per session (CODE-TOOLS §5.1).
- CODEOWNERS entry for the new package.

Not yet in this stage (later stages of the stack): encodings/BOM/UTF-16,
binary sniff, read cache, large files, outline, grep/glob, edits.

ADR-103 butir 10 added. Tests: TestReadLimits (CT-32), TestPageCap.
```

**Gates:** prmeta: ok size: 588;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/codetools );deadcode: ok;coverage: rc=0 ok   github.com/damnbot/damnbot/internal/codetools 0.007s coverage: 87.2% of statements in ./internal/codetools/... COVERAGE ok    github.com/damnbot/damnbot/internal/codetools: 87.2% (min 80%) ;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;LOC ok codetools 459 / 4500;;tidy: ok;tidy-bench: ok;

## f1-ct-03 (84b14fc)

PR base: f1-ct-02

**Title:** `feat(codetools): read_file encodings, binary sniff and read cache`

**Body:**

```text
Stage 3/13 of the F1 codetools stack (base: f1-ct-02). Completes the
small-file read path of read_file (CODE-TOOLS §4.2–§4.4, §7; ADR-103):

- Encoding detection on an 8 KiB head (§4.2): UTF-8 with/without BOM,
  UTF-16 LE/BE with/without BOM decoded to WTF-8 (odd trailing byte shown
  as invalid); header shows `utf-8 bom crlf`-style labels and EOL
  (lf/crlf/mixed). Files above 8 MiB that are not UTF-8 get a stat tag
  plus the full hash (§5.1).
- Binary sniff (CT-34): NUL outside UTF-16, > 10 % invalid bytes, or one
  of 21 magic signatures; a binary read is one descriptive line with a
  tag, not an error (§4.4).
- Bounded caches (§7, CT-35): byte-budgeted LRU for small-file bytes and
  stale_read snapshots, validated against a fresh stat and the racy
  window; every cache except tags is dropped after 10 minutes idle.

ADR-103 butir 6 added. Tests: TestReadEncodings (full), TestBinarySniff.
```

**Gates:** prmeta: ok size: 499;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/codetools );deadcode: ok;coverage: rc=0 ok   github.com/damnbot/damnbot/internal/codetools 0.019s coverage: 86.1% of statements in ./internal/codetools/... COVERAGE ok    github.com/damnbot/damnbot/internal/codetools: 86.1% (min 80%) ;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;LOC ok codetools 851 / 4500;;tidy: ok;tidy-bench: ok;

## f1-ct-04 (95abd51)

PR base: f1-ct-03

**Title:** `feat(codetools): read_file large files, async line index, outline`

**Body:**

```text
Stage 4/13 of the F1 codetools stack (base: f1-ct-03). read_file above
8 MiB and outline mode (CODE-TOOLS §4, §7; ADR-103 butir 1, 5):

- Files > 8 MiB are never loaded whole: line-offset checkpoints every
  1.000 lines (index LRU, 4 MB cap); a stat tag (§5.1) with the full hash
  computed by the index pass. Negative offsets on a cold file read
  backwards from EOF in 64 KiB chunks (`L-N..-1/?`,
  `numbering=from-end`) while the index is built on an Engine-owned
  background task.
- Background work runs via `internal/boundary` (E-26) and stops on
  `Engine.Close`. New import edge `codetools → boundary`: ENGINEERING
  §2.2.1 graph + `archtest/allowed.go` (ADR-103 butir 1).
- `outline=true`: Go via `go/ast` (types, funcs, methods with receiver,
  consts/vars), other languages via a declaration regex heuristic;
  outlines cached per file version (outline LRU, 4 MB).

Docs: docs/ENGINEERING.md (edge `CT --> BND`), docs/DECISIONS.md
(ADR-103 butir 1 and 5). Tests: TestReadLarge, TestOutline,
TestCodeToolsMemCap (CT-35 caps).
```

**Gates:** prmeta: ok size: 529;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/archtest ./internal/codetools );deadcode: ok;coverage: rc=0 ok   github.com/damnbot/damnbot/internal/codetools 0.252s coverage: 85.9% of statements in ./internal/codetools/... COVERAGE ok    github.com/damnbot/damnbot/internal/codetools: 85.9% (min 80%) ;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;LOC ok codetools 1297 / 4500;;tidy: ok;tidy-bench: ok;

## f1-ct-05 (bc522cf)

PR base: f1-ct-04

**Title:** `feat(codetools): gitignore engine with git wildmatch semantics`

**Body:**

```text
Stage 5/13 of the F1 codetools stack (base: f1-ct-04). The ignore engine
used by the grep/glob walker (CODE-TOOLS §2.3; ADR-103 butir 4):

- `wildmatch`: port of git's wildmatch (`**`, character classes incl.
  POSIX `[:alpha:]`, escapes, `WM_PATHNAME`), pinned by TestWildmatch.
- Ignore stack per directory: `.gitignore`, `.ignore`, `.git/info/exclude`
  of the repo containing the search root, negation, dir-only, anchored
  patterns, trailing-space rules; `core.excludesFile` and `include.path`
  from the repo and global git config (`$XDG_CONFIG_HOME/git`, `~`).
- CT27-3/CT27-4: files above the session root are never read; in-tree
  ignore files and the repo `.git/config` are read only via
  `OpenResolved(approved=false)` when Inside and regular (symlinks,
  FIFOs, devices = absent, as git ≥ 2.32). Only the global git config,
  its includes and its excludes may be read outside the root.
- `Config.Home` / `Config.XDGConfigHome` locate the global git config.

Docs: docs/DECISIONS.md (ADR-103 butir 4). REVIEW-F1-SEC2 closed here:
CT27-3 (ignore loading read files outside the root), CT27-4 (ignore
part: FIFO `.gitignore`). Tests: TestWildmatch, TestGitignoreConformance
(oracle `git check-ignore`, CT-23), TestGitConfigExcludes.
```

**Gates:** prmeta: ok size: 556;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/codetools );deadcode: ok;coverage: rc=0 ok   github.com/damnbot/damnbot/internal/codetools 0.403s coverage: 86.9% of statements in ./internal/codetools/... COVERAGE ok    github.com/damnbot/damnbot/internal/codetools: 86.9% (min 80%) ;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;LOC ok codetools 1753 / 4500;;tidy: ok;tidy-bench: ok;

## f1-ct-06 (b682a47)

PR base: f1-ct-05

**Title:** `feat(codetools): glob over a parallel ordered ignore-aware walker`

**Body:**

```text
Stage 6/13 of the F1 codetools stack (base: f1-ct-05). The `glob` tool and
the walker shared with grep (CODE-TOOLS §2.2, §3; ADR-103 butir 3):

- Parallel walker on `internal/boundary` tasks: DFS with byte-sorted
  names per directory, emitted in order through a reorder window of
  64 × GOMAXPROCS, so output is deterministic; skipped-entry counters are
  filled by the emitter in order (deterministic on early exit).
- Directories are listed after `sandbox.Check`, entries matched against
  deny_paths with `Sandbox.Denied` (no deny-listed names leak), symlinks
  not followed, non-regular entries skipped; ignore stack per directory.
- `glob`: doublestar patterns (basename match without `/`), literal
  prefix pruning, sort `path` | `mtime` (newest first), offset/limit,
  page cap. New direct dependency `github.com/bmatcuk/doublestar/v4`
  (already in deps.allow).

Docs: docs/DECISIONS.md (ADR-103 butir 3). REVIEW-F1-SEC2: CT27-6 (walker
part: glob must not list deny-listed names, TestGlobDenied). Tests:
TestGlobLimits (CT-31), TestGlobDenied.
```

**Gates:** prmeta: ok size: 597;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/codetools );deadcode: ok;coverage: rc=0 ok   github.com/damnbot/damnbot/internal/codetools 0.483s coverage: 86.3% of statements in ./internal/codetools/... COVERAGE ok    github.com/damnbot/damnbot/internal/codetools: 86.3% (min 80%) ;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;LOC ok codetools 2266 / 4500;;tidy: ok;tidy-bench: ok;

## f1-ct-07 (172e603)

PR base: f1-ct-06

**Title:** `feat(codetools): grep matcher with literal fast path and RE2`

**Body:**

```text
Stage 7/13 of the F1 codetools stack (base: f1-ct-06). The line matcher
used by grep (CODE-TOOLS §2.1, §2.4; ADR-103), standalone and unit-tested
before the grep tool lands in the next stage:

- Pure literal patterns (case-sensitive or ASCII case-folded) use a
  rare-byte `IndexByte` scan; other patterns compile to RE2 (`regexp`),
  smart-case by default.
- Required-literal prefilter extracted from the RE2 syntax tree
  (concatenations, bounded alternations ≤ 8, products) so most lines that
  cannot match are skipped without running the regexp (CT-01/CT-02).
- Line semantics match `rg`: `$` matches before `\r\n`, no phantom empty
  line after a final newline; multiline patterns scan the whole buffer.

Tests: TestMatcherLineEnds, TestRequiredLiterals.
```

**Gates:** prmeta: ok size: 431;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/codetools );deadcode: ok;coverage: rc=0 ok   github.com/damnbot/damnbot/internal/codetools 0.409s coverage: 84.7% of statements in ./internal/codetools/... COVERAGE ok    github.com/damnbot/damnbot/internal/codetools: 84.7% (min 80%) ;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;LOC ok codetools 2628 / 4500;;tidy: ok;tidy-bench: ok;

## f1-ct-08 (eb4e5e0)

PR base: f1-ct-07

**Title:** `feat(codetools): grep tool with matches/files/count modes and context`

**Body:**

```text
Stage 8/13 of the F1 codetools stack (base: f1-ct-07). The `grep` tool
(CODE-TOOLS §2; ADR-103 butir 3) on the walker (stage 6), ignore engine
(stage 5) and matcher (stage 7):

- Arguments: pattern, path, glob (doublestar), type (language table),
  case smart|sensitive|insensitive, context 0..5, mode
  matches|files|count, offset/limit (≤ 500); 30 s budget.
- Files ≤ 8 MiB are scanned in pooled scratch buffers (binary skipped
  via the sniff of stage 3); larger files stream in chunks with context
  recovered by a second pass. Each file block carries its tag so
  edit_file can follow grep without a read.
- Output (CODE-TOOLS §9.1): per-file blocks, `N:` match / `N-` context
  rows, matched lines windowed to 500 chars around the match, page cap,
  `next: offset=…`, deterministic `skipped:` footer (binary, denied,
  unreadable, hard-linked).
- Hard-linked files and approved walks outside the root go through
  `OpenResolved` with identity checks (CT27-1, CT27-13).

REVIEW-F1-SEC2: CT27-6 (walker part: TestGrepApprovedWalkRace,
TestGrepSkipsHardLinked). Tests: TestGrepLimits, TestGrepOutput,
TestGrepIgnoreRules, TestCodeToolsDeterministic, TestHardLinkApproval,
TestBinarySniffCorpus, TestIgnoreSymlinkOutsideRoot,
TestRepoExcludesFileOutsideRoot.
```

**Gates:** prmeta: ok size: 590;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/codetools );deadcode: ok;coverage: rc=0 ok   github.com/damnbot/damnbot/internal/codetools 1.937s coverage: 87.6% of statements in ./internal/codetools/... COVERAGE ok    github.com/damnbot/damnbot/internal/codetools: 87.6% (min 80%) ;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;LOC ok codetools 3156 / 4500;;tidy: ok;tidy-bench: ok;

## f1-ct-09 (b20bacc)

PR base: f1-ct-08

**Title:** `feat(codetools): write_file creates files via temp+fsync+rename`

**Body:**

```text
Stage 9/13 of the F1 codetools stack (base: f1-ct-08). The atomic commit
path and `write_file` for new files (CODE-TOOLS §5.4, §5.6; ADR-103 butir
8, 16):

- Commit: temp `.<name>.damnbot-<rand>` created next to the target
  through the sandbox (`O_EXCL`), content hashed while written, `fsync`,
  re-stat (`stale_read` if the target changed or appeared), rename
  (`MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH)` on Windows), `fsync` of
  the directory opened through the sandbox, then a read-back sha256
  check (`write_verify_failed`, TOOL-03). Disk full → `disk_full`; the
  temp is discarded on every failure.
- `write_file` creates a file: parents created per component after
  `sandbox.Check` (approval for non-Inside verdicts), mode from the umask
  (temp 0666), UTF-8 without BOM, `files.new_eol` (`Config.NewEOL`),
  1.000.000-char cap, redaction sentinel refused. An existing path is
  `stale_read` here; overwriting with a tag is the next stage.

Docs: docs/DECISIONS.md (ADR-103 butir 8, 16 — residual path-based
rename/mkdir race, CT27-11 accepted). Tests: TestWriteReadBack (create
part, new_eol crlf, deny, read-back mismatch), TestCodeToolsSymlinkOutsideRoot.
```

**Gates:** prmeta: ok size: 538;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/codetools );deadcode: ok;coverage: rc=0 ok   github.com/damnbot/damnbot/internal/codetools 1.845s coverage: 85.9% of statements in ./internal/codetools/... COVERAGE ok    github.com/damnbot/damnbot/internal/codetools: 85.9% (min 80%) ;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;LOC ok codetools 3568 / 4500;;tidy: ok;tidy-bench: ok;

## f1-ct-10 (b08d7c8)

PR base: f1-ct-09

**Title:** `feat(codetools): write_file overwrite, file locks, owner/xattr keep`

**Body:**

```text
Stage 10/13 of the F1 codetools stack (base: f1-ct-09). Overwrite with a
tag and the safety pieces of the write path (CODE-TOOLS §5.1, §5.4, §5.5;
ADR-103 butir 9, 12, 14):

- `write_file` overwrites with the tag from read_file/grep: unknown or
  outdated tag → `stale_read` with the fresh tag; racy stat-tags need the
  recorded full hash (§5.5); encoding, BOM and dominant EOL preserved
  (UTF-16 re-encoded from WTF-8, unrepresentable content fails closed).
- Per-file edit lock keyed by the resolved path, case-folded on Windows
  and macOS, and by device+inode for hard-linked files (CT27-5).
- Owner and xattrs copied to the temp before the mode is set; setuid /
  setgid / sticky kept and `mode_preserved` verified (CT27-9); Windows
  reports `owner_preserved=false` for rename writes (CT27-12).
- `Engine.CleanOrphanTemps`: removes only `^\..+\.damnbot-[a-z2-7]{12}$`,
  unlinked relative to a directory fd opened through the sandbox
  (CT27-10).
- CT-25 kill points (`mid-temp`, `pre-rename`, `post-rename`) via a test
  hook; TestAtomicWriteKill9 parks a child at each point and SIGKILLs it.

REVIEW-F1-SEC2 closed here: CT27-5, CT27-9, CT27-10, CT27-12 (owner on
Windows), CT27-6 (kill-test part). Docs: docs/DECISIONS.md (ADR-103
butir 9, 12, 14). Tests: TestWriteReadBack (full), TestPathLockAliases,
TestCleanOrphanTempsPattern, TestAtomicWriteKill9.
```

**Gates:** prmeta: ok size: 461;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/codetools );deadcode: ok;coverage: rc=0 ok   github.com/damnbot/damnbot/internal/codetools 13.786s coverage: 83.3% of statements in ./internal/codetools/... COVERAGE ok    github.com/damnbot/damnbot/internal/codetools: 83.3% (min 80%) ;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;LOC ok codetools 3920 / 4500;;tidy: ok;tidy-bench: ok;

## f1-ct-11 (2476388)

PR base: f1-ct-10

**Title:** `feat(codetools): edit_file exact edits with stale_read diffs`

**Body:**

```text
Stage 11/13 of the F1 codetools stack (base: f1-ct-10). `edit_file`
(CODE-TOOLS §5.2, §5.3, §9.4; ADR-103 butir 7):

- Exact-only string edits (`old_string`/`new_string`/`replace_all`) and
  line ranges (`start_line`/`end_line`/`new_text`), up to 50 per call,
  applied atomically; overlaps, no-ops and redaction sentinels rejected.
- `no_match` lists up to 3 informative candidates (whitespace, case,
  NFC via `golang.org/x/text/unicode/norm`, EOL differences); ambiguous
  matches list their lines.
- `stale_read` reports the changed line ranges since the tag (Myers
  diff against the snapshot cached at read time).
- Result: `edited path #a→#b · N edits · +x -y` plus a bounded unified
  diff. Files above 64 MiB are `too_large` here; streaming line-range
  edits land in the next stage.
- New direct dependency `golang.org/x/text` (already in deps.allow).

Docs: docs/DECISIONS.md (ADR-103 butir 7). Tests: TestEditFormat,
TestEditLimits (CT-33), TestStatTagRacy, TestCacheIdleRelease,
TestEdgeCases, TestSetgidPreserved, TestFIFONoHang, FuzzEditApply.
```

**Gates:** prmeta: ok size: 598;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/codetools );deadcode: ok;coverage: rc=0 ok   github.com/damnbot/damnbot/internal/codetools 17.331s coverage: 84.6% of statements in ./internal/codetools/... COVERAGE ok    github.com/damnbot/damnbot/internal/codetools: 84.6% (min 80%) ;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;LOC ok codetools 4437 / 4500;;tidy: ok;tidy-bench: ok;

## f1-ct-12 (83898bb)

PR base: f1-ct-11

**Title:** `feat(codetools): hard-link in-place writes and streaming edits`

**Body:**

```text
Stage 12/13 of the F1 codetools stack (base: f1-ct-11). Hard-linked
files and files above 64 MiB (CODE-TOOLS §5.4; ADR-103 butir 13;
ADR-105):

- In-place rewrite for `nlink > 1` (approved `hardlinked_file`, ADR-097
  butir 12): the new content is first written completely to an fsynced
  temp (validation, no-op or disk full stop there, target untouched);
  then re-stat, old bytes to `Config.Snapshot`, fsynced backup
  `.<name>.damnbot-bak-<rand>`, preallocation (`fallocate` on Linux),
  copy, read-back. A failed copy is restored from the backup; only a
  crash mid-copy leaves a partial file, with the backup kept.
- Line-range edits on files > 64 MiB stream through the line index into
  the temp, no-ops detected while streaming (temp discarded).
- Edit accuracy suite CT-20 (330+ txtar cases incl. hardlink and huge),
  `.gitattributes` keeps testdata byte-exact on Windows.
- ADR-105: codetools LOC budget 4.500 → 4.800 (this stage reaches 4.728
  scc); `loc.budget` and ENGINEERING §2.4 updated.

Docs: docs/CODE-TOOLS.md §5.4, docs/TRUST-MODEL.md (in-place residual,
path-based rename residual), docs/ENGINEERING.md §2.4, docs/DECISIONS.md
(ADR-103 butir 13, ADR-105). REVIEW-F1-SEC2 closed here: CT27-2,
CT27-8. Tests: TestEditAccuracySuite, TestInPlaceEdit,
TestInPlaceFailuresKeepFile, TestInPlaceLargeFile, TestEditLockAlias,
TestInPlaceKill9.
```

**Gates:** prmeta: ok size: 371;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/codetools );deadcode: ok;coverage: rc=0 ok   github.com/damnbot/damnbot/internal/codetools 16.204s coverage: 88.3% of statements in ./internal/codetools/... COVERAGE ok    github.com/damnbot/damnbot/internal/codetools: 88.3% (min 80%) ;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;LOC ok codetools 4728 / 4800;;tidy: ok;tidy-bench: ok;

## f1-ct-13 (021e032)

PR base: f1-ct-12

**Title:** `feat(codetools): tool dispatch, corpus benches and CI gates`

**Body:**

```text
Stage 13/13 of the F1 codetools stack (base: f1-ct-12); the tree equals
f1-codetools-1 (b2fec95). Dispatch and F1 acceptance (CODE-TOOLS §10;
ADR-103 butir 11):

- `Schemas()` (CT-27: ≤ 200 tokens each) and `Engine.Run` with strict
  JSON decoding (unknown fields fail).
- Benchmarks `BenchmarkCodetools/<Spec>` (CT-04..07, CT-09) for the
  `perf` job; corpus benches `BenchmarkGrepLiteral/GrepRegex/Glob` and
  `TestGrepSpeedVsRg` (CT-01..03 ratios vs rg 15.1.0 on the same runner),
  `TestGrepParityRg` (CT-21), token goldens (CT-11, bench module).
- `scripts/corpora.sh` + `make corpora`, CI job `bench-parity` (PR) and
  nightly `bench-codetools`; without corpora the tests fail, not skip
  (`DAMNBOT_REQUIRE_CORPORA=1`).
- `archtest.TestCodeToolsIDsComplete`: every CT ID of CODE-TOOLS §10 has
  a test or benchmark.

Docs: docs/CODE-TOOLS.md §10 (CT-01..03 gates), docs/DECISIONS.md
(ADR-103 butir 11). REVIEW-F1-SEC2 closed here: CT27-7.
```

**Gates:** prmeta: ok size: 454;vet-linux: ok;vet-darwin: ok;vet-windows: ok;test: ok (./internal/archtest ./internal/codetools );deadcode: ok;coverage: rc=0 ok   github.com/damnbot/damnbot/internal/codetools 19.930s coverage: 88.3% of statements in ./internal/codetools/... COVERAGE ok    github.com/damnbot/damnbot/internal/codetools: 88.3% (min 80%) ;lint: ok;loc: ok LOC ok sandbox+redact 1447 / 1450;LOC ok codetools 4786 / 4800;;tidy: ok;tidy-bench: ok;

