package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
)

// Scanner classifies a skill for the quarantine gate (SPEC §14.4). It
// returns the severity ("low"/"medium"/"high") and the scan_result JSON. The
// composition root injects memory.ScanText; a nil scanner fails closed for
// untrusted installs.
type Scanner func(s *Skill) (severity string, result string, err error)

// Store is the persisted side of skill retrieval (E-09, ≤ 5 methods). Index
// upserts a trusted skill; Quarantine stores an untrusted one and returns its
// memory (review) id and index id; Approve records the user decision; Search
// returns approved skills only; Status reads the quarantine state.
type Store interface {
	Index(ctx context.Context, s *Skill) (int64, error)
	Quarantine(ctx context.Context, s *Skill, scan string, rejected bool) (memoryID, skillID int64, err error)
	Approve(ctx context.Context, memoryID int64) error
	Search(ctx context.Context, text string, k int) ([]Skill, error)
	Status(ctx context.Context, memoryID int64) (Status, error)
}

// Config builds a [Service].
type Config struct {
	Scan Scanner
	// Redact is the session redactor (redact.Redactor.String). It is required:
	// skill bodies/descriptions/paths are redacted before any storage or FTS
	// write (JN3-4).
	Redact func(string) string
	// Known and Files feed [Lint] (ADR-133): Known reports an installed skill
	// name, Files opens the skill directory (os.Root FS). Nil fails closed:
	// a skill referencing another skill or a script is invalid.
	Known func(name string) bool
	Files func(sk *Skill) (fs.FS, error)
}

// Service is the skill install/retrieval API.
type Service struct {
	store  Store
	scan   Scanner
	redact func(string) string
	known  func(string) bool
	files  func(*Skill) (fs.FS, error)
}

// New validates the store dependency.
func New(st Store, cfg Config) (*Service, error) {
	if st == nil || cfg.Redact == nil {
		return nil, fmt.Errorf("%w: nil store/redactor", ErrInvalid)
	}
	return &Service{store: st, scan: cfg.Scan, redact: cfg.Redact, known: cfg.Known, files: cfg.Files}, nil
}

// Install is the outcome of one install.
type Install struct {
	SkillID  int64
	MemoryID int64
	Status   Status
}

// Install lints the skill, then indexes a trusted skill (user/builtin) or
// quarantines every other source. A broken reference is invalid for every
// source; a high finding (scan or credential_access lint) rejects an
// untrusted skill immediately and never offers it, and a high lint finding
// makes a trusted skill invalid; low/medium is stored as scanned until a
// user approves it (P10).
func (s *Service) Install(ctx context.Context, sk *Skill) (Install, error) {
	if sk == nil {
		return Install{}, fmt.Errorf("%w: nil skill", ErrInvalid)
	}
	if err := sk.Validate(); err != nil {
		return Install{}, err
	}
	findings, err := s.lint(sk)
	if err != nil {
		return Install{}, err
	}
	// Scan the raw input (JN3-7), then redact before any storage write.
	rejected := false
	result := ""
	if s.scan != nil {
		severity, scanResult, serr := s.scan(sk)
		if serr != nil {
			return Install{}, fmt.Errorf("skills: scan: %w", serr)
		}
		rejected, result = severity == "high", scanResult
	} else if !sk.Source.Trusted() {
		return Install{}, fmt.Errorf("%w: untrusted source %q", ErrScan, sk.Source)
	}
	result, lintHigh, err := mergeScan(result, findings)
	if err != nil {
		return Install{}, err
	}
	if lintHigh && sk.Source.Trusted() {
		return Install{}, fmt.Errorf("%w: credential_access", ErrInvalid)
	}
	rejected = rejected || lintHigh
	stored := *sk
	stored.Description = s.redact(sk.Description)
	stored.Body = s.redact(sk.Body)
	stored.Path = s.redact(sk.Path)
	if stored.Source.Trusted() {
		stored.MemoryID = 0
		id, ierr := s.store.Index(ctx, &stored)
		if ierr != nil {
			return Install{}, fmt.Errorf("skills: index: %w", ierr)
		}
		return Install{SkillID: id, Status: StatusApproved}, nil
	}
	memoryID, skillID, err := s.store.Quarantine(ctx, &stored, result, rejected)
	if err != nil {
		return Install{}, fmt.Errorf("skills: quarantine: %w", err)
	}
	status := StatusScanned
	if rejected {
		status = StatusRejected
	}
	return Install{SkillID: skillID, MemoryID: memoryID, Status: status}, nil
}

// lint runs [Lint] with the configured resolvers.
func (s *Service) lint(sk *Skill) ([]Finding, error) {
	env := LintEnv{Known: s.known}
	if s.files != nil {
		files, err := s.files(sk)
		if err != nil {
			return nil, fmt.Errorf("%w: skill directory: %w", ErrInvalid, err)
		}
		env.Files = files
	}
	return Lint(sk, env)
}

// mergeScan appends lint findings to the scanner's scan_result JSON
// ({"severity","findings"}, memory.Scan) and reports a high lint finding.
func mergeScan(result string, findings []Finding) (string, bool, error) {
	if len(findings) == 0 {
		return result, false, nil
	}
	sc := struct {
		Severity string    `json:"severity"`
		Findings []Finding `json:"findings,omitempty"`
	}{Severity: "low"}
	if result != "" {
		if err := json.Unmarshal([]byte(result), &sc); err != nil {
			return "", false, fmt.Errorf("%w: scan result: %w", ErrScan, err)
		}
	}
	rank := map[string]int{"medium": 1, "high": 2}
	high := false
	for _, f := range findings {
		sc.Findings = append(sc.Findings, f)
		high = high || f.Severity == "high"
		if rank[f.Severity] > rank[sc.Severity] {
			sc.Severity = f.Severity
		}
	}
	b, err := json.Marshal(sc)
	if err != nil {
		return "", false, fmt.Errorf("skills: scan result: %w", err)
	}
	return string(b), high, nil
}

// Approve records the user approval of a quarantined skill.
func (s *Service) Approve(ctx context.Context, memoryID int64) error {
	if memoryID <= 0 {
		return fmt.Errorf("%w: memory id", ErrInvalid)
	}
	if err := s.store.Approve(ctx, memoryID); err != nil {
		return fmt.Errorf("skills: approve: %w", err)
	}
	return nil
}

// Status reads the quarantine state of a proposed skill.
func (s *Service) Status(ctx context.Context, memoryID int64) (Status, error) {
	if memoryID <= 0 {
		return "", fmt.Errorf("%w: memory id", ErrInvalid)
	}
	st, err := s.store.Status(ctx, memoryID)
	if err != nil {
		return "", fmt.Errorf("skills: status: %w", err)
	}
	return st, nil
}

// Retrieve returns at most k approved skills matching text, best first. At
// most [MaxOffer] skills ever enter the prompt (SPEC §6).
func (s *Service) Retrieve(ctx context.Context, text string, k int) ([]Skill, error) {
	if k <= 0 || k > MaxOffer {
		k = MaxOffer
	}
	list, err := s.store.Search(ctx, text, k)
	if err != nil {
		return nil, fmt.Errorf("skills: search: %w", err)
	}
	return list, nil
}

// Offer is the context-tier rendering: at most three prompt lines
// (≤ [MaxPromptDesc] description characters each). No "MUST load" phrasing
// is ever emitted (TestPromptLint).
func (s *Service) Offer(ctx context.Context, text string) ([]string, error) {
	list, err := s.Retrieve(ctx, text, MaxOffer)
	if err != nil {
		return nil, err
	}
	lines := make([]string, len(list))
	for i := range list {
		lines[i] = PromptLine(&list[i])
	}
	return lines, nil
}
