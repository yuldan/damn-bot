package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/skills"
	"github.com/damnbot/damnbot/internal/tools"
)

type skillStore struct {
	db   *ctxdb.Store
	host skills.Host // GAP-TOOL-10: unmet requirements never reach retrieval
}

var _ skills.Store = skillStore{}

func (s skillStore) Index(ctx context.Context, sk *skills.Skill) (int64, error) {
	row, err := skillRow(sk)
	if err != nil {
		return 0, err
	}
	id, err := skillstore.Index(ctx, s.db, row, 0)
	if err != nil {
		return 0, fmt.Errorf("skillstore index: %w", err)
	}
	return id, nil
}

func (s skillStore) Quarantine(ctx context.Context, sk *skills.Skill, scan string, rejected bool) (int64, int64, error) {
	row, err := skillRow(sk)
	if err != nil {
		return 0, 0, err
	}
	memoryID, skillID, err := skillstore.Quarantine(ctx, s.db, row, scan, rejected, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("skillstore quarantine: %w", err)
	}
	return memoryID, skillID, nil
}

func (s skillStore) Approve(ctx context.Context, memoryID int64) error {
	return skillstore.Approve(ctx, s.db, memoryID, 0) //nolint:wrapcheck // caller is the composition root
}

func (s skillStore) Search(ctx context.Context, text string, k int) ([]skills.Skill, error) {
	rows, err := skillstore.Search(ctx, s.db, text, k)
	if err != nil {
		return nil, fmt.Errorf("skill search: %w", err)
	}
	out := make([]skills.Skill, 0, len(rows))
	for i := range rows {
		sk := skillFromRow(&rows[i])
		if len(skillReasons(&sk, s.host)) > 0 {
			continue // GAP-TOOL-10: unmet requirements leave the index/catalog
		}
		out = append(out, sk)
	}
	return out, nil
}

func (s skillStore) Status(ctx context.Context, memoryID int64) (skills.Status, error) {
	st, err := skillstore.Status(ctx, s.db, memoryID)
	return skills.Status(st), err
}

// skillRow maps one parsed skill to its index row, storing the host
// requirement so read paths never parse SKILL.md again (ADR-208).
func skillRow(sk *skills.Skill) (*skillstore.Row, error) {
	requires, err := sk.Host.JSON()
	if err != nil {
		return nil, fmt.Errorf("skill row: %w", err)
	}
	return &skillstore.Row{
		ID: sk.ID, Name: sk.Name, Description: sk.Description, Body: sk.Body, Path: sk.Path,
		SHA256: sk.SHA256, Source: string(sk.Source), MemoryID: sk.MemoryID,
		RequiresHost: requires,
	}, nil
}

func skillFromRow(r *skillstore.Row) skills.Skill {
	sk := skills.Skill{
		ID: r.ID, Name: r.Name, Description: r.Description, Body: r.Body, Path: r.Path,
		SHA256: r.SHA256, Source: skills.Source(r.Source), MemoryID: r.MemoryID,
	}
	if host, err := skills.ParseHostRequirementJSON(r.RequiresHost); err != nil {
		sk.RequirementErr = err
	} else {
		sk.Host = host
	}
	return sk
}

// skillReasons is the one evaluation used by every catalog/retrieval surface:
// a corrupt stored requirement is unmet (fail closed), otherwise the host
// evaluator decides.
func skillReasons(sk *skills.Skill, host skills.Host) []skills.Reason {
	if sk.RequirementErr != nil {
		return []skills.Reason{{Field: "requires", Detail: "invalid stored requirement"}}
	}
	return sk.Unavailable(host)
}

type skillManager struct {
	db      *ctxdb.Store
	svc     *skills.Service
	dir     string
	userDir string
	host    skills.Host // GAP-TOOL-10: catalog filtering
}

var _ tools.SkillManager = (*skillManager)(nil)

func newSkillManager(ctx context.Context, db *ctxdb.Store, dataDir, configDir string, red *redact.Redactor) (*skillManager, error) {
	dir := filepath.Join(dataDir, "skills", "agent")
	userDir := filepath.Join(configDir, "skills")
	st := skillStore{db: db, host: skillHost()}
	svc, err := skills.New(st, skills.Config{
		Redact: func(s string) string { return red.String(s) },
		Scan: func(sk *skills.Skill) (string, string, error) {
			scan := memory.ScanText(red, sk.Description+"\n"+sk.Body)
			b, jerr := scan.JSON()
			return string(scan.Severity), b, jerr
		},
		//nolint:contextcheck // Config.Known has no ctx parameter.
		Known: func(name string) bool {
			_, found, kerr := skillstore.ByName(context.Background(), db, name)
			return kerr == nil && found
		},
		Files: func(sk *skills.Skill) (fs.FS, error) {
			skillDir := filepath.Dir(sk.Path)
			if _, statErr := os.Stat(skillDir); statErr != nil {
				return nil, fmt.Errorf("skill directory: %w", statErr)
			}
			return rootedFS{dir: skillDir}, nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}
	m := &skillManager{db: db, svc: svc, dir: dir, userDir: userDir, host: skillHost()}
	m.materializeApproved(ctx)
	return m, nil
}

// materializeApproved copies an approved agent skill into the trusted user
// skill directory only after approval. It never overwrites a directory that
// is not already this row's path, so a user-created disk skill cannot be
// clobbered by a late approval.
func (m *skillManager) materializeApproved(ctx context.Context) {
	rows, err := skillstore.Search(ctx, m.db, "", 100)
	if err != nil {
		return
	}
	for i := range rows {
		r := &rows[i]
		if r.Source != "agent" {
			continue
		}
		src := filepath.Dir(r.Path)
		if filepath.Clean(src) != filepath.Join(m.dir, r.Name) {
			continue
		}
		dst := filepath.Join(m.userDir, r.Name)
		if _, serr := os.Stat(dst); serr == nil {
			continue
		}
		if cerr := copySkillDir(src, dst); cerr != nil {
			continue
		}
		if rerr := skillstore.Replace(ctx, m.db, r.Name, filepath.Join(dst, "SKILL.md"), r.ID); rerr != nil {
			continue
		}
	}
}

func copySkillDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("materialize skill: %w", err)
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return fmt.Errorf("materialize skill: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			_ = cerr
		}
	}()
	if err := os.CopyFS(dst, root.FS()); err != nil {
		return fmt.Errorf("materialize skill: %w", err)
	}
	return nil
}

type rootedFS struct{ dir string }

func (r rootedFS) Open(name string) (fs.File, error) {
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		return nil, fmt.Errorf("open skill root: %w", err)
	}
	f, err := root.Open(name)
	if err != nil {
		if cerr := root.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return nil, fmt.Errorf("open skill file: %w", err)
	}
	return rootedFile{File: f, root: root}, nil
}

type rootedFile struct {
	*os.File
	root *os.Root
}

func (f rootedFile) Close() error {
	return errors.Join(f.File.Close(), f.root.Close())
}

func (m *skillManager) ListSkills(ctx context.Context, limit int) ([]tools.SkillInfo, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	rows, err := skillstore.Search(ctx, m.db, "", limit)
	if err != nil {
		return nil, fmt.Errorf("skills list: %w", err)
	}
	out := make([]tools.SkillInfo, 0, len(rows))
	for i := range rows {
		sk := skillFromRow(&rows[i])
		if len(skillReasons(&sk, m.host)) > 0 {
			continue // GAP-TOOL-10: unmet requirements never enter the catalog
		}
		out = append(out, tools.SkillInfo{Name: sk.Name, Description: sk.Description})
	}
	return out, nil
}

func (m *skillManager) ViewSkill(ctx context.Context, name string) (tools.SkillDoc, error) {
	r, found, err := skillstore.ByName(ctx, m.db, name)
	if err != nil {
		return tools.SkillDoc{}, fmt.Errorf("skills view: %w", err)
	}
	if !found {
		return tools.SkillDoc{}, tools.ErrSkillNotFound
	}
	files, truncated, ferr := readSkillFiles(filepath.Dir(r.Path))
	if ferr != nil {
		return tools.SkillDoc{}, ferr
	}
	return tools.SkillDoc{Name: r.Name, Description: r.Description, Body: r.Body, Files: files, Truncated: truncated}, nil
}

func readSkillFiles(dir string) ([]tools.SkillFile, bool, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, false, fmt.Errorf("skill files: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			_ = cerr // inspection completed; close errors are not actionable
		}
	}()
	var out []tools.SkillFile
	total := 0
	truncated := false
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || strings.EqualFold(path.Base(p), "SKILL.md") || len(out) >= 20 {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return fmt.Errorf("skill file info: %w", ierr)
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		if fi.Size() > 64<<10 || total+int(fi.Size()) > 64<<10 {
			truncated = true
			return fs.SkipAll
		}
		b, rerr := root.ReadFile(p)
		if rerr != nil {
			return fmt.Errorf("read skill file: %w", rerr)
		}
		if !utf8.Valid(b) {
			return nil
		}
		total += len(b)
		out = append(out, tools.SkillFile{Path: p, Content: string(b)})
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("skill files: %w", err)
	}
	return out, truncated, nil
}

func (m *skillManager) ManageSkill(ctx context.Context, c *tools.SkillChange) (tools.SkillResult, error) {
	if err := m.guardChange(ctx, c); err != nil {
		return tools.SkillResult{}, err
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return tools.SkillResult{}, fmt.Errorf("skills dir: %w", err)
	}
	root, err := os.OpenRoot(m.dir)
	if err != nil {
		return tools.SkillResult{}, fmt.Errorf("skills dir: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			_ = cerr // the action is complete; close errors are not actionable
		}
	}()
	if c.Action == "delete" {
		return m.deleteSkill(ctx, root, c)
	}
	if verr := validateSkillChange(c); verr != nil {
		return tools.SkillResult{}, verr
	}
	doc := buildSkillDoc(c)
	if _, perr := skills.Parse([]byte(doc), filepath.Join(m.dir, c.Name, "SKILL.md"), skills.SourceAgent); perr != nil {
		return tools.SkillResult{}, &tools.ToolError{Code: tools.CodeInvalidArgs, Message: perr.Error()}
	}
	return m.installStaged(ctx, root, c, doc)
}

func (m *skillManager) installStaged(ctx context.Context, root *os.Root, c *tools.SkillChange, doc string) (tools.SkillResult, error) {
	stage, err := os.MkdirTemp(m.dir, ".stage-"+c.Name+"-")
	if err != nil {
		return tools.SkillResult{}, fmt.Errorf("skill staging: %w", err)
	}
	defer func() {
		if rerr := os.RemoveAll(stage); rerr != nil {
			_ = rerr
		}
	}()
	stageBase := filepath.Base(stage)
	if werr := writeManagedSkill(root, c, stageBase); werr != nil {
		return tools.SkillResult{}, werr
	}
	sk, err := skills.Parse([]byte(doc), filepath.Join(stage, "SKILL.md"), skills.SourceAgent)
	if err != nil {
		return tools.SkillResult{}, &tools.ToolError{Code: tools.CodeInvalidArgs, Message: err.Error()}
	}
	var old *skillstore.Row
	if c.Action == "update" {
		if r, found, rerr := skillstore.ByName(ctx, m.db, c.Name); rerr != nil {
			return tools.SkillResult{}, fmt.Errorf("previous skill: %w", rerr)
		} else if found {
			old = &r
		}
	}
	inst, err := m.svc.Install(ctx, &sk)
	if err != nil {
		return tools.SkillResult{}, m.installFailed(ctx, old, fmt.Errorf("install skill: %w", err))
	}
	if inst.Status != skills.StatusScanned {
		if old == nil {
			if derr := skillstore.DeleteID(ctx, m.db, inst.SkillID); derr != nil {
				return tools.SkillResult{}, fmt.Errorf("drop rejected skill: %w", derr)
			}
		}
		return tools.SkillResult{Action: c.Action, Name: c.Name, Status: string(inst.Status), MemoryID: inst.MemoryID},
			m.restoreSkill(ctx, old)
	}
	if serr := m.swapSkill(ctx, root, c.Name, stageBase, inst.SkillID); serr != nil {
		if derr := skillstore.DeleteID(ctx, m.db, inst.SkillID); derr != nil {
			serr = errors.Join(serr, fmt.Errorf("drop staged skill: %w", derr))
		}
		return tools.SkillResult{}, m.installFailed(ctx, old, serr)
	}
	return tools.SkillResult{Action: c.Action, Name: c.Name, Status: string(inst.Status), MemoryID: inst.MemoryID}, nil
}

func rollbackRename(root *os.Root, cause error, from, to string) error {
	if err := root.Rename(from, to); err != nil {
		return errors.Join(cause, fmt.Errorf("rollback skill: %w", err))
	}
	return cause
}

// swapSkill moves the validated staging directory into place and only then
// re-points the new agent row and removes the previous agent rows. On any
// failure the previous directory is restored.
// installFailed restores the previous approved version when a replacing
// install failed after the quarantine transaction removed it.
func (m *skillManager) installFailed(ctx context.Context, old *skillstore.Row, cause error) error {
	if old == nil {
		return cause
	}
	if _, found, err := skillstore.ByName(ctx, m.db, old.Name); err == nil && found {
		return cause
	}
	return errors.Join(cause, m.restoreSkill(ctx, old))
}

func (m *skillManager) restoreSkill(ctx context.Context, old *skillstore.Row) error {
	if old == nil {
		return nil
	}
	r := *old
	r.MemoryID = 0
	memoryID, _, err := skillstore.Quarantine(ctx, m.db, &r, "{}", false, 0)
	if err != nil {
		return fmt.Errorf("restore previous skill: %w", err)
	}
	if aerr := skillstore.Approve(ctx, m.db, memoryID, 0); aerr != nil {
		return fmt.Errorf("approve restored skill: %w", aerr)
	}
	return nil
}

func (m *skillManager) swapSkill(ctx context.Context, root *os.Root, name, stageBase string, newID int64) error {
	backup := stageBase + ".old"
	hasOld := false
	if _, err := root.Stat(name); err == nil {
		if rerr := root.Rename(name, backup); rerr != nil {
			return fmt.Errorf("backup skill: %w", rerr)
		}
		hasOld = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat skill: %w", err)
	}
	if err := root.Rename(stageBase, name); err != nil {
		if hasOld {
			return rollbackRename(root, fmt.Errorf("activate skill: %w", err), backup, name)
		}
		return fmt.Errorf("activate skill: %w", err)
	}
	if err := skillstore.Replace(ctx, m.db, name, filepath.Join(m.dir, name, "SKILL.md"), newID); err != nil {
		cerr := fmt.Errorf("commit skill: %w", err)
		if rerr := root.Rename(name, stageBase); rerr != nil {
			cerr = errors.Join(cerr, rerr)
		}
		if hasOld {
			return rollbackRename(root, cerr, backup, name)
		}
		return cerr
	}
	if hasOld {
		if err := root.RemoveAll(backup); err != nil {
			return fmt.Errorf("remove old skill: %w", err)
		}
	}
	return nil
}

func (m *skillManager) guardChange(ctx context.Context, c *tools.SkillChange) error {
	if !validSkillName(c.Name) {
		return &tools.ToolError{Code: tools.CodeInvalidArgs, Message: "name must be [a-z0-9][a-z0-9._-]{0,63}"}
	}
	source, found, err := skillstore.SourceOf(ctx, m.db, c.Name)
	if err != nil {
		return fmt.Errorf("skill source: %w", err)
	}
	switch c.Action {
	case "create":
		if found {
			return tools.ErrSkillExists
		}
		for _, dir := range []string{filepath.Join(m.userDir, c.Name), filepath.Join(m.dir, c.Name)} {
			if _, serr := os.Stat(dir); serr == nil {
				return tools.ErrSkillExists
			} else if !errors.Is(serr, fs.ErrNotExist) {
				return fmt.Errorf("skill name check: %w", serr)
			}
		}
	case "update", "delete":
		if !found || source != "agent" {
			return tools.ErrSkillNotFound
		}
	default:
		return &tools.ToolError{Code: tools.CodeInvalidArgs, Message: "action must be create, update or delete"}
	}
	return nil
}

func (m *skillManager) deleteSkill(ctx context.Context, _ *os.Root, c *tools.SkillChange) (tools.SkillResult, error) {
	path, found, err := skillstore.AgentPath(ctx, m.db, c.Name)
	if err != nil {
		return tools.SkillResult{}, fmt.Errorf("delete skill: %w", err)
	}
	if !found {
		return tools.SkillResult{}, tools.ErrSkillNotFound
	}
	dir := filepath.Dir(path)
	if filepath.Clean(dir) != filepath.Join(m.dir, c.Name) && filepath.Clean(dir) != filepath.Join(m.userDir, c.Name) {
		return tools.SkillResult{}, fmt.Errorf("delete skill: unexpected path")
	}
	if err := os.RemoveAll(dir); err != nil {
		return tools.SkillResult{}, fmt.Errorf("delete skill: %w", err)
	}
	if err := skillstore.Delete(ctx, m.db, c.Name); err != nil {
		if errors.Is(err, skillstore.ErrNotFound) {
			return tools.SkillResult{}, tools.ErrSkillNotFound
		}
		return tools.SkillResult{}, fmt.Errorf("delete skill row: %w", err)
	}
	return tools.SkillResult{Action: c.Action, Name: c.Name, Status: "deleted"}, nil
}

func validateSkillChange(c *tools.SkillChange) error {
	bad := func(msg string) error {
		return &tools.ToolError{Code: tools.CodeInvalidArgs, Message: msg}
	}
	if len(c.Description) > 200 || strings.IndexFunc(c.Description, func(r rune) bool {
		return r == '\r' || r == '\n' || r == '\u2028' || r == '\u2029' || unicode.IsControl(r)
	}) >= 0 {
		return bad("description must be one line without control characters")
	}
	if len(c.Body) > 8192 {
		return bad("body exceeds 8192 bytes")
	}
	if len(c.Files) > 20 {
		return bad("at most 20 files")
	}
	if err := validateSkillFiles(c.Files); err != nil {
		return err
	}
	if err := validateEffects(c); err != nil {
		return err
	}
	return nil
}

func validateEffects(c *tools.SkillChange) error {
	bad := func(msg string) error {
		return &tools.ToolError{Code: tools.CodeInvalidArgs, Message: msg}
	}
	for _, e := range c.SideEffects {
		if e == "" || len(e) > 32 {
			return bad("invalid side effect")
		}
		for _, r := range e {
			if r < 'a' || r > 'z' {
				return bad("invalid side effect " + e)
			}
		}
	}
	for _, r := range c.Requires {
		if !validSkillName(r) {
			return bad("invalid required skill " + r)
		}
	}
	return nil
}

func validateSkillFiles(files []tools.SkillFile) error {
	for _, f := range files {
		if _, ok := safeSkillFile(f.Path); !ok {
			return &tools.ToolError{Code: tools.CodeInvalidArgs, Message: "unsafe skill file path " + f.Path}
		}
		if len(f.Content) > 64<<10 {
			return &tools.ToolError{Code: tools.CodeInvalidArgs, Message: "skill file exceeds 64 KiB"}
		}
	}
	return nil
}

func buildSkillDoc(c *tools.SkillChange) string {
	desc := "\"" + c.Description + "\""
	doc := "---\nname: " + c.Name + "\ndescription: " + desc + "\n"
	if len(c.SideEffects) > 0 {
		doc += "side_effects: [" + strings.Join(c.SideEffects, ", ") + "]\n"
	}
	if len(c.Requires) > 0 {
		doc += "requires: [" + strings.Join(c.Requires, ", ") + "]\n"
	}
	return doc + "---\n" + c.Body
}

func writeManagedSkill(root *os.Root, c *tools.SkillChange, dir string) error {
	if err := root.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("skill dir: %w", err)
	}
	if err := writeAtomic(root, filepath.Join(dir, "SKILL.md"), []byte(buildSkillDoc(c))); err != nil {
		return err
	}
	for _, f := range c.Files {
		rel, ok := safeSkillFile(f.Path)
		if !ok {
			return &tools.ToolError{Code: tools.CodeInvalidArgs, Message: "unsafe skill file path " + f.Path}
		}
		full := filepath.Join(dir, rel)
		if err := root.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return fmt.Errorf("skill file dir: %w", err)
		}
		if err := writeAtomic(root, full, []byte(f.Content)); err != nil {
			return err
		}
	}
	return nil
}

func writeAtomic(root *os.Root, name string, data []byte) error {
	tmp := name + ".tmp"
	if err := root.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write skill: %w", err)
	}
	if err := root.Rename(tmp, name); err != nil {
		return fmt.Errorf("rename skill: %w", err)
	}
	return nil
}

func safeSkillFile(p string) (string, bool) {
	if p == "" || strings.ContainsAny(p, "\\\x00") || path.IsAbs(p) {
		return "", false
	}
	clean := path.Clean(p)
	// SEC16-5: SKILL.md is the skill document; a supporting file must never
	// shadow it, including on case-insensitive filesystems (skill.md, Skill.MD),
	// at any depth.
	if clean == "." || clean == ".." || strings.EqualFold(path.Base(clean), "SKILL.md") || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return filepath.FromSlash(clean), true
}

var skillNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func validSkillName(s string) bool { return skillNameRE.MatchString(s) }
