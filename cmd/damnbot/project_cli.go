//go:build !minimal

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type f8Project struct {
	Name     string   `json:"name"`
	Root     string   `json:"root"`
	Folders  []string `json:"folders"`
	Primary  string   `json:"primary,omitempty"`
	Board    string   `json:"board,omitempty"`
	Archived bool     `json:"archived,omitempty"`
}

type f8ProjectState struct {
	Current string      `json:"current,omitempty"`
	Items   []f8Project `json:"items"`
}

//nolint:dupl // runner load/store identik dengan runPets; boilerplate sengaja eksplisit per surface.
func runProject(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("project", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return f8ExitUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		projectUsage(stderr)
		return f8ExitUsage
	}
	dir, err := f8Dir(*dataDir)
	if err != nil {
		return f8CmdFail(stderr, "project", err)
	}
	st := f8ProjectState{}
	if lerr := f8Load(f8StorePath(dir, "projects.json"), &st); lerr != nil {
		return f8CmdFail(stderr, "project", lerr)
	}
	changed, err := projectCommand(rest, &st, stdout, stderr)
	if err != nil {
		if errors.Is(err, errF8Usage) {
			return f8ExitUsage
		}
		return f8CmdFail(stderr, "project", err)
	}
	if changed {
		if err := f8Save(f8StorePath(dir, "projects.json"), &st); err != nil {
			return f8CmdFail(stderr, "project", err)
		}
	}
	return f8ExitOK
}

func projectUsage(w io.Writer) {
	say(w, "usage: damnbot project [--data-dir DIR] create|list|show|add-folder|remove-folder|rename|set-primary|use|archive|restore|bind-board ...\n")
}

//nolint:gocognit,gocyclo,funlen // satu cabang switch per subcommand CLI-030; memecahnya menyebarkan state machine.
func projectCommand(rest []string, st *f8ProjectState, stdout, stderr io.Writer) (bool, error) {
	switch rest[0] {
	case "create":
		return projectCreate(rest[1:], st, stdout, stderr)
	case "list":
		for _, a := range rest[1:] {
			if a != "--all" {
				return false, errF8Usage
			}
		}
		all := slices.Contains(rest[1:], "--all")
		for _, p := range st.sorted() {
			if p.Archived && !all {
				continue
			}
			mark, arch := " ", ""
			if st.Current == p.Name {
				mark = "*"
			}
			if p.Archived {
				arch = " archived=true"
			}
			say(stdout, "%s%s folders=%d primary=%s%s\n", mark, p.Name, len(p.Folders), p.Primary, arch)
		}
		return false, nil
	case "show":
		p := st.find(arg(rest, 1))
		if p == nil {
			return false, fmt.Errorf("project %q not found", arg(rest, 1))
		}
		say(stdout, "name: %s\nroot: %s\nprimary: %s\nboard: %s\narchived: %t\n", p.Name, p.Root, p.Primary, p.Board, p.Archived)
		for _, f := range p.Folders {
			say(stdout, "folder: %s\n", f)
		}
		return false, nil
	case "add-folder", "remove-folder":
		return projectFolder(rest[0] == "add-folder", rest[1:], st, stdout, stderr)
	case "rename":
		p := st.find(arg(rest, 1))
		if p == nil {
			return false, fmt.Errorf("project %q not found", arg(rest, 1))
		}
		if err := projectNameOK(arg(rest, 2)); err != nil {
			return false, err
		}
		if st.find(arg(rest, 2)) != nil {
			return false, fmt.Errorf("project %q already exists", arg(rest, 2))
		}
		old := p.Name
		if st.Current == old {
			st.Current = arg(rest, 2)
		}
		p.Name = arg(rest, 2)
		st.sort()
		say(stdout, "renamed %s -> %s\n", old, p.Name)
		return true, nil
	case "set-primary":
		p := st.find(arg(rest, 1))
		if p == nil {
			return false, fmt.Errorf("project %q not found", arg(rest, 1))
		}
		abs, err := projectDir(arg(rest, 2))
		if err != nil {
			return false, err
		}
		if !slices.Contains(p.Folders, abs) {
			return false, f8BadArg("folder %s is not part of project %q", abs, p.Name)
		}
		if p.Primary == abs {
			return false, nil
		}
		p.Primary = abs
		say(stdout, "primary %s = %s\n", p.Name, abs)
		return true, nil
	case "use":
		name := arg(rest, 1)
		if name == "off" {
			changed := st.Current != ""
			st.Current = ""
			say(stdout, "current project off\n")
			return changed, nil
		}
		p := st.find(name)
		if p == nil {
			return false, fmt.Errorf("project %q not found", name)
		}
		if p.Archived {
			return false, fmt.Errorf("project %q is archived", p.Name)
		}
		if st.Current == p.Name {
			return false, nil
		}
		st.Current = p.Name
		say(stdout, "current project %s\n", p.Name)
		return true, nil
	case "archive", "restore":
		p := st.find(arg(rest, 1))
		if p == nil {
			return false, fmt.Errorf("project %q not found", arg(rest, 1))
		}
		want := rest[0] == "archive"
		if p.Archived == want {
			return false, nil
		}
		p.Archived = want
		if want && st.Current == p.Name {
			st.Current = ""
		}
		verb := "archived"
		if !want {
			verb = "restored"
		}
		say(stdout, "%s %s\n", verb, p.Name)
		return true, nil
	case "bind-board":
		p := st.find(arg(rest, 1))
		if p == nil {
			return false, fmt.Errorf("project %q not found", arg(rest, 1))
		}
		if arg(rest, 2) == "" || len(arg(rest, 2)) > 64 {
			return false, f8BadArg("board must be 1..64 characters")
		}
		if p.Board == arg(rest, 2) {
			return false, nil
		}
		p.Board = arg(rest, 2)
		say(stdout, "bound %s board=%s\n", p.Name, p.Board)
		return true, nil
	default:
		say(stderr, "project: unknown subcommand %q\n", cleanArg(rest[0]))
		projectUsage(stderr)
		return false, errF8Usage
	}
}

//nolint:gocognit,gocyclo // validasi cabang mengikuti flag SPEC CLI-030; tetap satu alur create.
func projectCreate(args []string, st *f8ProjectState, stdout, stderr io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, errF8Usage
	}
	name, root := args[0], ""
	var folders []string
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--root":
			if i+1 >= len(args) {
				return false, errF8Usage
			}
			root = args[i+1]
			i++
		case "--folder":
			if i+1 >= len(args) {
				return false, errF8Usage
			}
			folders = append(folders, args[i+1])
			i++
		default:
			return false, errF8Usage
		}
	}
	if root == "" {
		say(stderr, "usage: damnbot project create <name> --root DIR [--folder DIR]...\n")
		return false, errF8Usage
	}
	if err := projectNameOK(name); err != nil {
		return false, err
	}
	if st.find(name) != nil {
		return false, fmt.Errorf("project %q already exists", name)
	}
	abs, err := projectDir(root)
	if err != nil {
		return false, err
	}
	if len(folders) == 0 {
		folders = []string{abs}
	}
	list := make([]string, 0, len(folders))
	for _, f := range folders {
		dir, err := projectDir(f)
		if err != nil {
			return false, err
		}
		if err := projectInside(abs, dir); err != nil {
			return false, err
		}
		if !slices.Contains(list, dir) {
			list = append(list, dir)
		}
	}
	st.Items = append(st.Items, f8Project{Name: name, Root: abs, Folders: list, Primary: list[0]})
	st.sort()
	say(stdout, "created project %s root=%s folders=%d\n", name, abs, len(list))
	return true, nil
}

func projectFolder(add bool, args []string, st *f8ProjectState, stdout, stderr io.Writer) (bool, error) {
	verb := "add-folder"
	if !add {
		verb = "remove-folder"
	}
	if len(args) != 2 {
		say(stderr, "usage: damnbot project %s <name> DIR\n", verb)
		return false, errF8Usage
	}
	p := st.find(args[0])
	if p == nil {
		return false, fmt.Errorf("project %q not found", args[0])
	}
	abs, err := projectDir(args[1])
	if err != nil {
		return false, err
	}
	if add {
		if err := projectInside(p.Root, abs); err != nil {
			return false, err
		}
		if slices.Contains(p.Folders, abs) {
			say(stdout, "already listed: %s\n", abs)
			return false, nil
		}
		p.Folders = append(p.Folders, abs)
		say(stdout, "added folder %s to %s\n", abs, p.Name)
		return true, nil
	}
	i := slices.Index(p.Folders, abs)
	if i < 0 {
		say(stdout, "not listed: %s\n", abs)
		return false, nil
	}
	if len(p.Folders) == 1 {
		return false, fmt.Errorf("project %q would lose its only folder", p.Name)
	}
	p.Folders = slices.Delete(p.Folders, i, i+1)
	if p.Primary == abs {
		p.Primary = p.Folders[0]
	}
	say(stdout, "removed folder %s from %s\n", abs, p.Name)
	return true, nil
}

func (st *f8ProjectState) find(name string) *f8Project {
	for i := range st.Items {
		if st.Items[i].Name == name {
			return &st.Items[i]
		}
	}
	return nil
}

func (st *f8ProjectState) sort() {
	slices.SortFunc(st.Items, func(a, b f8Project) int { return strings.Compare(a.Name, b.Name) })
}

func (st *f8ProjectState) sorted() []f8Project {
	out := slices.Clone(st.Items)
	slices.SortFunc(out, func(a, b f8Project) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func projectNameOK(name string) error {
	if name == "" || len(name) > 64 {
		return f8BadArg("project name must match [a-z0-9][a-z0-9_-]{0,63}")
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !ok || i == 0 && (r == '-' || r == '_') {
			return f8BadArg("project name %q must match [a-z0-9][a-z0-9_-]{0,63}", name)
		}
	}
	return nil
}

func projectDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", cleanArg(path), err)
	}
	fi, err := os.Stat(abs) // #nosec G304 -- operator workspace
	if err != nil {
		return "", fmt.Errorf("workspace %s: %w", abs, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("workspace %s is not a directory", abs)
	}
	return abs, nil
}

func projectInside(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("folder %s: %w", path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return f8BadArg("folder %s is outside workspace %s", path, root)
	}
	return nil
}
