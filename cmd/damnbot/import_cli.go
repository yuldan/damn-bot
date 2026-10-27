// `damnbot import hermes` (SPEC §9.15, ADR-200): F3 writes skills and memory;
// every other documented category is skipped, and --only for one is an error.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/memstore"
	"github.com/damnbot/damnbot/internal/importer"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/skills"
)

func runImport(args []string, stdout, stderr io.Writer) (code int) {
	var o struct {
		from, only string
		dryRun     bool
	}
	fs := flag.NewFlagSet("import hermes", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.from, "from", "", "Hermes home to import (default ~/.hermes)")
	fs.StringVar(&o.only, "only", "", "comma-separated categories (SPEC §9.15)")
	fs.BoolVar(&o.dryRun, "dry-run", false, "report what would be imported; write nothing")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) == 1 && pos[0] != "hermes" {
		say(stderr, "import: %s is not available in F3 (SPEC §9.15: hermes only)\n", cleanArg(pos[0]))
		return 2
	}
	if len(pos) != 1 {
		importUsage(stderr)
		return 2
	}
	unsupported, ok := parseImportOnly(o.only, stderr)
	if !ok {
		return 2
	}
	root, err := resolveHermesRoot(o.from)
	if err != nil {
		return importFail(stderr, err)
	}
	ctx := context.Background()
	sink := &hermesSink{noWrite: noWrite{"import: writer is not available"}, root: root, red: redact.New(redact.Options{})}
	deps := importer.Deps{
		ReadFile: root.ReadFile, WalkDir: filepath.WalkDir,
		Sink: sink, Secrets: noWrite{"import: keychain writer is not available"},
	}
	opts := importer.Options{Unsupported: unsupported, DryRun: o.dryRun}
	if !o.dryRun {
		_, dataDir, perr := skillsPaths()
		if perr != nil {
			return importFail(stderr, perr)
		}
		sink.dataDir = dataDir
	}
	imp, err := importer.New(deps)
	if err != nil {
		return importFail(stderr, err)
	}
	report, err := imp.Import(ctx, root.path, opts)
	var closeErr error
	if sink.db != nil {
		closeErr = sink.db.CloseContext(ctx)
	}
	if err != nil {
		return importFail(stderr, err)
	}
	if closeErr != nil {
		return importFail(stderr, closeErr)
	}
	return writeImportReport(stdout, root.path, &report, o.dryRun)
}

func importFail(stderr io.Writer, err error) int {
	say(stderr, "import hermes: %s\n", cleanLine(err.Error()))
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
		case "skills", "memory":
			wanted[name] = true
		case "config", "secrets", "mcp", "cron", "sessions":
			say(stderr, "import: --only %s is not supported in F3: no writer for that category (SPEC §9.15); nothing was written\n", name)
			return nil, false
		default:
			say(stderr, "import: unknown --only category %s (SPEC §9.15: config,secrets,skills,memory,mcp,cron,sessions)\n", cleanArg(name))
			return nil, false
		}
	}
	for _, name := range []importer.Source{importer.SourceSkills, importer.SourceMemory} {
		if !wanted[string(name)] {
			out[name] = "not selected (--only); nothing written"
		}
	}
	return out, true
}

// resolveHermesRoot validates --from; a missing or non-directory source is an
// error, not an empty import.
func resolveHermesRoot(from string) (hermesRoot, error) {
	if from == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return hermesRoot{}, fmt.Errorf("home directory: %w", err)
		}
		from = filepath.Join(home, ".hermes")
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

// hermesRoot confines every read to the resolved tree: a link out of it is
// rejected before it is read, and os.Root is the second half of that.
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

// hermesSink opens the database on the first item, so rejected input writes nothing.
type hermesSink struct {
	noWrite
	root    hermesRoot
	red     *redact.Redactor
	dataDir string
	db      *ctxdb.Store
	skill   *skills.Service
	mem     *memory.Service
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

// existingSkill reports whether the name exists and whether its content matches.
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

// writeImportReport prints one line per category, skip and clamp, never a secret.
func writeImportReport(w io.Writer, root string, rep *importer.Report, dryRun bool) int {
	sessionsPrintf(w, "hermes: %s\n", cleanLine(root))
	categories := []struct {
		name string
		rep  *importer.SourceReport
	}{
		{"config", &rep.Config},
		{"secrets", &rep.Secrets},
		{"skills", &rep.Skills},
		{"memory", &rep.Memory},
		{"mcp", &rep.MCP},
		{"cron", &rep.Cron},
	}
	for _, cat := range categories {
		sessionsPrintf(w, "%s: %d items, %d imported, %d changed, %d skipped\n",
			cat.name, cat.rep.Total, cat.rep.Imported, cat.rep.Changed, len(cat.rep.Skipped))
		for _, skip := range cat.rep.Skipped {
			sessionsPrintf(w, "  skipped %s: %s\n", cleanLine(skip.Key), cleanLine(skip.Reason))
		}
	}
	sessionsPrintf(w, "sessions: skipped: no sessions collector in F3 (SPEC §9.15)\n")
	for _, clamp := range rep.Clamps {
		sessionsPrintf(w, "clamped %s: %d -> %d\n", cleanLine(clamp.Key), clamp.From, clamp.To)
	}
	sessionsPrintf(w, "import: %d items, %d imported, %d changed, %d skipped, %.1f%% accounted\n",
		rep.Total(), rep.Imported(), rep.Changed(), rep.SkippedCount(), rep.Coverage()*100)
	if dryRun {
		sessionsPrintf(w, "dry run: nothing was written\n")
	}
	return 0
}
