package batch

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

// Runner defaults; Parallel is capped so one batch cannot exhaust sockets.
const (
	DefaultParallel = 4
	MaxParallel     = 32
	DefaultMaxChars = 2000
)

// Closed errors, compared with errors.Is.
var (
	ErrNoOutDir    = errors.New("batch: output directory required")
	ErrBadItem     = errors.New("batch: invalid item")
	ErrBadParallel = errors.New("batch: parallel out of range")
	ErrCanceled    = errors.New("batch: canceled before the item started")
)
var itemIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Item is one JSONL line. Model/Route override the batch defaults per item.
type Item struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	System string `json:"system,omitempty"`
	Model  string `json:"model,omitempty"`
	Route  string `json:"route,omitempty"`
}

// Request is the item plus the effective model/route after defaults.
type Request struct {
	Item
	Model string
	Route string
}

// Usage is one item's token accounting; nil CostUSDMicros means unknown.
type Usage struct {
	Input         int64  `json:"input_tokens"`
	CachedInput   int64  `json:"cached_input_tokens,omitempty"`
	CacheCreation int64  `json:"cache_creation_input_tokens,omitempty"`
	Output        int64  `json:"output_tokens"`
	CostUSDMicros *int64 `json:"cost_usd_micros,omitempty"`
}

// Step is one recorded step. Role is system|user|assistant.
type Step struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Reply is one completed item; Steps must end with an assistant step.
type Reply struct {
	Steps []Step
	Usage Usage
}

// Runner executes one item and must honour ctx.
type Runner interface {
	Run(ctx context.Context, req *Request) (Reply, error)
}

// Ledger records one executed item exactly once (never on resume).
type Ledger interface {
	Record(ctx context.Context, req *Request, u Usage) error
}

// Config configures a batch. Zero Parallel takes its default.
type Config struct {
	Parallel int
	OutDir   string
	Model    string
	Route    string
	Resume   bool
	Ledger   Ledger
}

// Batch is one resumable output directory.
type Batch struct {
	cfg  Config
	mu   sync.Mutex // serializes results.jsonl appends
	fail atomic.Bool
}

// New validates cfg and returns a resumable batch.
func New(cfg *Config) (*Batch, error) {
	if strings.TrimSpace(cfg.OutDir) == "" {
		return nil, ErrNoOutDir
	}
	if cfg.Parallel == 0 {
		cfg.Parallel = DefaultParallel
	}
	if cfg.Parallel < 1 || cfg.Parallel > MaxParallel {
		return nil, fmt.Errorf("%w: %d (want 1..%d)", ErrBadParallel, cfg.Parallel, MaxParallel)
	}
	return &Batch{cfg: *cfg}, nil
}

// Load parses JSONL items; blank lines are ignored and ids must be unique.
func (b *Batch) Load(r io.Reader) ([]Item, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	seen := map[string]bool{}
	var items []Item
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var it Item
		if err := json.Unmarshal([]byte(raw), &it); err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", ErrBadItem, line, err)
		}
		if !itemIDRE.MatchString(it.ID) || seen[it.ID] || strings.TrimSpace(it.Prompt) == "" {
			return nil, fmt.Errorf("%w: line %d: id %q", ErrBadItem, line, it.ID)
		}
		seen[it.ID] = true
		items = append(items, it)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("batch: read items: %w", err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: no items", ErrBadItem)
	}
	return items, nil
}

// Run executes items with cfg.Parallel workers, returning one result per item
// in input order. Per-item failures live in Result.Err; a filesystem or
// ledger failure stops the run and marks the remaining items canceled.
func (b *Batch) Run(ctx context.Context, r Runner, items []Item) ([]Result, error) {
	if r == nil {
		return nil, errors.New("batch: runner required")
	}
	if err := os.MkdirAll(filepath.Join(b.cfg.OutDir, "trajectories"), 0o700); err != nil {
		return nil, fmt.Errorf("batch: create output: %w", err)
	}
	done, err := b.completed()
	if err != nil {
		return nil, err
	}
	if !b.cfg.Resume {
		done = map[string]bool{}
	}
	results := make([]Result, len(items))
	for i := range items {
		results[i].ID = items[i].ID
	}
	work := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var runErr error
	for range b.cfg.Parallel {
		wg.Go(func() {
			for i := range work {
				if b.fail.Load() {
					results[i].Err = ErrCanceled.Error()
					continue
				}
				if err := b.runOne(ctx, r, &items[i], &results[i], done); err != nil {
					mu.Lock()
					runErr = errors.Join(runErr, err)
					mu.Unlock()
					b.fail.Store(true)
				}
			}
		})
	}
	for i := range items {
		select {
		case work <- i:
		case <-ctx.Done():
			results[i].Err = ErrCanceled.Error()
		}
	}
	close(work)
	wg.Wait()
	return results, runErr
}

func (b *Batch) runOne(ctx context.Context, r Runner, it *Item, out *Result, done map[string]bool) error {
	req := &Request{Item: *it, Model: cmp.Or(it.Model, b.cfg.Model), Route: cmp.Or(it.Route, b.cfg.Route)}
	out.Model, out.Route = req.Model, req.Route
	if done[it.ID] && b.cfg.Resume {
		if tr, err := b.readTrajectory(it.ID); err == nil {
			*out = Result{ID: it.ID, Model: req.Model, Route: req.Route, Steps: tr.Steps, Usage: tr.Usage}
			return nil
		}
	}
	reply, err := r.Run(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("batch: item %s: %w", it.ID, err)
		}
		out.Err = err.Error()
		return nil
	}
	out.Steps = reply.Steps
	out.Usage = reply.Usage
	if err := b.writeItem(it.ID, req, out); err != nil {
		return err
	}
	if err := b.appendRow(out); err != nil {
		return err
	}
	if b.cfg.Ledger != nil {
		if err := b.cfg.Ledger.Record(ctx, req, reply.Usage); err != nil {
			return fmt.Errorf("batch: ledger %s: %w", it.ID, err)
		}
	}
	return nil
}

// Result is one item's outcome; results.jsonl carries the accounting fields.
type Result struct {
	ID    string `json:"id"`
	Model string `json:"model,omitempty"`
	Route string `json:"route,omitempty"`
	Steps []Step `json:"-"`
	Usage Usage  `json:"usage"`
	Err   string `json:"error,omitempty"`
}

// completed reads the rows a previous run persisted.
func (b *Batch) completed() (_ map[string]bool, err error) {
	out := map[string]bool{}
	f, err := os.Open(filepath.Join(b.cfg.OutDir, "results.jsonl")) // #nosec G304 -- explicit output
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("batch: open results: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	dec := json.NewDecoder(f)
	for {
		var row Result
		if derr := dec.Decode(&row); errors.Is(derr, io.EOF) {
			break
		} else if derr != nil {
			return nil, fmt.Errorf("batch: read results: %w", derr)
		}
		if row.ID != "" && row.Err == "" {
			out[row.ID] = true
		}
	}
	return out, nil
}

func (b *Batch) appendRow(row *Result) error {
	raw, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("batch: encode result: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(b.cfg.OutDir, "results.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- explicit output
	if err != nil {
		return fmt.Errorf("batch: append results: %w", err)
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return errors.Join(fmt.Errorf("batch: write results: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("batch: close results: %w", err)
	}
	return nil
}

// writeFileAtomic writes data through a 0600 temp file and rename.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("batch: temp %s: %w", path, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(fmt.Errorf("batch: write %s: %w", path, err), tmp.Close(), os.Remove(name))
	}
	if err := tmp.Close(); err != nil {
		return errors.Join(fmt.Errorf("batch: close %s: %w", path, err), os.Remove(name))
	}
	if err := os.Rename(name, path); err != nil {
		return errors.Join(fmt.Errorf("batch: rename %s: %w", path, err), os.Remove(name))
	}
	return nil
}
