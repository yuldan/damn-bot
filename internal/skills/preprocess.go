package skills

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
)

// ErrInlineShellDisabled reports `!`command“ in a skill while inline shell is
// off (the default, SPEC §12 `skills.inline_shell`).
var ErrInlineShellDisabled = errors.New("skills: inline shell disabled")

// MaxInlineOutput bounds one inline-shell substitution (bytes).
const MaxInlineOutput = 4096

// PreprocessVars are the template values of SKILL-004.
type PreprocessVars struct {
	SkillDir, SkillName, ProjectDir, Home string
}

// ShellRunner executes one inline-shell command inside dir. The caller owns
// the timeout and any output cap; nil means inline shell is unavailable.
type ShellRunner interface {
	Run(ctx context.Context, dir, command string) (string, error)
}

// Preprocess expands `${...}` template variables and, when shell is non-nil,
// `!`command“ spans in a skill body. A shell span with shell == nil returns
// ErrInlineShellDisabled instead of leaking the literal command to the model.
func Preprocess(ctx context.Context, body string, v PreprocessVars, shell ShellRunner) (string, error) {
	body = expandTemplate(body, v)
	var b strings.Builder
	for i := 0; i < len(body); {
		if strings.HasPrefix(body[i:], "```") { // fenced code stays verbatim
			end := strings.Index(body[i+3:], "```")
			if end < 0 {
				b.WriteString(body[i:])
				break
			}
			n := i + 3 + end + 3
			b.WriteString(body[i:n])
			i = n
			continue
		}
		if body[i] != '`' {
			b.WriteByte(body[i])
			i++
			continue
		}
		end := strings.IndexByte(body[i+1:], '`')
		if end < 0 {
			b.WriteString(body[i:])
			break
		}
		content := body[i+1 : i+1+end]
		i = i + end + 2
		if !strings.HasPrefix(content, "!") {
			b.WriteByte('`')
			b.WriteString(content)
			b.WriteByte('`')
			continue
		}
		if shell == nil {
			return "", ErrInlineShellDisabled
		}
		out, err := shell.Run(ctx, v.SkillDir, strings.TrimPrefix(content, "!"))
		if err != nil {
			return "", fmt.Errorf("skills: inline shell: %w", err)
		}
		out = strings.TrimRight(out, "\r\n")
		if len(out) > MaxInlineOutput {
			out = out[:MaxInlineOutput] + "\n[inline output truncated]"
		}
		b.WriteString(out)
	}
	return b.String(), nil
}

// expandTemplate substitutes the documented variables; unknown ${...} stays
// literal so a skill can reference its own placeholders.
func expandTemplate(body string, v PreprocessVars) string {
	return strings.NewReplacer(
		"${SKILL_DIR}", v.SkillDir,
		"${SKILL_NAME}", v.SkillName,
		"${PROJECT_DIR}", v.ProjectDir,
		"${HOME}", v.Home,
		"${OS}", runtime.GOOS,
	).Replace(body)
}
