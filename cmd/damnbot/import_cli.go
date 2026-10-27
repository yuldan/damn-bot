// `damnbot import hermes` (SPEC §9.15, ADR-200): F3 writes skills and memory;
// every other documented category is skipped, and --only for one is an error.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/importstore"
	"github.com/damnbot/damnbot/internal/ctxdb/memstore"
	"github.com/damnbot/damnbot/internal/importer"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/skills"
)

type importOptions struct {
	from, only string
	dryRun     bool
}

func runImport(args []string, stdout, stderr io.Writer) (code int) {
	var o importOptions
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.from, "from", "", "source home to import (default: the importer's home)")
	fs.StringVar(&o.only, "only", "", "comma-separated categories (SPEC §9.15, ADR-218)")
	fs.BoolVar(&o.dryRun, "dry-run", false, "report what would be imported; write nothing")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		importUsage(stderr)
		return 2
	}
	if pos[0] == "claude-code" {
		return runClaudeImport(o, stdout, stderr)
	}
	if pos[0] != "hermes" {
		say(stderr, "import: %s is not implemented yet (SPEC §9.15 parity: OPS-034/OPS-035; ADR-218)\n", cleanArg(pos[0]))
		return 2
	}
	unsupported, ok := parseImportOnly(o.only, stderr)
	if !ok {
		return 2
	}
	root, err := resolveImportRoot(o.from, ".hermes")
	if err != nil {
		return importFail(stderr, "hermes", err)
	}
	ctx := context.Background()
	sink := &hermesSink{noWrite: noWrite{"import: writer is not available"}, root: root, red: redact.New(redact.Options{})}
	deps := importer.Deps{
		ReadFile: root.ReadFile, WalkDir: filepath.WalkDir,
		Sink: sink, SessionSink: sink,
		Secrets: noWrite{"import: keychain writer is not available"}, Sessions: hermesSessions{},
	}
	opts := importer.Options{Unsupported: unsupported, DryRun: o.dryRun}
	if !o.dryRun {
		_, dataDir, perr := skillsPaths()
		if perr != nil {
			return importFail(stderr, "hermes", perr)
		}
		sink.dataDir = dataDir
	}
	imp, err := importer.New(&deps)
	if err != nil {
		return importFail(stderr, "hermes", err)
	}
	report, err := imp.Import(ctx, root.path, opts)
	var closeErr error
	if sink.db != nil {
		closeErr = sink.db.CloseContext(ctx)
	}
	if err != nil {
		return importFail(stderr, "hermes", err)
	}
	if closeErr != nil {
		return importFail(stderr, "hermes", closeErr)
	}
	return writeImportReport(stdout, root.path, &report, o.dryRun)
}

func importFail(stderr io.Writer, command string, err error) int {
	say(stderr, "import %s: %s\n", command, cleanLine(err.Error()))
	return 1
}

func importUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot import hermes [--from DIR] [--only config,secrets,skills,memory,mcp,cron,sessions] [--dry-run]\n")
}

// parseImportOnly turns --only into the reason each unselected or unwritable
// category is reported with; a category without a write seam is a usage error.
func parseImportOnly(value string, stderr io.Writer) (map[importer.Source]string, bool) {
	out := map[importer.Source]string{
		importer.SourceConfig:  "no config writer in F3 (SPEC §9.15); nothing written",
		importer.SourceSecrets: "no keychain writer in F3 (SPEC §9.15); nothing written",
		importer.SourceMCP:     "no MCP config writer in F3 (SPEC §9.15); nothing written",
		importer.SourceCron:    "no cron store in F3 (SPEC §9.15); nothing written",
	}
	if value == "" {
		return out, true
	}
	wanted := map[string]bool{}
	for name := range strings.SplitSeq(value, ",") {
		name = strings.TrimSpace(name)
		switch name {
		case "skills", "memory", "sessions":
			wanted[name] = true
		case "config", "secrets", "mcp", "cron":
			say(stderr, "import: --only %s is not supported in F3: no writer for that category (SPEC §9.15); nothing was written\n", name)
			return nil, false
		default:
			say(stderr, "import: unknown --only category %s (SPEC §9.15: config,secrets,skills,memory,mcp,cron,sessions)\n", cleanArg(name))
			return nil, false
		}
	}
	for _, name := range []importer.Source{importer.SourceSkills, importer.SourceMemory, importer.SourceSessions} {
		if !wanted[string(name)] {
			out[name] = "not selected (--only); nothing written"
		}
	}
	return out, true
}

func resolveImportRoot(from, defaultDir string) (hermesRoot, error) {
	if from == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return hermesRoot{}, fmt.Errorf("home directory: %w", err)
		}
		from = filepath.Join(home, defaultDir)
	}
	abs, err := filepath.Abs(from)
	if err != nil {
		return hermesRoot{}, fmt.Errorf("source %s: %w", cleanArg(from), err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return hermesRoot{}, fmt.Errorf("source %s: %w", cleanArg(from), err)
	}
	if !info.IsDir() {
		return hermesRoot{}, fmt.Errorf("source %s is not a directory", cleanArg(from))
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return hermesRoot{}, fmt.Errorf("source %s: %w", cleanArg(from), err)
	}
	return hermesRoot{path: resolved}, nil
}

type hermesRoot struct{ path string }

func (r hermesRoot) ReadFile(path string) (data []byte, err error) {
	rel, err := filepath.Rel(r.path, path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", cleanArg(path), err)
	}
	root, err := os.OpenRoot(r.path)
	if err != nil {
		return nil, fmt.Errorf("open import root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if data, err = root.ReadFile(rel); err != nil {
		return nil, fmt.Errorf("read %s: %w", cleanArg(path), err)
	}
	return data, nil
}

type hermesSessions struct{}

func (hermesSessions) ReadSessions(ctx context.Context, path string) (sessions []importer.Session, err error) {
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		return nil, nil
	} else if statErr != nil {
		return nil, fmt.Errorf("state.db: %w", statErr)
	}
	dir, err := os.MkdirTemp("", "damnbot-hermes-state-")
	if err != nil {
		return nil, fmt.Errorf("state.db temp: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	if cerr := copyHermesState(path, dir); cerr != nil {
		return nil, cerr
	}
	if sessions, err = importstore.ReadHermes(ctx, filepath.Join(dir, "state.db")); err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	return sessions, nil
}

func copyHermesState(path, dir string) (err error) {
	srcDir, srcName := filepath.Split(path)
	dst, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("state.db temp: %w", err)
	}
	defer func() { err = errors.Join(err, dst.Close()) }()
	for _, suffix := range []string{"", "-wal"} {
		if cerr := copyRootFile(srcDir, srcName+suffix, dst, "state.db"+suffix); cerr != nil && !errors.Is(cerr, fs.ErrNotExist) {
			return cerr
		}
	}
	return nil
}

func copyRootFile(srcDir, srcName string, dst *os.Root, dstName string) (err error) {
	src, err := os.OpenRoot(srcDir)
	if err != nil {
		return fmt.Errorf("open state.db: %w", err)
	}
	defer func() { err = errors.Join(err, src.Close()) }()
	in, err := src.Open(srcName)
	if err != nil {
		return fmt.Errorf("open %s: %w", srcName, err)
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	out, err := dst.OpenFile(dstName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", dstName, err)
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s: %w", srcName, err)
	}
	return nil
}

type hermesSink struct {
	noWrite
	root    hermesRoot
	red     *redact.Redactor
	dataDir string
	db      *ctxdb.Store
	skill   *skills.Service
	mem     *memory.Service
	runID   int64
}

func (s *hermesSink) ensure(ctx context.Context) error {
	if s.db != nil {
		return nil
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: s.dataDir, Kind: "cli"})
	if err != nil {
		return fmt.Errorf("open ctxdb: %w", err)
	}
	if s.mem, err = openMemory(db, s.red); err != nil {
		return errors.Join(err, db.CloseContext(ctx))
	}
	if s.skill, err = newSkillService(ctx, db, s.red); err != nil {
		return errors.Join(err, db.CloseContext(ctx))
	}
	s.db = db
	return nil
}

func (s *hermesSink) PutSkill(ctx context.Context, in *importer.Skill) (bool, error) {
	if err := s.ensure(ctx); err != nil {
		return false, err
	}
	exists, same, err := existingSkill(ctx, s.db, in.Name, in.SHA256)
	if err != nil {
		return false, err
	}
	if exists {
		if same {
			return false, nil // identical re-import: idempotent no-op
		}
		return false, &importer.SkipError{Reason: "a skill with this name already exists; not overwritten"}
	}
	sk := &skills.Skill{
		Name: in.Name, Description: in.Description, Body: in.Body,
		Path:   filepath.Join(s.root.path, filepath.FromSlash(in.Origin), "SKILL.md"),
		SHA256: in.SHA256, Source: skills.SourceImport,
	}
	if _, err := s.skill.Install(ctx, sk); err != nil {
		if errors.Is(err, skills.ErrInvalid) || errors.Is(err, skills.ErrScan) ||
			errors.Is(err, skills.ErrFrontMatter) || errors.Is(err, skills.ErrTooLarge) {
			return false, &importer.SkipError{Reason: err.Error()}
		}
		return false, fmt.Errorf("install skill: %w", err)
	}
	return true, nil
}

func (s *hermesSink) PutMemory(ctx context.Context, in importer.MemoryEntry) (bool, error) {
	if err := s.ensure(ctx); err != nil {
		return false, err
	}
	content := s.red.String(in.Text)
	hits, err := memstore.Search(ctx, s.db, &memstore.Query{
		Scopes: []string{"user"}, Now: time.Now().UnixMilli(),
		Statuses: []string{"pending", "scanned", "approved", "rejected", "retired", "expired"},
	})
	if err != nil {
		return false, fmt.Errorf("lookup memory: %w", err)
	}
	if slices.ContainsFunc(hits, func(h memstore.Hit) bool { return h.Row.Content == content }) {
		return false, nil
	}
	if _, err := s.mem.Propose(ctx, &memory.Proposal{
		Category: memory.CategoryFact, Scope: "user", Content: in.Text, Origin: memory.OriginImport,
	}); err != nil {
		return false, fmt.Errorf("propose memory: %w", err)
	}
	return true, nil
}

// PutSession writes one source session as an origin=import session plus
// kind=imported events, keyed by the source id and its content hash in
// imported_items. A re-import is a no-op; different content is a skip.
func (s *hermesSink) PutSession(ctx context.Context, in *importer.Session) (bool, error) {
	if err := s.ensure(ctx); err != nil {
		return false, err
	}
	key := sessionImportKey(in)
	existing, found, err := s.importedSession(ctx, in.SourceID)
	if err != nil {
		return false, err
	}
	if found {
		if existing == key {
			return false, nil
		}
		return false, &importer.SkipError{Reason: "source session already imported with different content; not overwritten"}
	}
	seed := sha256.Sum256([]byte("hermes|" + in.SourceID))
	sid := "imp_" + hex.EncodeToString(seed[:10])
	runID, err := s.createSession(ctx, in, sid, key, s.runID)
	if err != nil {
		return false, err
	}
	s.runID = runID
	return true, nil
}

func (s *hermesSink) createSession(ctx context.Context, in *importer.Session, sid, key string, runID int64) (int64, error) {
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		if runID == 0 {
			if rerr := tx.Get(`INSERT INTO import_runs(source, source_path, stats, created_at)
				VALUES ('hermes', ?1, '{}', ?2) RETURNING id`,
				[]any{s.root.path, time.Now().UnixMilli()}, &runID); rerr != nil {
				return fmt.Errorf("import run: %w", rerr)
			}
		}
		if _, cerr := tx.CreateSession(ctxdb.Session{ID: sid, Origin: "import", Cwd: in.Cwd, Model: in.Model}); cerr != nil {
			return fmt.Errorf("create session %s: %w", sid, cerr)
		}
		now := time.Now().UnixMilli()
		if in.Title != "" {
			if _, terr := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at)
				VALUES (?1, 'title', ?2, ?3)`, sid, in.Title, now); terr != nil {
				return fmt.Errorf("session title: %w", terr)
			}
		}
		for _, m := range in.Messages {
			payload := fmt.Sprintf(`{"source":"hermes","source_message_id":%d,"timestamp_ms":%d}`, m.SourceID, m.Timestamp)
			if _, eerr := tx.AppendEvent(sid, &ctxdb.Event{
				Kind: "imported", Role: m.Role, Content: m.Content, ToolName: m.ToolName, Payload: payload,
			}); eerr != nil {
				return fmt.Errorf("imported event: %w", eerr)
			}
		}
		if _, ierr := tx.Exec(`INSERT INTO imported_items(source, source_key, target_kind, target_id, run_id)
			VALUES ('hermes', ?1, 'session', ?2, ?3)`, key, sid, runID); ierr != nil {
			return fmt.Errorf("imported item: %w", ierr)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("write session %s: %w", sid, err)
	}
	return runID, nil
}

func (s *hermesSink) importedSession(ctx context.Context, sourceID string) (string, bool, error) {
	prefix := "session:" + sourceID + "#"
	var key string
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		qerr := tx.Get(`SELECT source_key FROM imported_items
			WHERE source = 'hermes' AND source_key LIKE ?1 ESCAPE '\' ORDER BY source_key LIMIT 1`,
			[]any{escapeLike(prefix) + "%"}, &key)
		if qerr != nil {
			return fmt.Errorf("imported session: %w", qerr)
		}
		return nil
	})
	if errors.Is(err, ctxdb.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lookup imported session: %w", err)
	}
	return key, true, nil
}

func sessionImportKey(in *importer.Session) string {
	h := sha256.New()
	write := func(parts ...string) {
		for _, part := range parts {
			h.Write([]byte(part))
			h.Write([]byte{0})
		}
	}
	write(in.Title, in.Model, in.Cwd)
	for _, m := range in.Messages {
		write(strconv.FormatInt(m.SourceID, 10), strconv.FormatInt(m.Timestamp, 10), m.Role, m.ToolName, m.Content)
	}
	return "session:" + in.SourceID + "#" + hex.EncodeToString(h.Sum(nil)[:16])
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func existingSkill(ctx context.Context, db *ctxdb.Store, name, sha string) (exists, same bool, err error) {
	var total, match int64
	err = db.Write(ctx, func(tx *ctxdb.Tx) error {
		return errors.Join(tx.Get(
			`SELECT count(*), count(*) FILTER (WHERE sha256 = ?2) FROM skills WHERE name = ?1`,
			[]any{name, sha}, &total, &match))
	})
	if err != nil {
		return false, false, fmt.Errorf("lookup skill %s: %w", cleanArg(name), err)
	}
	return total > 0, match > 0, nil
}

type noWrite struct{ reason string }

func (s noWrite) PutConfig(context.Context, importer.ConfigEntry) (bool, error) {
	return false, errors.New(s.reason)
}

func (s noWrite) PutMCPServer(context.Context, importer.MCPServer) (bool, error) {
	return false, errors.New(s.reason)
}

func (s noWrite) PutCron(context.Context, importer.CronJob) (bool, error) {
	return false, errors.New(s.reason)
}

func (s noWrite) Put(context.Context, string, string) (bool, error) {
	return false, errors.New(s.reason)
}

type reportCategory struct {
	name string
	rep  *importer.SourceReport
}

type reportTotals interface {
	Total() int
	Imported() int
	Changed() int
	SkippedCount() int
	Coverage() float64
}

func writeReport(w io.Writer, label, root string, categories []reportCategory, clamps []importer.Clamp, totals reportTotals, dryRun bool) int {
	sessionsPrintf(w, "%s: %s\n", label, cleanLine(root))
	for _, cat := range categories {
		sessionsPrintf(w, "%s: %d items, %d imported, %d changed, %d skipped\n",
			cat.name, cat.rep.Total, cat.rep.Imported, cat.rep.Changed, len(cat.rep.Skipped))
		for _, skip := range cat.rep.Skipped {
			sessionsPrintf(w, "  skipped %s: %s\n", cleanLine(skip.Key), cleanLine(skip.Reason))
		}
	}
	for _, clamp := range clamps {
		sessionsPrintf(w, "clamped %s: %d -> %d\n", cleanLine(clamp.Key), clamp.From, clamp.To)
	}
	sessionsPrintf(w, "import: %d items, %d imported, %d changed, %d skipped, %.1f%% accounted\n",
		totals.Total(), totals.Imported(), totals.Changed(), totals.SkippedCount(), totals.Coverage()*100)
	if dryRun {
		sessionsPrintf(w, "dry run: nothing was written\n")
	}
	return 0
}

func writeImportReport(w io.Writer, root string, rep *importer.Report, dryRun bool) int {
	return writeReport(w, "hermes", root, []reportCategory{
		{"config", &rep.Config},
		{"secrets", &rep.Secrets},
		{"skills", &rep.Skills},
		{"memory", &rep.Memory},
		{"mcp", &rep.MCP},
		{"cron", &rep.Cron},
		{"sessions", &rep.Sessions},
	}, rep.Clamps, rep, dryRun)
}
