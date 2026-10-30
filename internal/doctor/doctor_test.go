package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func doctorPaths(t *testing.T) (configDir, dataDir, configFile string) {
	t.Helper()
	root := t.TempDir()
	configDir = filepath.Join(root, "config")
	dataDir = filepath.Join(root, "data")
	for _, d := range []string{configDir, dataDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configFile = filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configFile, []byte("[provider]\nmodel = \"openai/gpt-test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configDir, dataDir, configFile
}

func healthyDB(t *testing.T, dataDir string) {
	t.Helper()
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "s", Origin: "headless", Cwd: "/w", Model: "openai/gpt-test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, "s", nil, ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "t", Content: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, o *Options) Report {
	t.Helper()
	o.Keyring = func(context.Context) (bool, error) { return false, nil }
	return Run(t.Context(), o)
}

func TestRunHealthy(t *testing.T) {
	cfgDir, dataDir, cfgFile := doctorPaths(t)
	healthyDB(t, dataDir)
	rep := run(t, &Options{ConfigDir: cfgDir, DataDir: dataDir, ConfigFile: cfgFile, ConfigFound: true, ProviderModel: "openai/gpt-test"})
	if rep.Exit != 0 {
		t.Fatalf("exit = %d, want 0\n%+v", rep.Exit, rep)
	}
	if len(rep.Checks) != 11 || rep.Summary.OK != 11 {
		t.Fatalf("checks = %d, ok = %d\n%+v", len(rep.Checks), rep.Summary.OK, rep)
	}
}

func TestRunInvalidConfig(t *testing.T) {
	cfgDir, dataDir, cfgFile := doctorPaths(t)
	rep := run(t, &Options{
		ConfigDir: cfgDir, DataDir: dataDir, ConfigFile: cfgFile, ConfigFound: true,
		ConfigErr: errors.New("config: loop.max_iterations: must be in 1..40"),
	})
	if rep.Exit != 1 || rep.Summary.Error != 1 {
		t.Fatalf("exit = %d, errors = %d, want 1/1", rep.Exit, rep.Summary.Error)
	}
	if rep.Checks[0].Message == "" || !strings.Contains(rep.Checks[0].Message, cfgFile) {
		t.Fatalf("config message lacks the file: %q", rep.Checks[0].Message)
	}
}

func TestRunFixesPermissions(t *testing.T) {
	cfgDir, dataDir, cfgFile := doctorPaths(t)
	if err := os.Chmod(cfgFile, 0o644); err != nil { // #nosec G302 -- deliberately wide to exercise the doctor check
		t.Fatal(err)
	}
	o := &Options{ConfigDir: cfgDir, DataDir: dataDir, ConfigFile: cfgFile, ConfigFound: true, ProviderModel: "openai/gpt-test"}
	if runtime.GOOS == "windows" {
		if rep := run(t, o); rep.Exit != 0 {
			t.Fatalf("Windows ACL check: exit = %d, want 0", rep.Exit)
		}
		return
	}
	if rep := run(t, o); rep.Exit != 1 || rep.Summary.Error == 0 {
		t.Fatalf("wide mode: exit = %d, want 1\n%+v", rep.Exit, rep)
	}
	o.Fix = true
	if rep := run(t, o); rep.Exit != 3 || rep.Summary.Fixed == 0 {
		t.Fatalf("fix: exit = %d, want 3\n%+v", rep.Exit, rep)
	}
	fi, err := os.Stat(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", fi.Mode().Perm())
	}
	if rep := run(t, o); rep.Exit != 0 {
		t.Fatalf("after fix: exit = %d, want 0", rep.Exit)
	}
}

func TestRunCorruptDB(t *testing.T) {
	cfgDir, dataDir, cfgFile := doctorPaths(t)
	if err := os.WriteFile(ctxdb.DBPath(dataDir), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := run(t, &Options{ConfigDir: cfgDir, DataDir: dataDir, ConfigFile: cfgFile, ConfigFound: true, ProviderModel: "openai/gpt-test"})
	if rep.Exit != 1 || rep.Summary.Error == 0 {
		t.Fatalf("exit = %d, want 1\n%+v", rep.Exit, rep)
	}
}

func TestReportJSON(t *testing.T) {
	cfgDir, dataDir, cfgFile := doctorPaths(t)
	rep := run(t, &Options{ConfigDir: cfgDir, DataDir: dataDir, ConfigFile: cfgFile, ConfigFound: true, ProviderModel: "openai/gpt-test"})
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["version"] != SchemaVersion {
		t.Fatalf("version = %v", got["version"])
	}
	checks, ok := got["checks"].([]any)
	if !ok || len(checks) != 11 {
		t.Fatalf("checks = %T %v", got["checks"], got["checks"])
	}
	first, ok := checks[0].(map[string]any)
	if !ok || first["id"] != "DOC-001" {
		t.Fatalf("first check = %v", checks[0])
	}
}

func TestRunDefaultsAndWarnings(t *testing.T) {
	root := t.TempDir()
	rep := run(t, &Options{
		ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data"),
		ConfigFile: filepath.Join(root, "config", "config.toml"),
	})
	if rep.Exit != 2 || rep.Summary.Warn != 1 {
		t.Fatalf("exit = %d, warn = %d, want 2/1\n%+v", rep.Exit, rep.Summary.Warn, rep)
	}
	if rep.Checks[0].Status != StatusOK || !strings.Contains(rep.Checks[0].Message, "not created") {
		t.Fatalf("config check = %+v", rep.Checks[0])
	}
}

func TestRunFixesMissingDirs(t *testing.T) {
	root := t.TempDir()
	cfgDir, dataDir := filepath.Join(root, "config"), filepath.Join(root, "data")
	rep := run(t, &Options{
		ConfigDir: cfgDir, DataDir: dataDir, ConfigFile: filepath.Join(cfgDir, "config.toml"),
		ProviderModel: "openai/gpt-test", Fix: true,
	})
	if rep.Exit != 3 || rep.Summary.Fixed < 2 {
		t.Fatalf("exit = %d, fixed = %d, want 3/2\n%+v", rep.Exit, rep.Summary.Fixed, rep)
	}
	if runtime.GOOS == "windows" {
		return // POSIX mode bits are not meaningful here
	}
	for _, d := range []string{cfgDir, dataDir} {
		fi, err := os.Stat(d)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %v, %v", d, fi.Mode().Perm(), err)
		}
	}
}

func TestRunSecretBackend(t *testing.T) {
	cfgDir, dataDir, cfgFile := doctorPaths(t)
	for _, tc := range []struct {
		name string
		ok   bool
		err  error
		want string
	}{
		{"keychain", true, nil, "OS keychain available"},
		{"fallback", false, nil, "file fallback (0600)"},
		{"error", false, errors.New("no dbus"), "file fallback (keychain:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &Options{ConfigDir: cfgDir, DataDir: dataDir, ConfigFile: cfgFile, ConfigFound: true, ProviderModel: "openai/gpt-test"}
			o.Keyring = func(context.Context) (bool, error) { return tc.ok, tc.err }
			rep := Run(t.Context(), o)
			if got := rep.Checks[7]; got.ID != "DOC-008" || got.Status != StatusOK || !strings.Contains(got.Message, tc.want) {
				t.Fatalf("secret check = %+v", got)
			}
		})
	}
}

func TestRunSecretsPermission(t *testing.T) {
	cfgDir, dataDir, cfgFile := doctorPaths(t)
	secret := filepath.Join(cfgDir, "secrets.enc")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil { // #nosec G306 -- deliberately wide fixture
		t.Fatal(err)
	}
	o := &Options{ConfigDir: cfgDir, DataDir: dataDir, ConfigFile: cfgFile, ConfigFound: true, ProviderModel: "openai/gpt-test"}
	if runtime.GOOS == "windows" {
		if rep := run(t, o); rep.Exit != 0 {
			t.Fatalf("Windows secret check exit = %d, want 0", rep.Exit)
		}
		return
	}
	if rep := run(t, o); rep.Exit != 1 {
		t.Fatalf("wide secret exit = %d, want 1", rep.Exit)
	}
	o.Fix = true
	if rep := run(t, o); rep.Exit != 3 {
		t.Fatalf("fix secret exit = %d, want 3", rep.Exit)
	}
	fi, err := os.Stat(secret)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode = %v, %v", fi.Mode().Perm(), err)
	}
}

func TestRunFixesDirPermissions(t *testing.T) {
	cfgDir, dataDir, cfgFile := doctorPaths(t)
	o := &Options{ConfigDir: cfgDir, DataDir: dataDir, ConfigFile: cfgFile, ConfigFound: true, ProviderModel: "openai/gpt-test"}
	if runtime.GOOS == "windows" {
		if rep := run(t, o); rep.Exit != 0 {
			t.Fatalf("Windows dir check exit = %d, want 0", rep.Exit)
		}
		return
	}
	for _, d := range []string{cfgDir, dataDir} {
		if err := os.Chmod(d, 0o755); err != nil { // #nosec G302 -- deliberately wide fixture
			t.Fatal(err)
		}
	}
	if rep := run(t, o); rep.Exit != 1 {
		t.Fatalf("wide dirs exit = %d, want 1", rep.Exit)
	}
	o.Fix = true
	if rep := run(t, o); rep.Exit != 3 {
		t.Fatalf("fix dirs exit = %d, want 3", rep.Exit)
	}
	for _, d := range []string{cfgDir, dataDir} {
		fi, err := os.Stat(d)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %v, %v", d, fi.Mode().Perm(), err)
		}
	}
}

// TestRunEmergencyStop: a paused daemon is a warning (exit 2) named by
// DOC-010, and a normal dir reports the stop off.
func TestRunEmergencyStop(t *testing.T) {
	cfgDir, dataDir, cfgFile := doctorPaths(t)
	healthyDB(t, dataDir)
	o := &Options{ConfigDir: cfgDir, DataDir: dataDir, ConfigFile: cfgFile, ConfigFound: true, ProviderModel: "openai/gpt-test"}
	rep := run(t, o)
	if got := rep.Checks[len(rep.Checks)-1]; got.ID != "DOC-010" || got.Status != StatusOK {
		t.Fatalf("normal stop check = %+v", got)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "estop.json"), []byte(`{"paused":true,"reason":"maintenance","since":"2026-10-30T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep = run(t, o)
	if rep.Exit != 2 {
		t.Fatalf("exit = %d, want 2\n%+v", rep.Exit, rep)
	}
	if got := rep.Checks[len(rep.Checks)-1]; got.ID != "DOC-010" || got.Status != StatusWarn || !strings.Contains(got.Message, "maintenance") {
		t.Fatalf("paused stop check = %+v", got)
	}
}

func TestCheckDiskStatus(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	for _, d := range []string{dataDir, filepath.Join(dataDir, "blobs", "aa"), filepath.Join(dataDir, "logs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []struct {
		path string
		size int
	}{{"damnbot.db", 1 << 20}, {"blobs/aa/sha", 2 << 20}, {"logs/acp.log", 3 << 20}} {
		if err := os.WriteFile(filepath.Join(dataDir, filepath.FromSlash(f.path)), make([]byte, f.size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const gib = int64(1) << 30
	for _, tc := range []struct {
		name  string
		free  int64
		total int64
		err   error
		want  Status
	}{
		{"below", 0, 10 * gib, nil, StatusError},
		{"near", 600 << 20, 10 * gib, nil, StatusWarn},
		{"ok", 2 * gib, 10 * gib, nil, StatusOK},
		{"probe", 0, 0, errors.New("no statfs"), StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) { assertDisk(t, dataDir, tc.name, tc.free, tc.total, tc.err, tc.want) })
	}
	missing := checkDisk(&Options{DataDir: filepath.Join(root, "absent"), Free: func(string) (int64, int64, error) {
		return 2 * gib, 10 * gib, nil
	}})
	if missing.Status != StatusOK || !strings.Contains(missing.Message, "not created") {
		t.Fatalf("missing data dir = %+v", missing)
	}
}

func assertDisk(t *testing.T, dataDir, name string, free, total int64, probe error, want Status) {
	t.Helper()
	c := checkDisk(&Options{DataDir: dataDir, Free: func(string) (int64, int64, error) { return free, total, probe }})
	if c.ID != "DOC-011" || c.Status != want {
		t.Fatalf("check = %+v, want %s", c, want)
	}
	if name != "probe" && !strings.Contains(c.Message, "db 1.0 MiB") {
		t.Errorf("sizes missing: %s", c.Message)
	}
	if name == "below" && !strings.Contains(c.Message, "low-water 512.0 MiB") {
		t.Errorf("low-water unreadable: %s", c.Message)
	}
}
