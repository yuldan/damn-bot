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
	CapFacts      = 60
	CapWorkspace  = 150
	CapFiles      = 400
	CapFilesMin   = 200
	CapMemory     = 300
	CapPinned     = 150
	CapSkills     = 150
	CapContext    = 1100
	CapSeparators = 40
	MaxSkills     = 3
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

// ContextInput is the caller-provided context tier.
type ContextInput struct {
	Facts     string
	Workspace string
	Files     []ContextFile
	Memory    []Memory
	Skills    []string
	Reader    FileReader
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

// Context renders the context tier. It returns [ErrDeps] when files are
// requested without a reader and [ErrContextPath] for an empty path; a sandbox
// refusal (ErrUnavailable) or a missing file omits that file.
func (a *Assembler) Context(ctx context.Context, in *ContextInput) (ContextResult, error) {
	if in == nil {
		return ContextResult{}, nil
	}
	filesFull, err := a.readContextFiles(ctx, in)
	if err != nil {
		return ContextResult{}, err
	}
	state := contextState{
		asm:        a,
		facts:      slot("# Environment", a.scrub(in.Facts), CapFacts, a.tokens),
		workspace:  slot("# Workspace", a.scrub(in.Workspace), CapWorkspace, a.tokens),
		filesFull:  filesFull,
		memories:   in.Memory,
		skillNames: in.Skills,
	}
	marker := a.fileMarker(in.Files)
	state.files = clip(filesFull, CapFiles, marker, a.tokens)
	state.memory = a.renderMemory(in.Memory, -1)
	state.skills = a.renderSkills(in.Skills, -1)

	if state.over() {
		state.shrinkFiles(marker)
	}
	if state.over() {
		state.shrinkMemory()
	}
	if state.over() {
		state.shrinkSkills()
	}
	text := state.join()
	if state.over() {
		text = clip(text, CapContext, "\n[truncated]", a.tokens)
	}
	return ContextResult{Text: text, Slots: state.slots()}, nil
}

type contextState struct {
	asm        *Assembler
	facts      string
	workspace  string
	filesFull  string
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

// shrinkFiles reduces the files tier from [CapFiles] towards [CapFilesMin]
// until the total fits, so AGENTS.md is the first casualty (SPEC §6).
func (s *contextState) shrinkFiles(marker string) {
	for limit := CapFiles - 10; limit >= CapFilesMin; limit -= 10 {
		s.files = clip(s.filesFull, limit, marker, s.asm.tokens)
		if !s.over() {
			return
		}
	}
	s.files = clip(s.filesFull, CapFilesMin, marker, s.asm.tokens)
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

func (a *Assembler) readContextFiles(ctx context.Context, in *ContextInput) (string, error) {
	if len(in.Files) == 0 {
		return "", nil
	}
	if in.Reader == nil {
		return "", fmt.Errorf("assemble: context files: %w", ErrDeps)
	}
	var b strings.Builder
	b.WriteString("# Context files")
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
	if b.Len() == len("# Context files") {
		return "", nil
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
	return a.scrub(string(data)), nil
}

// renderMemory renders pinned entries under [CapPinned] first, then non-pinned
// entries under [CapMemory]. maxNonPinned < 0 means all non-pinned entries.
func (a *Assembler) renderMemory(mem []Memory, maxNonPinned int) string {
	var pinned, rest []string
	for _, m := range mem {
		if m.Text == "" {
			continue
		}
		line := "\n- " + a.scrub(m.Text)
		if m.Pinned {
			pinned = append(pinned, line)
		} else {
			rest = append(rest, line)
		}
	}
	if len(pinned) == 0 && len(rest) == 0 {
		return ""
	}
	var b strings.Builder
	if len(pinned) > 0 {
		b.WriteString(clip("# Memory"+strings.Join(pinned, ""), CapPinned, "\n[truncated]", a.tokens))
	}
	limited := rest
	if maxNonPinned >= 0 && maxNonPinned < len(rest) {
		limited = rest[:maxNonPinned]
	}
	omitted := len(rest) - len(limited)
	if b.Len() == 0 {
		b.WriteString("# Memory")
	}
	for i, line := range limited {
		if a.tokens.Count(b.String()+line) > CapMemory {
			omitted += len(limited) - i
			break
		}
		b.WriteString(line)
	}
	a.appendNote(&b, fmt.Sprintf("[+%d memories: recall]", omitted), CapMemory)
	if b.String() == "# Memory" {
		return ""
	}
	return b.String()
}

// renderSkills renders at most [MaxSkills] skills under [CapSkills].
func (a *Assembler) renderSkills(names []string, maxN int) string {
	limit := min(len(names), MaxSkills)
	if maxN >= 0 && maxN < limit {
		limit = maxN
	}
	if limit == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Skills")
	for _, name := range names[:limit] {
		b.WriteString("\n- ")
		b.WriteString(a.scrub(name))
	}
	return clip(b.String(), CapSkills, "\n[truncated]", a.tokens)
}

func (a *Assembler) appendNote(b *strings.Builder, note string, capTokens int) {
	if note == "" {
		return
	}
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

// promptPath is the form of a context file path shown in the prompt: scrubbed,
// with line breaks turned into spaces and control/format runes dropped
// ([lineRune]). The sandbox still opens the path as given.
func (a *Assembler) promptPath(p string) string { return strings.Map(lineRune, a.scrub(p)) }
