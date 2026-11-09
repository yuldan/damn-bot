//go:build !minimal

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/skills"
	"github.com/damnbot/damnbot/internal/skills/hub"
)

// skillsHubCommand implements the F8 network half of `damnbot skills`:
// search/list/install/approve/sync against the four sources. It is behind
// !minimal and every network action is explicit; content is downloaded into
// quarantine and never executed.
func skillsHubCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		skillsHubUsage(stderr)
		return 2
	}
	rest, owner, optional, err := splitHubFlags(args)
	if err != nil {
		say(stderr, "skills hub: %v\n", err)
		return 2
	}
	a, err := parseSkillArgs(rest)
	if err != nil {
		say(stderr, "skills hub: %v\n", err)
		return 2
	}
	o := &hubOptions{skillArgs: a, owner: owner, optional: optional}
	if len(o.pos) == 0 {
		skillsHubUsage(stderr)
		return 2
	}
	ctx := context.Background()
	env, err := openHub(ctx, o)
	if err != nil {
		say(stderr, "skills hub: %v\n", err)
		return 1
	}
	defer env.close(ctx)
	switch o.pos[0] {
	case "search", "list":
		return env.search(ctx, o, stdout, stderr)
	case "install":
		return env.install(ctx, o, stdout, stderr)
	case "approve":
		return env.approve(ctx, o, stdout, stderr)
	case "sync":
		return env.sync(ctx, o, stdout, stderr)
	default:
		say(stderr, "skills hub: subcommand %q tidak dikenal\n", skillsArg(o.pos[0]))
		skillsHubUsage(stderr)
		return 2
	}
}

func skillsHubUsage(w io.Writer) {
	say(w, "pemakaian: damnbot skills hub search <source> [query] [--limit N]\n")
	say(w, "           damnbot skills hub list <source>\n")
	say(w, "           damnbot skills hub install <source> <name>\n")
	say(w, "           damnbot skills hub approve <memory-id> [--owner <name>]\n")
	say(w, "           damnbot skills hub sync <source> [--optional]\n")
	say(w, "source: official|github|clawhub|skills.sh; owner juga dari DAMNBOT_SKILLS_OWNER\n")
}

// hubOptions is the shared skillArgs plus the two hub-only flags. It lives in
// the !minimal file so the shared parser stays at its budgeted shape.
type hubOptions struct {
	skillArgs
	owner    string
	optional bool
}

// splitHubFlags removes --owner/--optional from args before the shared parser
// runs; the rest is parsed exactly like every other skills subcommand.
func splitHubFlags(args []string) (rest []string, owner string, optional bool, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--optional":
			optional = true
		case args[i] == "--owner":
			if i+1 >= len(args) {
				return nil, "", false, fmt.Errorf("--owner needs a value")
			}
			i++
			owner = args[i]
		case strings.HasPrefix(args[i], "--owner="):
			owner = strings.TrimPrefix(args[i], "--owner=")
			if owner == "" {
				return nil, "", false, fmt.Errorf("--owner needs a value")
			}
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, owner, optional, nil
}

// hubEnv is one command's composition: the store-backed service, the hub, and
// the close hook of the SQLite handle.
type hubEnv struct {
	h         *hub.Hub
	db        *ctxdb.Store
	configDir string
}

func openHub(ctx context.Context, o *hubOptions) (*hubEnv, error) {
	red := redact.New(redact.Options{})
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: o.dataDir, Kind: "cli"})
	if err != nil {
		return nil, fmt.Errorf("open ctxdb: %w", err)
	}
	svc, err := newSkillServiceScan(ctx, db, red, hubScanner(red))
	if err != nil {
		_ = db.CloseContext(ctx) //nolint:errcheck // the composition error is the actionable one
		return nil, fmt.Errorf("skills hub: service: %w", err)
	}
	client := netx.New(netx.Config{TestDial: skillsHubTestDial()}).Client("refs", netx.Tool).WithSameHostRedirects(3)
	fetcher, err := hub.NewFetcher(client)
	if err != nil {
		_ = db.CloseContext(ctx) //nolint:errcheck // the composition error is the actionable one
		return nil, fmt.Errorf("skills hub: fetcher: %w", err)
	}
	h, err := hub.New(hub.Config{
		Fetcher: fetcher, Service: svc, Sources: skillsHubSources(),
		AllowTestSources: skillsHubTestSources(), Owner: hubOwner(o.owner),
	})
	if err != nil {
		_ = db.CloseContext(ctx) //nolint:errcheck // the composition error is the actionable one
		return nil, fmt.Errorf("skills hub: %w", err)
	}
	return &hubEnv{h: h, db: db, configDir: o.configDir}, nil
}

func (e *hubEnv) close(ctx context.Context) {
	if err := e.db.CloseContext(ctx); err != nil {
		// A close failure after the command committed is not actionable through
		// the exit code; sqlite already fsynced the successful writes.
		slog.WarnContext(ctx, "skills hub: close db", slog.String("error", err.Error()))
	}
}

// hubScanner composes the shared memory scanner with the hub's grep scan and
// keeps the higher severity, so prompt injection/secret findings and the
// hub's shell/traversal/exfil families both gate the quarantine decision.
func hubScanner(red *redact.Redactor) skills.Scanner {
	return func(sk *skills.Skill) (string, string, error) {
		scan := memory.ScanText(red, sk.Description+"\n"+sk.Body)
		base, jerr := scan.JSON()
		if jerr != nil {
			return "", "", fmt.Errorf("skills hub: memory scan: %w", jerr)
		}
		sev, result, err := hub.ThreatScan(sk)
		if err != nil {
			return "", "", fmt.Errorf("skills hub: threat scan: %w", err)
		}
		if hubSeverityRank(sev) > hubSeverityRank(string(scan.Severity)) {
			return sev, result, nil
		}
		return string(scan.Severity), base, nil
	}
}

func hubSeverityRank(sev string) int {
	switch sev {
	case "high":
		return 2
	case "medium":
		return 1
	}
	return 0
}

// hubOwner is the explicit approver identity: --owner wins over the
// environment. An empty value leaves approval closed (hub.ErrNotOwner).
func hubOwner(owner string) string {
	if owner != "" {
		return owner
	}
	return config.OS().Getenv("DAMNBOT_SKILLS_OWNER")
}

func (e *hubEnv) source(pos []string) (hub.Source, int, error) {
	if len(pos) < 2 {
		return "", 0, fmt.Errorf("sumber wajib: official|github|clawhub|skills.sh")
	}
	src, err := hub.ParseSource(pos[1])
	if err != nil {
		return "", 0, fmt.Errorf("skills hub: %w", err)
	}
	return src, 2, nil
}

func (e *hubEnv) search(ctx context.Context, o *hubOptions, stdout, stderr io.Writer) int {
	src, next, err := e.source(o.pos)
	if err != nil {
		say(stderr, "skills hub %s: %v\n", o.pos[0], err)
		return 2
	}
	query := ""
	if len(o.pos) > next {
		query = o.pos[next]
	}
	entries, err := e.h.Search(ctx, src, query, o.limit)
	if err != nil {
		say(stderr, "skills hub %s: %v\n", o.pos[0], err)
		return 1
	}
	for i := range entries {
		entry := &entries[i]
		say(stdout, "%s\t%s\t%s\t%s\n", entry.Name, entry.Version, entry.ToS.Risk, skillsArg(entry.Description))
	}
	return 0
}

func (e *hubEnv) install(ctx context.Context, o *hubOptions, stdout, stderr io.Writer) int {
	src, next, err := e.source(o.pos)
	if err != nil || len(o.pos) != next+1 {
		if err != nil {
			say(stderr, "skills hub install: %v\n", err)
		}
		skillsHubUsage(stderr)
		return 2
	}
	name := o.pos[next]
	target := filepath.Join(e.configDir, "skills")
	path, err := e.h.Install(ctx, src, name, target)
	if err == nil {
		say(stdout, "installed %s (%s)\n", name, path)
		return 0
	}
	if !errors.Is(err, hub.ErrNotApproved) {
		say(stderr, "skills hub install: %v\n", err)
		return 1
	}
	idx, ierr := e.h.Index(ctx, src)
	if ierr != nil {
		say(stderr, "skills hub install: %v\n", ierr)
		return 1
	}
	entry, ferr := idx.Find(name)
	if ferr != nil {
		say(stderr, "skills hub install: %v\n", ferr)
		return 1
	}
	inst, derr := e.h.Download(ctx, src, &entry)
	if derr != nil {
		say(stderr, "skills hub install: %v\n", derr)
		return 1
	}
	if inst.Status == skills.StatusRejected {
		say(stderr, "rejected %s: temuan ancaman high; tidak masuk antrean review\n", name)
		return 1
	}
	say(stdout, "pending %s memory=%d status=%s source=%s\n", name, inst.MemoryID, inst.Status, src)
	say(stdout, "setujui: damnbot skills hub approve %d --owner <nama>\n", inst.MemoryID)
	return 0
}

func (e *hubEnv) approve(ctx context.Context, o *hubOptions, stdout, stderr io.Writer) int {
	if len(o.pos) != 2 {
		skillsHubUsage(stderr)
		return 2
	}
	id, err := strconv.ParseInt(o.pos[1], 10, 64)
	if err != nil || id <= 0 {
		say(stderr, "skills hub approve: memory id %q tidak valid\n", skillsArg(o.pos[1]))
		return 2
	}
	if err := e.h.Approve(ctx, hubOwner(o.owner), id); err != nil {
		say(stderr, "skills hub approve: %v\n", err)
		return 1
	}
	say(stdout, "approved memory=%d\n", id)
	return 0
}

func (e *hubEnv) sync(ctx context.Context, o *hubOptions, stdout, stderr io.Writer) int {
	src, next, err := e.source(o.pos)
	if err != nil || len(o.pos) != next {
		if err != nil {
			say(stderr, "skills hub sync: %v\n", err)
		}
		skillsHubUsage(stderr)
		return 2
	}
	var res hub.SyncResult
	if o.optional {
		res, err = e.h.SyncOptional(ctx, src)
	} else {
		res, err = e.h.Sync(ctx, src)
	}
	say(stdout, "synced source=%s installed=%d rejected=%d failed=%d\n",
		src, len(res.Installed), len(res.Rejected), len(res.Failed))
	if err != nil {
		say(stderr, "skills hub sync: %v\n", err)
		return 1
	}
	return 0
}
