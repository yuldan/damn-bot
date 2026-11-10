//go:build !minimal

package desktopui

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/sandbox"
)

// resolveTarget validates one desktop_preview target. A URL must be https; a
// file must resolve inside the session workspace. The resolved path is what
// the surface receives, so a symlink cannot widen the tool's reach.
func resolveTarget(ctx context.Context, cwd, target string) (string, error) {
	if strings.Contains(target, "://") {
		u, err := url.Parse(target)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return "", invalid("only https URLs can be previewed")
		}
		return u.String(), nil
	}
	if cwd == "" {
		return "", denied("no workspace in this session")
	}
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{cwd}})
	if err != nil {
		return "", internalErr(err)
	}
	p := target
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	verdict, resolved, err := sb.Check(ctx, p, sandbox.OpRead)
	if err != nil {
		if errors.Is(err, sandbox.ErrDeniedPath) {
			return "", &host.Error{Code: "denied_path", Message: "preview target is denied"}
		}
		return "", internalErr(err)
	}
	if verdict != sandbox.Inside {
		return "", &host.Error{Code: "outside_workspace", Message: "preview target is outside the workspace"}
	}
	return resolved, nil
}
