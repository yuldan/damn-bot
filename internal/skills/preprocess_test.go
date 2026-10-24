package skills

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeShell struct {
	dir, cmd string
	out      string
	err      error
}

func (f *fakeShell) Run(_ context.Context, dir, command string) (string, error) {
	f.dir, f.cmd = dir, command
	return f.out, f.err
}

// TestPreprocessTemplates: documented variables expand, unknown ones and
// fenced code stay literal.
func TestPreprocessTemplates(t *testing.T) {
	t.Parallel()
	body := "dir=${SKILL_DIR} name=${SKILL_NAME} project=${PROJECT_DIR} os=${OS} unknown=${NOPE}\n```\n${SKILL_DIR}\n```"
	got, err := Preprocess(context.Background(), body, PreprocessVars{
		SkillDir: "/s", SkillName: "lint", ProjectDir: "/w", Home: "/h",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dir=/s", "name=lint", "project=/w", "unknown=${NOPE}"} {
		if !strings.Contains(got, want) {
			t.Errorf("body lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "```\n/s\n```") {
		t.Errorf("fenced code changed:\n%s", got)
	}
}

// TestPreprocessInlineShell: inline shell is off by default and runs only
// through the injected runner when enabled.
func TestPreprocessInlineShell(t *testing.T) {
	t.Parallel()
	body := "host: `!hostname` and `code`"
	if _, err := Preprocess(context.Background(), body, PreprocessVars{SkillDir: "/s"}, nil); !errors.Is(err, ErrInlineShellDisabled) {
		t.Fatalf("disabled inline shell = %v, want ErrInlineShellDisabled", err)
	}
	sh := &fakeShell{out: "box\n"}
	got, err := Preprocess(context.Background(), body, PreprocessVars{SkillDir: "/s"}, sh)
	if err != nil {
		t.Fatal(err)
	}
	if sh.dir != "/s" || sh.cmd != "hostname" {
		t.Fatalf("runner got dir=%q cmd=%q", sh.dir, sh.cmd)
	}
	if !strings.Contains(got, "host: box and `code`") {
		t.Errorf("body = %q", got)
	}
}

// TestPreprocessInlineOutputCap: output is bounded with a marker.
func TestPreprocessInlineOutputCap(t *testing.T) {
	t.Parallel()
	sh := &fakeShell{out: strings.Repeat("x", MaxInlineOutput+10)}
	got, err := Preprocess(context.Background(), "`!big`", PreprocessVars{}, sh)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[inline output truncated]") || len(got) > MaxInlineOutput+40 {
		t.Fatalf("cap not applied (len %d)", len(got))
	}
}
