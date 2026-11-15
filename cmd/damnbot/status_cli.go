//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/gateway"
)

// statusComponent is one row of `damnbot status` (CLI-022/OPS-012). State is
// ok, warn, or fail; the detail is always a concrete observation, never a
// promise.
type statusComponent struct {
	Component string `json:"component"`
	State     string `json:"state"`
	Detail    string `json:"detail"`
}

// statusSession is the session/model/token/context block SLASH-038 adds to
// /status. Found is false when the route has no session yet.
type statusSession struct {
	Found        bool   `json:"found"`
	ID           string `json:"id,omitempty"`
	Model        string `json:"model,omitempty"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	ContextUsed  int64  `json:"context_used"`
	ContextLimit int64  `json:"context_limit"`
}

// statusReport is the one aggregation the status surfaces render.
type statusReport struct {
	Profile    string            `json:"profile"`
	Components []statusComponent `json:"components"`
	Session    *statusSession    `json:"session,omitempty"`
}

func statusUsage(w io.Writer) {
	say(w, "usage: damnbot status [--json] [--session ID]\n")
}

// runStatus implements `damnbot status` (CLI-022, OPS-012). It is read-only:
// the database opens with ReadOnly=true and the command writes no state.
func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "machine-readable components")
	session := fs.String("session", "", "session id (default: newest visible session)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		say(stderr, "status: unexpected argument %q\n", cleanArg(fs.Arg(0)))
		return 2
	}
	loader, err := config.New(config.OS())
	if err != nil {
		say(stderr, "status: %s\n", cleanLine(err.Error()))
		return 1
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		say(stderr, "status: %s\n", cleanLine(err.Error()))
		return 1
	}
	cfg, found, cfgErr := loadStatusConfig(loader, paths)
	report := collectStatus(context.Background(), &statusOptions{
		Paths: paths, Config: cfg, ConfigFound: found, ConfigErr: cfgErr, SessionID: *session,
	})
	if *asJSON {
		b, merr := json.MarshalIndent(report, "", "  ")
		if merr != nil {
			return 1
		}
		if code := say(stdout, "%s\n", b); code != 0 {
			return code
		}
		if statusFailed(report) {
			return 1
		}
		return 0
	}
	say(stdout, "damnbot status · profile %s\n", report.Profile)
	for _, c := range report.Components {
		say(stdout, "  %-5s %-9s %s\n", c.State, c.Component, c.Detail)
	}
	if s := report.Session; s != nil && s.Found {
		say(stdout, "session %s · model %s · tokens %d/%d · context %d/%d\n",
			s.ID, s.Model, s.InputTokens, s.OutputTokens, s.ContextUsed, s.ContextLimit)
	}
	if statusFailed(report) {
		return 1
	}
	return 0
}

// statusFailed reports whether any component row failed; both the text and
// JSON renderings exit 1 on it.
func statusFailed(rep statusReport) bool {
	for _, c := range rep.Components {
		if c.State == "fail" {
			return true
		}
	}
	return false
}

// loadStatusConfig mirrors doctor: a missing file is a warning, an invalid one
// is a failure, and both still report the remaining components.
func loadStatusConfig(loader *config.Loader, paths config.Paths) (config.Config, bool, error) {
	cfg, err := loader.Load(paths.ConfigFile())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return config.Default(), false, nil
	case err != nil:
		return config.Default(), true, fmt.Errorf("config: %w", err)
	}
	if eerr := loader.Effective(&cfg); eerr != nil {
		return config.Default(), true, fmt.Errorf("config: %w", eerr)
	}
	return cfg, true, nil
}

// statusOptions is the aggregator input, injectable so the gateway runtime can
// reuse the session reader against its live store.
type statusOptions struct {
	Paths       config.Paths
	Config      config.Config
	ConfigFound bool
	ConfigErr   error
	SessionID   string
}

// collectStatus builds every component from one snapshot. A component error is
// reported in place; it never hides the components after it.
func collectStatus(ctx context.Context, o *statusOptions) statusReport {
	rep := statusReport{Profile: activeProfileName()}
	rep.Components = append(rep.Components, statusVersion(), statusConfig(o), statusData(o.Paths.DataDir))
	db, dbErr := openStatusDB(ctx, o.Paths.DataDir)
	if dbErr != nil {
		rep.Components = append(rep.Components, statusComponent{"database", "warn", cleanLine(dbErr.Error())},
			statusComponent{"cron", "warn", "database unavailable"})
	} else {
		defer func() { _ = db.Close() }() //nolint:contextcheck,errcheck // ctxdb.Store.Close has no context parameter
		rep.Components = append(rep.Components, statusDatabase(ctx, db), statusCron(ctx, db))
		if s, ok := readStatusSession(ctx, db, o.SessionID, int64(o.Config.Loop.TurnMaxInputTok)); ok {
			rep.Session = &s
		}
	}
	rep.Components = append(rep.Components, statusGateway(ctx, o.Paths.DataDir), statusSkills(o.Paths.ConfigDir))
	return rep
}

func statusVersion() statusComponent {
	return statusComponent{"version", "ok", fmt.Sprintf("%s %s/%s go%s", version, runtime.GOOS, runtime.GOARCH, runtime.Version())}
}

func statusConfig(o *statusOptions) statusComponent {
	path := o.Paths.ConfigFile()
	switch {
	case o.ConfigErr != nil:
		return statusComponent{"config", "fail", path + ": " + cleanLine(o.ConfigErr.Error())}
	case !o.ConfigFound:
		return statusComponent{"config", "warn", path + " not found; defaults"}
	case o.Config.Provider.Model == "":
		return statusComponent{"config", "warn", path + "; no provider.model"}
	default:
		return statusComponent{"config", "ok", path + "; model " + o.Config.Provider.Model}
	}
}

func statusData(dir string) statusComponent {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return statusComponent{"data", "warn", dir + " missing (created on first run)"}
	case err != nil:
		return statusComponent{"data", "fail", cleanLine(err.Error())}
	case !info.IsDir():
		return statusComponent{"data", "fail", dir + " is not a directory"}
	default:
		return statusComponent{"data", "ok", dir}
	}
}

// openStatusDB opens the store read-only. A missing file is not an error: the
// database has simply not been created yet.
func openStatusDB(ctx context.Context, dataDir string) (*ctxdb.Store, error) {
	if _, err := os.Stat(ctxdb.DBPath(dataDir)); err != nil { //nolint:gosec // data dir comes from config paths, not request input
		return nil, fmt.Errorf("not initialized: %w", err)
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "cli", ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	return db, nil
}

func statusDatabase(ctx context.Context, db *ctxdb.Store) statusComponent {
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		return statusComponent{"database", "fail", cleanLine(err.Error())}
	}
	n, err := db.Count(ctx, `SELECT count(*) FROM sessions WHERE id <> '__system__'`)
	if err != nil {
		return statusComponent{"database", "fail", cleanLine(err.Error())}
	}
	return statusComponent{"database", "ok", fmt.Sprintf("schema %d; %d sessions", version, n)}
}

func statusCron(ctx context.Context, db *ctxdb.Store) statusComponent {
	total, err := db.Count(ctx, `SELECT count(*) FROM cron_jobs`)
	if err != nil {
		return statusComponent{"cron", "warn", cleanLine(err.Error())}
	}
	enabled, err := db.Count(ctx, `SELECT count(*) FROM cron_jobs WHERE enabled = 1`)
	if err != nil {
		return statusComponent{"cron", "warn", cleanLine(err.Error())}
	}
	return statusComponent{"cron", "ok", fmt.Sprintf("%d enabled / %d jobs", enabled, total)}
}

// statusGateway reads the emergency-stop file and the serve owner line without
// taking the lock: a status probe must never create or disturb serve state.
func statusGateway(ctx context.Context, dataDir string) statusComponent {
	est, err := gateway.Estop{Dir: dataDir}.Status(ctx)
	if err != nil {
		return statusComponent{"gateway", "warn", "estop state unreadable (fail closed)"}
	}
	state := "emergency stop off"
	if est.Paused {
		state = "emergency stop on"
		if est.Reason != "" {
			state += " (" + cleanLine(est.Reason) + ")"
		}
	}
	lock := filepath.Join(dataDir, "run", "serve.lock")
	if _, serr := os.Stat(lock); errors.Is(serr, fs.ErrNotExist) {
		return statusComponent{"gateway", "ok", state + "; serve not started"}
	}
	return statusComponent{"gateway", "ok", state + "; serve " + lockOwner(lock)}
}

func statusSkills(configDir string) statusComponent {
	entries, err := os.ReadDir(filepath.Join(configDir, "skills"))
	if errors.Is(err, fs.ErrNotExist) {
		return statusComponent{"skills", "ok", "0 local skills"}
	}
	if err != nil {
		return statusComponent{"skills", "warn", cleanLine(err.Error())}
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, serr := os.Stat(filepath.Join(configDir, "skills", e.Name(), "SKILL.md"))
		if serr == nil && info.Mode().IsRegular() {
			n++
		}
	}
	return statusComponent{"skills", "ok", fmt.Sprintf("%d local skills", n)}
}

// readStatusSession is the shared session reader of the CLI and gateway
// surfaces. An empty id selects the newest visible session.
func readStatusSession(ctx context.Context, db *ctxdb.Store, sessionID string, limit int64) (statusSession, bool) {
	s := statusSession{ContextLimit: limit}
	if sessionID == "" {
		if !newestStatusSession(ctx, db, &s.ID) {
			return statusSession{}, false
		}
	} else {
		s.ID = sessionID
	}
	info, err := db.SessionInfo(ctx, s.ID)
	if err != nil {
		return statusSession{}, false
	}
	s.Found, s.Model = true, info.Model
	if err := db.Rows(ctx, `SELECT COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0)
		FROM usage_ledger WHERE session_id = ?1`, []any{s.ID}, []any{&s.InputTokens, &s.OutputTokens}, func() error { return nil }); err != nil {
		return statusSession{}, false
	}
	if err := db.Rows(ctx, `SELECT COALESCE((SELECT input_tokens FROM usage_ledger
		WHERE session_id = ?1 AND purpose = 'turn' ORDER BY id DESC LIMIT 1), 0)`,
		[]any{s.ID}, []any{&s.ContextUsed}, func() error { return nil }); err != nil {
		return statusSession{}, false
	}
	return s, true
}

func newestStatusSession(ctx context.Context, db *ctxdb.Store, id *string) bool {
	found := false
	err := db.Rows(ctx, `SELECT s.id FROM sessions s
		WHERE s.id <> '__system__'
		  AND NOT EXISTS (SELECT 1 FROM session_labels l WHERE l.session_id = s.id AND l.key = 'purge_state')
		ORDER BY s.created_at DESC, s.id LIMIT 1`, nil, []any{id}, func() error { found = true; return nil })
	return err == nil && found
}

// activeProfileName is the profile shown in the header; the global flag sets it
// before dispatch.
func activeProfileName() string {
	if p := activeProfile(); p != "" {
		return p
	}
	return "default"
}
