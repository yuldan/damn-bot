package skills

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// LedgerFile is the append-only skill ledger below the data dir (SKILL-019).
const LedgerFile = "skills-ledger.jsonl"

// MaxLedgerEntries bounds the ledger; exceeding it fails closed instead of
// silently dropping provenance.
const MaxLedgerEntries = 50000

// ErrLedger is a malformed or oversized skill ledger.
var ErrLedger = errors.New("skills: ledger error")

// LedgerEntry records one skill change (install/index/reject/approve).
type LedgerEntry struct {
	At       int64  `json:"at"`
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Path     string `json:"path"`
	Source   string `json:"source"`
	Action   string `json:"action"`
	MemoryID int64  `json:"memory_id,omitempty"`
}

// ReadLedger reads every entry from <dataDir>/skills-ledger.jsonl; a missing
// file is an empty ledger. A malformed line fails closed.
func ReadLedger(dataDir string) (entries []LedgerEntry, outErr error) {
	root, err := os.OpenRoot(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLedger, err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil && outErr == nil {
			outErr = fmt.Errorf("%w: close data dir: %w", ErrLedger, cerr)
		}
	}()
	f, oerr := root.Open(LedgerFile)
	if errors.Is(oerr, os.ErrNotExist) {
		return nil, nil
	}
	if oerr != nil {
		return nil, fmt.Errorf("%w: %w", ErrLedger, oerr)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && outErr == nil {
			outErr = fmt.Errorf("%w: close ledger: %w", ErrLedger, cerr)
		}
	}()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e LedgerEntry
		if derr := json.Unmarshal([]byte(line), &e); derr != nil {
			return nil, fmt.Errorf("%w: %w", ErrLedger, derr)
		}
		entries = append(entries, e)
		if len(entries) > MaxLedgerEntries {
			return nil, fmt.Errorf("%w: more than %d entries", ErrLedger, MaxLedgerEntries)
		}
	}
	if serr := sc.Err(); serr != nil {
		return nil, fmt.Errorf("%w: %w", ErrLedger, serr)
	}
	return entries, nil
}

// AppendLedger appends one entry (0600, O_APPEND) after checking the cap.
// Duplicate entries are the caller's responsibility (see HasLedgerEntry).
func AppendLedger(dataDir string, e *LedgerEntry) error {
	if e.Name == "" || e.SHA256 == "" || e.Action == "" {
		return fmt.Errorf("%w: incomplete entry", ErrLedger)
	}
	if entries, err := ReadLedger(dataDir); err != nil {
		return err
	} else if len(entries) >= MaxLedgerEntries {
		return fmt.Errorf("%w: full (%d entries)", ErrLedger, len(entries))
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLedger, err)
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLedger, err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("%w: close data dir: %w", ErrLedger, cerr))
		}
	}()
	f, err := root.OpenFile(LedgerFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLedger, err)
	}
	if _, werr := f.Write(append(line, '\n')); werr != nil {
		if cerr := f.Close(); cerr != nil {
			return fmt.Errorf("%w: %w", ErrLedger, errors.Join(werr, cerr))
		}
		return fmt.Errorf("%w: %w", ErrLedger, werr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("%w: %w", ErrLedger, cerr)
	}
	return nil
}

// HasLedgerEntry reports whether the same skill change is already recorded.
func HasLedgerEntry(entries []LedgerEntry, e *LedgerEntry) bool {
	for _, x := range slices.Backward(entries) {
		if x.Name == e.Name && x.SHA256 == e.SHA256 && x.Action == e.Action {
			return true
		}
	}
	return false
}

// Provenance returns the latest ledger entry for a skill name.
func Provenance(entries []LedgerEntry, name string) (LedgerEntry, bool) {
	for _, e := range slices.Backward(entries) {
		if e.Name == name {
			return e, true
		}
	}
	return LedgerEntry{}, false
}

// LedgerPath is the on-disk ledger location (for messages/tests).
func LedgerPath(dataDir string) string { return filepath.Join(dataDir, LedgerFile) }
