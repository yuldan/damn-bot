package batch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Trajectory is one item's recorded steps in the canonical in-memory shape.
type Trajectory struct {
	ID    string `json:"id"`
	Model string `json:"model,omitempty"`
	Steps []Step `json:"steps"`
	Usage Usage  `json:"usage"`
}

// ErrBadTrajectory rejects a file that cannot be replayed read-only.
var ErrBadTrajectory = errors.New("batch: invalid trajectory")

// Format names the serialized views (SPEC §9.13: OpenAI/ShareGPT).
type Format string

// Trajectory views: the same steps in OpenAI or ShareGPT shape.
const (
	FormatOpenAI   Format = "openai"
	FormatShareGPT Format = "sharegpt"
)

type openAIView struct {
	ID       string    `json:"id"`
	Model    string    `json:"model,omitempty"`
	Messages []Step    `json:"messages"`
	Usage    openAIUse `json:"usage"`
}

type openAIUse struct {
	Input  int64  `json:"prompt_tokens"`
	Output int64  `json:"completion_tokens"`
	Cost   *int64 `json:"cost_usd_micros,omitempty"`
}

type shareGPTView struct {
	ID            string          `json:"id"`
	Conversations []shareGPTEntry `json:"conversations"`
}

type shareGPTEntry struct {
	From  string `json:"from"`
	Value string `json:"value"`
}

// WriteTrajectory renders tr in format; every step appears in both views.
func WriteTrajectory(w io.Writer, tr *Trajectory, format Format) error {
	if err := validateTrajectory(tr); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	var view any
	switch format {
	case FormatOpenAI:
		view = openAIView{
			ID: tr.ID, Model: tr.Model, Messages: tr.Steps,
			Usage: openAIUse{Input: tr.Usage.Input, Output: tr.Usage.Output, Cost: tr.Usage.CostUSDMicros},
		}
	case FormatShareGPT:
		v := shareGPTView{ID: tr.ID}
		for _, s := range tr.Steps {
			v.Conversations = append(v.Conversations, shareGPTEntry{From: shareGPTRole(s.Role), Value: s.Content})
		}
		view = v
	default:
		return fmt.Errorf("%w: unknown format %q", ErrBadTrajectory, format)
	}
	if err := enc.Encode(view); err != nil {
		return fmt.Errorf("batch: encode %s trajectory: %w", format, err)
	}
	return nil
}

func shareGPTRole(role string) string {
	switch role {
	case "assistant":
		return "gpt"
	case "user":
		return "human"
	default:
		return role
	}
}

// ReadTrajectory decodes the OpenAI view read-only and validates it.
func ReadTrajectory(r io.Reader) (*Trajectory, error) {
	var f openAIView
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadTrajectory, err)
	}
	tr := &Trajectory{
		ID: f.ID, Model: f.Model, Steps: f.Messages,
		Usage: Usage{Input: f.Usage.Input, Output: f.Usage.Output, CostUSDMicros: f.Usage.Cost},
	}
	if err := validateTrajectory(tr); err != nil {
		return nil, err
	}
	return tr, nil
}

func validateTrajectory(tr *Trajectory) error {
	if tr == nil || !itemIDRE.MatchString(tr.ID) {
		return fmt.Errorf("%w: missing or invalid id", ErrBadTrajectory)
	}
	if len(tr.Steps) == 0 || tr.Steps[len(tr.Steps)-1].Role != "assistant" {
		return fmt.Errorf("%w: %s must end with an assistant step", ErrBadTrajectory, tr.ID)
	}
	for i, s := range tr.Steps {
		switch s.Role {
		case "system", "user", "assistant":
		default:
			return fmt.Errorf("%w: %s step %d role %q", ErrBadTrajectory, tr.ID, i, s.Role)
		}
	}
	return nil
}

// Replay is the read-only cursor: it never runs a provider or a tool.
type Replay struct {
	tr   *Trajectory
	next int
}

// NewReplay validates tr and returns a cursor at step zero.
func NewReplay(tr *Trajectory) (*Replay, error) {
	if err := validateTrajectory(tr); err != nil {
		return nil, err
	}
	return &Replay{tr: tr}, nil
}

// Next returns a copy of the next step, or false at the end.
func (r *Replay) Next() (Step, bool) {
	if r == nil || r.next >= len(r.tr.Steps) {
		return Step{}, false
	}
	s := r.tr.Steps[r.next]
	r.next++
	return s, true
}

func (b *Batch) writeItem(id string, req *Request, res *Result) error {
	tr := &Trajectory{ID: id, Model: req.Model, Steps: res.Steps, Usage: res.Usage}
	for _, format := range []Format{FormatOpenAI, FormatShareGPT} {
		var buf strings.Builder
		if err := WriteTrajectory(&buf, tr, format); err != nil {
			return err
		}
		if err := writeFileAtomic(b.trajectoryPath(id, format), []byte(buf.String())); err != nil {
			return err
		}
	}
	return nil
}

func (b *Batch) trajectoryPath(id string, format Format) string {
	return filepath.Join(b.cfg.OutDir, "trajectories", id+"."+string(format)+".json")
}

func (b *Batch) readTrajectory(id string) (_ *Trajectory, err error) {
	f, err := os.Open(b.trajectoryPath(id, FormatOpenAI)) // #nosec G304 -- id is validated
	if err != nil {
		return nil, fmt.Errorf("batch: open trajectory %s: %w", id, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return ReadTrajectory(f)
}
