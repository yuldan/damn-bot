// Package doctor runs the read-only F3 health checks of `damnbot doctor`
// (SELF-HEALING §8, GAP-OPS-06). It touches no network and writes only when
// the caller explicitly asks to fix a permission bit.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/gateway"
)

// SchemaVersion is the machine-readable doctor document (GAP-OPS-06).
const SchemaVersion = "damnbot.doctor/v1"

// Status is one check verdict.
type Status string

// Check verdicts. `fixed` is only produced with Options.Fix.
const (
	StatusOK    Status = "ok"
	StatusWarn  Status = "warn"
	StatusError Status = "error"
	StatusFixed Status = "fixed"
)

// Check is one stable DOC-NNN finding.
type Check struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  Status `json:"status"`
	Message string `json:"message"`
}

// Summary counts the verdicts.
type Summary struct {
	OK    int `json:"ok"`
	Warn  int `json:"warn"`
	Error int `json:"error"`
	Fixed int `json:"fixed"`
}

// Report is the `damnbot.doctor/v1` document.
type Report struct {
	Version string  `json:"version"`
	Checks  []Check `json:"checks"`
	Summary Summary `json:"summary"`
	Exit    int     `json:"exit"`
}

// Options are the inputs resolved by the CLI. No config package import keeps
// the module boundary narrow and lets a broken config still be reported.
type Options struct {
	ConfigDir     string
	DataDir       string
	ConfigFile    string
	ConfigFound   bool
	ConfigErr     error
	ProviderModel string
	// ProviderWarnings are the SEC-24 findings the composition root computed
	// from the catalog and [[providers.custom]] (override, non-default
	// base_url, unpriced model). A non-empty list makes DOC-009 a warning.
	ProviderWarnings []string
	credentialPoolOptions
	Fix bool
	// Keyring probes the OS keychain; nil uses the real one with a 2 s cap.
	Keyring func(context.Context) (bool, error)
	// Free reports free and total bytes; nil uses the platform statfs probe.
	Free func(path string) (free, total int64, err error)
}

// Run executes every check and applies the SELF-HEALING §8 exit contract:
// 0 ok, 1 error/critical remains, 2 warning only, 3 fixed and clean.
func Run(ctx context.Context, o *Options) Report {
	if o.Keyring == nil {
		o.Keyring = probeKeyring
	}
	r := Report{Version: SchemaVersion}
	r.Checks = []Check{
		checkConfig(o),
		checkPath("DOC-002", "config dir", o.ConfigDir, true, o.Fix),
		checkPath("DOC-003", "data dir", o.DataDir, true, o.Fix),
		checkPath("DOC-004", "config file", o.ConfigFile, false, o.Fix),
		checkPath("DOC-005", "secrets file", o.ConfigDir+"/secrets.enc", false, o.Fix),
		checkDatabase(ctx, o),
		checkEngine(ctx),
		checkSecret(ctx, o),
		checkProvider(o),
		checkDisk(o),
		checkEmergencyStop(ctx, o),
	}
	r.Checks = append(r.Checks, credentialPoolChecks(o)...)
	return r.finish()
}

func (r *Report) finish() Report {
	for _, c := range r.Checks {
		switch c.Status {
		case StatusOK:
			r.Summary.OK++
		case StatusWarn:
			r.Summary.Warn++
		case StatusError:
			r.Summary.Error++
		case StatusFixed:
			r.Summary.Fixed++
		}
	}
	switch {
	case r.Summary.Error > 0:
		r.Exit = 1
	case r.Summary.Warn > 0:
		r.Exit = 2
	case r.Summary.Fixed > 0:
		r.Exit = 3
	}
	return *r
}

// checkEmergencyStop reports the process-wide stop (OPS-037).
func checkEmergencyStop(ctx context.Context, o *Options) Check {
	st, err := gateway.Estop{Dir: o.DataDir}.Status(ctx)
	if err != nil || st.Paused {
		reason := st.Reason
		if reason == "" {
			reason = "state unreadable"
		}
		return Check{
			"DOC-010", "emergency stop", StatusWarn,
			fmt.Sprintf("paused (%s); run `damnbot resume`", reason),
		}
	}
	return Check{"DOC-010", "emergency stop", StatusOK, "off"}
}

func checkConfig(o *Options) Check {
	if o.ConfigErr != nil {
		return Check{"DOC-001", "config", StatusError, fmt.Sprintf("%s: %v", o.ConfigFile, o.ConfigErr)}
	}
	if !o.ConfigFound {
		return Check{"DOC-001", "config", StatusOK, "not created; SPEC defaults in use"}
	}
	return Check{"DOC-001", "config", StatusOK, o.ConfigFile + " valid"}
}

// checkPath enforces 0700 directories and 0600 files (SELF-HEALING §4). Mode
// bits are not portable on Windows, where the ACL check is reported as ok.
func checkPath(id, name, path string, dir, fix bool) Check {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if !dir {
			return Check{id, name, StatusOK, "not created"}
		}
		if !fix {
			return Check{id, name, StatusOK, "not created"}
		}
		if merr := os.MkdirAll(path, 0o700); merr != nil {
			return Check{id, name, StatusError, fmt.Sprintf("%s: %v", path, merr)}
		}
		return Check{id, name, StatusFixed, path + " created 0700"}
	}
	if err != nil {
		return Check{id, name, StatusError, fmt.Sprintf("%s: %v", path, err)}
	}
	if runtime.GOOS == "windows" {
		return Check{id, name, StatusOK, path + " (ACL; mode bits not checked)"}
	}
	want := fs.FileMode(0o600)
	if dir {
		want = 0o700
	}
	if fi.Mode().Perm()&0o077 == 0 && (!dir || fi.Mode().Perm()&0o200 != 0) {
		return Check{id, name, StatusOK, fmt.Sprintf("%s %04o", path, fi.Mode().Perm())}
	}
	if !fix {
		return Check{id, name, StatusError, fmt.Sprintf("%s %04o, want %04o", path, fi.Mode().Perm(), want)}
	}
	if cerr := os.Chmod(path, want); cerr != nil {
		return Check{id, name, StatusError, fmt.Sprintf("%s: %v", path, cerr)}
	}
	return Check{id, name, StatusFixed, fmt.Sprintf("%s %04o → %04o", path, fi.Mode().Perm(), want)}
}

func checkDatabase(ctx context.Context, o *Options) Check {
	path := ctxdb.DBPath(o.DataDir)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return Check{"DOC-006", "database", StatusOK, "not created"}
	} else if err != nil {
		return Check{"DOC-006", "database", StatusError, fmt.Sprintf("%s: %v", path, err)}
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: o.DataDir, Kind: "doctor", ReadOnly: true})
	if err != nil {
		return Check{"DOC-006", "database", StatusError, fmt.Sprintf("%s: %v", path, err)}
	}
	res, ierr := db.IntegrityCheck(ctx)
	cerr := db.CloseContext(ctx)
	if ierr == nil {
		ierr = cerr
	}
	switch {
	case ierr != nil:
		return Check{"DOC-006", "database", StatusError, fmt.Sprintf("%s: %v", path, ierr)}
	case res != "ok":
		return Check{"DOC-006", "database", StatusError, fmt.Sprintf("%s: integrity_check: %s", path, res)}
	default:
		return Check{"DOC-006", "database", StatusOK, path + " integrity ok"}
	}
}

func checkEngine(ctx context.Context) Check {
	info, err := ctxdb.ProbeEngine(ctx)
	if err != nil {
		return Check{"DOC-007", "sqlite", StatusError, err.Error()}
	}
	if !info.Supported() {
		return Check{"DOC-007", "sqlite", StatusError, "engine lacks REL-04 capabilities"}
	}
	return Check{"DOC-007", "sqlite", StatusOK, "sqlite " + info.Version}
}

func checkSecret(ctx context.Context, o *Options) Check {
	ok, err := o.Keyring(ctx)
	switch {
	case err != nil:
		return Check{"DOC-008", "secret backend", StatusOK, fmt.Sprintf("file fallback (keychain: %v)", err)}
	case ok:
		return Check{"DOC-008", "secret backend", StatusOK, "OS keychain available"}
	default:
		return Check{"DOC-008", "secret backend", StatusOK, "file fallback (0600)"}
	}
}

func checkProvider(o *Options) Check {
	if len(o.ProviderWarnings) > 0 {
		msg := strings.Join(o.ProviderWarnings, "; ")
		if o.ProviderModel != "" {
			msg = o.ProviderModel + ": " + msg
		}
		return Check{"DOC-009", "provider", StatusWarn, msg}
	}
	if o.ProviderModel == "" {
		return Check{"DOC-009", "provider", StatusWarn, "provider.model is empty; run `damnbot setup`"}
	}
	return Check{"DOC-009", "provider", StatusOK, o.ProviderModel + " (no network probe)"}
}

// checkDisk reports OPS-033/SELF-HEALING §4 data-dir disk status: free space
// against the readable low-water mark plus the space used by the DB, blobs and
// logs. A missing data dir or an unavailable probe is not a failure.
func checkDisk(o *Options) Check {
	const id, name = "DOC-011", "disk"
	fi, err := os.Stat(o.DataDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Check{id, name, StatusOK, "data dir not created; no space report"}
	case err != nil:
		return Check{id, name, StatusError, fmt.Sprintf("%s: %v", o.DataDir, err)}
	case !fi.IsDir():
		return Check{id, name, StatusError, o.DataDir + " is not a directory"}
	}
	db := int64(0)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if fi, ferr := os.Stat(filepath.Join(o.DataDir, "damnbot.db") + suffix); ferr == nil && fi.Mode().IsRegular() {
			db += fi.Size()
		}
	}
	used := fmt.Sprintf("db %s · blobs %s · logs %s", sizeText(db),
		sizeText(dirSize(filepath.Join(o.DataDir, "blobs"))), sizeText(dirSize(filepath.Join(o.DataDir, "logs"))))
	probe := o.Free
	if probe == nil {
		probe = diskFree
	}
	free, total, err := probe(o.DataDir)
	if err != nil {
		return Check{id, name, StatusOK, used + "; free space unknown (" + err.Error() + ")"}
	}
	if total <= 0 {
		return Check{id, name, StatusOK, used + "; free space unknown (zero capacity)"}
	}
	lowWater := min(int64(1)<<30, total/20)
	clear := lowWater + 256<<20
	msg := fmt.Sprintf("%s free of %s (low-water %s, clears at %s); %s",
		sizeText(free), sizeText(total), sizeText(lowWater), sizeText(clear), used)
	switch {
	case free < lowWater:
		return Check{id, name, StatusError, "disk below low-water: " + msg}
	case free < clear:
		return Check{id, name, StatusWarn, "disk near low-water: " + msg}
	default:
		return Check{id, name, StatusOK, msg}
	}
}

// dirSize sums regular files below dir; a missing dir is zero.
func dirSize(dir string) int64 {
	var n int64
	if err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, ferr := d.Info(); ferr == nil {
				n += fi.Size()
			}
		}
		return nil
	}); err != nil {
		return n
	}
	return n
}

// sizeText renders bytes for the operator (binary units).
func sizeText(n int64) string {
	const unit = int64(1024)
	switch {
	case n >= unit*unit*unit:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(unit*unit*unit))
	case n >= unit*unit:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(unit*unit))
	case n >= unit:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(unit))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// probeKeyring caps the OS keychain call; a hung secret service must never
// turn a quick doctor run into a hang. ErrNotFound means the backend works.
func probeKeyring(ctx context.Context) (bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ch := make(chan error, 1)
	go func() {
		_, err := keyring.Get("damnbot", "doctor-probe")
		ch <- err
	}()
	select {
	case err := <-ch:
		switch {
		case err == nil, errors.Is(err, keyring.ErrNotFound):
			return true, nil
		case errors.Is(err, keyring.ErrUnsupportedPlatform):
			return false, nil
		default:
			return false, err
		}
	case <-cctx.Done():
		return false, fmt.Errorf("keychain probe: %w", cctx.Err())
	}
}
