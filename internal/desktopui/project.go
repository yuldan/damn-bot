//go:build !minimal

package desktopui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/sandbox"
)

const (
	projectsKey = "projects"
	setupKey    = "setup-beat"
)

// Project is one named Desktop Project (a workspace with folders).
type Project struct {
	Name    string   `json:"name"`
	Root    string   `json:"root"`
	Folders []string `json:"folders"`
}

type projectState struct {
	Current string    `json:"current,omitempty"`
	Items   []Project `json:"items"`
}

func (s *projectState) find(name string) bool {
	for i := range s.Items {
		if s.Items[i].Name == name {
			return true
		}
	}
	return false
}

func loadProjects(ctx context.Context, store host.Store) projectState {
	var st projectState
	if store == nil {
		return st
	}
	raw, ok, err := store.Get(ctx, []byte(projectsKey))
	if err != nil || !ok {
		return projectState{}
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return projectState{}
	}
	return st
}

func saveProjects(ctx context.Context, store host.Store, st projectState) error {
	if store == nil {
		return nil
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode projects: %w", err)
	}
	if err := store.Put(ctx, []byte(projectsKey), raw); err != nil {
		return fmt.Errorf("save projects: %w", err)
	}
	return nil
}

// projectNameOK accepts the same id charset as tool and project names.
func projectNameOK(name string) error {
	if name == "" || len(name) > 64 {
		return invalid("project name must be 1..64 characters")
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !ok {
			return invalid("project name must match [a-z0-9_-]+")
		}
		if i == 0 && (r == '_' || r == '-') {
			return invalid("project name must start with a letter or digit")
		}
	}
	return nil
}

// resolveInside resolves p through the session sandbox root cwd and returns
// its canonical path. Anything outside, or any deny_paths hit, is refused.
func resolveInside(ctx context.Context, cwd, p string) (string, error) {
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{cwd}})
	if err != nil {
		return "", internalErr(err)
	}
	target := p
	if !filepath.IsAbs(target) {
		target = filepath.Join(cwd, target)
	}
	verdict, resolved, err := sb.Check(ctx, target, sandbox.OpRead)
	if err != nil {
		if errors.Is(err, sandbox.ErrDeniedPath) {
			return "", &host.Error{Code: "denied_path", Message: "project path is denied"}
		}
		return "", internalErr(err)
	}
	if verdict != sandbox.Inside {
		return "", &host.Error{Code: "outside_workspace", Message: "project path is outside the workspace"}
	}
	return resolved, nil
}

// within reports whether dir is root or below it, using path components.
func within(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
