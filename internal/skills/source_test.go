package skills

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestPreprocessLoadedTrustedOnly (ADR-168): SKILL-004 preprocessing expands
// templates for every source, but the inline shell runs only for a trusted
// source with the config flag on; an untrusted source or a disabled flag fails
// closed with ErrInlineShellDisabled instead of leaking the literal command.
func TestPreprocessLoadedTrustedOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	spy := &shellSpy{}
	opts := LoadOptions{InlineShell: true, Shell: spy, Vars: PreprocessVars{ProjectDir: dir, Home: dir}}

	trusted := Skill{Name: "run", Body: "value: `!echo hi`", Source: SourceUser, Path: filepath.Join(dir, "SKILL.md")}
	if err := preprocessLoaded(ctx, &trusted, opts); err != nil {
		t.Fatalf("trusted inline shell: %v", err)
	}
	if !strings.Contains(trusted.Body, "value: ran") || spy.calls != 1 {
		t.Fatalf("body = %q, calls = %d", trusted.Body, spy.calls)
	}
	// Negative control: the same body from an untrusted root must not run.
	untrusted := Skill{Name: "imp", Body: "value: `!echo hi`", Source: SourceImport, Path: filepath.Join(dir, "SKILL.md")}
	if err := preprocessLoaded(ctx, &untrusted, opts); !errors.Is(err, ErrInlineShellDisabled) {
		t.Fatalf("untrusted inline shell = %v, want ErrInlineShellDisabled", err)
	}
	if spy.calls != 1 {
		t.Fatalf("untrusted source ran the shell: calls = %d", spy.calls)
	}
	// Negative control: flag off means no inline shell even for a trusted skill.
	off := Skill{Name: "off", Body: "value: `!echo hi`", Source: SourceUser, Path: filepath.Join(dir, "SKILL.md")}
	if err := preprocessLoaded(ctx, &off, LoadOptions{Shell: spy, Vars: opts.Vars}); !errors.Is(err, ErrInlineShellDisabled) {
		t.Fatalf("disabled inline shell = %v, want ErrInlineShellDisabled", err)
	}
	if spy.calls != 1 {
		t.Fatalf("disabled inline shell still ran: calls = %d", spy.calls)
	}
	// Template expansion still happens without the shell.
	plain := Skill{Name: "tpl", Body: "dir: ${SKILL_DIR}", Source: SourceImport, Path: filepath.Join(dir, "SKILL.md")}
	if err := preprocessLoaded(ctx, &plain, opts); err != nil {
		t.Fatalf("template without shell: %v", err)
	}
	if !strings.Contains(plain.Body, dir) {
		t.Fatalf("template not expanded: %q", plain.Body)
	}
}

// shellSpy records how often the inline shell ran.
type shellSpy struct{ calls int }

func (s *shellSpy) Run(context.Context, string, string) (string, error) {
	s.calls++
	return "ran", nil
}
