package assemble

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

// Context caps from SPEC §6 (B-03). All are tokens counted by [Deps.Tokens].
const (
	CapFacts     = 60
	CapWorkspace = 150
	CapFiles     = 400
	CapFilesMin  = 200
	CapMemory    = 300
	CapPinned    = 150
	CapSkills    = 150
	CapContext   = 1100
	// CapContextDegraded is the whole-tier cap in PROV-02 degraded mode.
	CapContextDegraded = 600
	CapSeparators      = 40
	MaxSkills          = 3
	// MaxContextFileBytes bounds one sandbox read; the 400-token cap keeps
	// almost all of this out of the prompt, but a whole-file read would let
	// a 4 GiB file stall startup.
	MaxContextFileBytes = 256 << 10
)

// ContextFile is one sandbox-relative context file (AGENTS.md first, then
// repository conventions in caller priority order).
type ContextFile struct {
	Path string
}

// Memory is one approved memory entry. Pinned entries render first and are
// dropped only after the file tier has reached [CapFilesMin] (SPEC §6).
type Memory struct {
	Text   string
	Pinned bool
}

// FileReader opens a context file beneath the session sandbox. Implementations
// are sandbox-backed (internal/sandbox.OpenFile with verdict Inside); assemble
// never calls os.Open. A refused verdict returns an error matching
// [ErrUnavailable] so the file is omitted instead of leaking content.
type FileReader interface {
	Open(ctx context.Context, path string) (io.ReadCloser, error)
}

// Retriever is the consumer view of memory and skill retrieval (ADR-120,
// ADR-121; E-09). The composition root adapts memory.Service.Recall (approved,
// unexpired, pinned first, then BM25 rank) and skills.Service.Offer (at most
// three "- <name>: <description> (<path>)" lines, best first). Order is the
// contract: assemble never re-ranks, it only cuts at entry boundaries.
type Retriever interface {
	Memories(ctx context.Context, query string) ([]Memory, error)
	Skills(ctx context.Context, query string) ([]string, error)
}

// ContextInput is the caller-provided context tier. It is rendered once per
// session by [Assembler.Context]; the result is frozen (P15).
type ContextInput struct {
	Facts     string
	Workspace string
	Files     []ContextFile
	Reader    FileReader
	// UntrustedFiles wraps the context files in <untrusted source="workspace">
	// for an untrusted workspace (SPEC §14, ADR-069): they are data, not rules.
	UntrustedFiles bool
	// Query is the text memories and skills are ranked against (the first
	// user message of the session, SPEC §4.8).
	Query string
	// Retriever supplies memories and skills; nil renders neither slot.
	Retriever Retriever
}

// ContextSlots is each rendered slot, including its heading. The strings are
// also written to the P1 tiers fixture so per-slot caps are measurable.
type ContextSlots struct {
	Facts     string
	Workspace string
	Files     string
	Memory    string
	Skills    string
}

// ContextResult is the rendered context tier.
type ContextResult struct {
	Text  string
	Slots ContextSlots
}

// untrustedWorkspace opens the wrapper of untrusted context files.
const untrustedWorkspace = `<untrusted source="workspace">`

// Context renders the context tier. It returns [ErrDeps] when files are
// requested without a reader and [ErrContextPath] for an empty path; a sandbox
// refusal (ErrUnavailable) or a missing file omits that file. A retrieval
// error is returned, never skipped silently (SPEC §4.8).
func (a *Assembler) Context(ctx context.Context, in *ContextInput) (ContextResult, error) {
	if in == nil {
		return ContextResult{}, nil
	}
	filesBody, err := a.readContextFiles(ctx, in)
	if err != nil {
		return ContextResult{}, err
	}
	memories, skills, err := retrieve(ctx, in)
	if err != nil {
		return ContextResult{}, err
	}
	state := contextState{
		asm:        a,
		facts:      slot("# Environment", a.data(in.Facts), CapFacts, a.tokens),
		workspace:  slot("# Workspace", a.data(in.Workspace), CapWorkspace, a.tokens),
		filesBody:  filesBody,
		marker:     a.fileMarker(in.Files),
		untrusted:  in.UntrustedFiles,
		memories:   memories,
		skillNames: skills,
	}
	state.files = state.renderFiles(CapFiles)
	state.memory = a.renderMemory(memories, -1)
	state.skills = a.renderSkills(skills, -1)

	if state.over() {
		state.shrinkFiles()
	}
	if state.over() {
		state.shrinkMemory()
	}
	if state.over() {
		state.shrinkSkills()
	}
	text := state.join()
	if state.over() {
		text = clipWrappedTier(text, CapContext, "\n[truncated]", a.tokens)
	}
	return ContextResult{Text: text, Slots: state.slots()}, nil
}

func retrieve(ctx context.Context, in *ContextInput) ([]Memory, []string, error) {
	if in.Retriever == nil {
		return nil, nil, nil
	}
	memories, err := in.Retriever.Memories(ctx, in.Query)
	if err != nil {
		return nil, nil, fmt.Errorf("assemble: retrieve memories: %w", err)
	}
	skills, err := in.Retriever.Skills(ctx, in.Query)
	if err != nil {
		return nil, nil, fmt.Errorf("assemble: retrieve skills: %w", err)
	}
	return memories, skills, nil
}

type contextState struct {
	asm        *Assembler
	facts      string
	workspace  string
	filesBody  string
	marker     string
	untrusted  bool
	files      string
	memory     string
	skills     string
	memories   []Memory
	skillNames []string
}

func (s *contextState) join() string {
	parts := []string{s.facts, s.workspace, s.files, s.memory, s.skills}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

func (s *contextState) slots() ContextSlots {
	return ContextSlots{
		Facts: s.facts, Workspace: s.workspace, Files: s.files,
		Memory: s.memory, Skills: s.skills,
	}
}

func (s *contextState) over() bool { return s.asm.tokens.Count(s.join()) > CapContext }

// renderFiles renders the files slot within limit tokens. Untrusted files keep
// their wrapper whole: only the body is cut, so the closing tag always lands.
func (s *contextState) renderFiles(limit int) string {
	if s.filesBody == "" {
		return ""
	}
	if !s.untrusted {
		return clip("# Context files"+s.filesBody, limit, s.marker, s.asm.tokens)
	}
	return clipWrapped("# Context files\n"+untrustedWorkspace, s.filesBody,
		"\n</untrusted>", s.marker, limit, s.asm.tokens)
}

// shrinkFiles reduces the files tier from [CapFiles] towards [CapFilesMin]
// until the total fits, so AGENTS.md is the first casualty (SPEC §6).
func (s *contextState) shrinkFiles() {
	for limit := CapFiles - 10; limit >= CapFilesMin; limit -= 10 {
		s.files = s.renderFiles(limit)
		if !s.over() {
			return
		}
	}
	s.files = s.renderFiles(CapFilesMin)
}

// shrinkMemory drops non-pinned memories from the tail (lowest rank).
func (s *contextState) shrinkMemory() {
	nonPinned := 0
	for _, m := range s.memories {
		if !m.Pinned {
			nonPinned++
		}
	}
	for nonPinned > 0 {
		nonPinned--
		s.memory = s.asm.renderMemory(s.memories, nonPinned)
		if !s.over() {
			return
		}
	}
	s.memory = s.asm.renderMemory(s.memories, 0)
}

// shrinkSkills drops the least relevant skill (the tail) last (SPEC §6).
func (s *contextState) shrinkSkills() {
	n := min(len(s.skillNames), MaxSkills)
	for n > 0 {
		n--
		s.skills = s.asm.renderSkills(s.skillNames, n)
		if !s.over() {
			return
		}
	}
	s.skills = ""
}

// readContextFiles returns the files body: "\n# <path>\n<content>" per file
// that the sandbox opened, without the slot heading.
func (a *Assembler) readContextFiles(ctx context.Context, in *ContextInput) (string, error) {
	if len(in.Files) == 0 {
		return "", nil
	}
	if in.Reader == nil {
		return "", fmt.Errorf("assemble: context files: %w", ErrDeps)
	}
	var b strings.Builder
	for _, f := range in.Files {
		if f.Path == "" {
			return "", fmt.Errorf("assemble: %w: empty path", ErrContextPath)
		}
		body, err := a.readContextFile(ctx, in.Reader, f.Path)
		switch {
		case errors.Is(err, ErrUnavailable), errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return "", err
		}
		b.WriteString("\n# ")
		b.WriteString(a.promptPath(f.Path))
		b.WriteByte('\n')
		b.WriteString(body)
	}
	return b.String(), nil
}

func (a *Assembler) readContextFile(ctx context.Context, r FileReader, path string) (string, error) {
	rc, err := r.Open(ctx, path)
	if err != nil {
		return "", fmt.Errorf("assemble: open context file %s: %w", path, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(rc, MaxContextFileBytes))
	closeErr := rc.Close()
	if readErr != nil {
		return "", fmt.Errorf("assemble: read context file %s: %w", path, readErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("assemble: close context file %s: %w", path, closeErr)
	}
	return a.data(string(data)), nil
}

// renderMemory renders pinned entries under [CapPinned] first, then ranked
// entries under [CapMemory], cutting only at entry boundaries; the rest is
// counted in the "[+N memories: recall]" closer (SPEC §4.8). maxRanked < 0
// means every ranked entry is a candidate.
func (a *Assembler) renderMemory(mem []Memory, maxRanked int) string {
	var pinned, ranked []string
	for _, m := range mem {
		text := oneLine(a.data(m.Text))
		if text == "" {
			continue
		}
		if m.Pinned {
			pinned = append(pinned, "\n- "+text)
		} else {
			ranked = append(ranked, "\n- "+text)
		}
	}
	omitted := 0
	if maxRanked >= 0 && maxRanked < len(ranked) {
		omitted = len(ranked) - maxRanked
		ranked = ranked[:maxRanked]
	}
	var b strings.Builder
	b.WriteString("# Memory")
	omitted += a.appendEntries(&b, pinned, CapPinned)
	omitted += a.appendEntries(&b, ranked, CapMemory)
	if omitted > 0 {
		a.appendNote(&b, fmt.Sprintf("[+%d memories: recall]", omitted), CapMemory)
	}
	if b.Len() == len("# Memory") {
		return ""
	}
	return b.String()
}

// appendEntries appends entries in rank order while the builder stays within
// capTokens and returns how many did not fit. It never skips ahead to a
// smaller, lower-ranked entry.
func (a *Assembler) appendEntries(b *strings.Builder, entries []string, capTokens int) int {
	for i, e := range entries {
		if a.tokens.Count(b.String()+e) > capTokens {
			return len(entries) - i
		}
		b.WriteString(e)
	}
	return 0
}

// renderSkills renders at most [MaxSkills] one-line skills under [CapSkills].
func (a *Assembler) renderSkills(names []string, maxN int) string {
	limit := min(len(names), MaxSkills)
	if maxN >= 0 && maxN < limit {
		limit = maxN
	}
	var b strings.Builder
	b.WriteString("# Skills")
	for _, name := range names[:limit] {
		if line := oneLine(a.data(name)); line != "" {
			b.WriteString("\n- ")
			b.WriteString(strings.TrimPrefix(line, "- "))
		}
	}
	if b.Len() == len("# Skills") {
		return ""
	}
	return clip(b.String(), CapSkills, "\n[truncated]", a.tokens)
}

func (a *Assembler) appendNote(b *strings.Builder, note string, capTokens int) {
	if a.tokens.Count(b.String()+"\n"+note) <= capTokens {
		b.WriteString("\n")
		b.WriteString(note)
	}
}

// slot renders one heading+body slot and clips it to cap.
func slot(heading, body string, capTokens int, c Counter) string {
	if body == "" {
		return ""
	}
	return clip(heading+"\n"+body, capTokens, "\n[truncated]", c)
}

func (a *Assembler) fileMarker(files []ContextFile) string {
	if len(files) == 0 {
		return "\n[truncated]"
	}
	return "\n[truncated; read_file " + a.promptPath(files[0].Path) + "]"
}

// promptPath is the form of a context file path shown in the prompt: data
// ([Assembler.data]) with line breaks turned into spaces and control/format
// runes dropped ([lineRune]). The sandbox still opens the path as given.
func (a *Assembler) promptPath(p string) string { return strings.Map(lineRune, a.data(p)) }

// data prepares caller text for the context tier: scrubbed, with every
// <untrusted boundary escaped, so the only live tags in the frozen tier are
// the wrapper this package writes and [Assembler.Build] can validate it
// (REVIEW-F2-SEC4 ASM4-2/3).
func (a *Assembler) data(s string) string { return escapeUntrusted(a.scrub(s)) }
