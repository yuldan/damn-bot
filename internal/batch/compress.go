package batch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const truncateMarker = " …[trimmed %d chars]… "

// CompressTrajectory returns a copy whose steps are at most maxChars runes.
// The input is never mutated and the output is deterministic.
func CompressTrajectory(tr *Trajectory, maxChars int) *Trajectory {
	if tr == nil {
		return nil
	}
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	out := &Trajectory{ID: tr.ID, Model: tr.Model, Usage: tr.Usage}
	out.Steps = make([]Step, len(tr.Steps))
	for i, s := range tr.Steps {
		out.Steps[i] = Step{Role: s.Role, Content: truncate(s.Content, maxChars)}
	}
	return out
}

func truncate(s string, maxChars int) string {
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	keep, tail := maxChars/2, maxChars-maxChars/2
	var b strings.Builder
	b.WriteString(string(r[:keep]))
	fmt.Fprintf(&b, truncateMarker, len(r)-keep-tail)
	b.WriteString(string(r[len(r)-tail:]))
	return b.String()
}

// Compressed is the deterministic summary of one compress run.
type Compressed struct {
	Items           int `json:"items"`
	Truncated       int `json:"truncated"`
	OriginalChars   int `json:"original_chars"`
	CompressedChars int `json:"compressed_chars"`
}

// CompressDir rewrites every saved trajectory into the compressed view and
// writes compress-summary.json; it always reads the uncompressed file.
func CompressDir(outDir string, maxChars int) (Compressed, error) {
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	dir := filepath.Join(outDir, "trajectories")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Compressed{}, fmt.Errorf("batch: read trajectories: %w", err)
	}
	var sum Compressed
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "."+string(FormatOpenAI)+".json") {
			continue
		}
		tr, rerr := readTrajectoryFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			return Compressed{}, rerr
		}
		comp := CompressTrajectory(tr, maxChars)
		before, after := totalChars(tr.Steps), totalChars(comp.Steps)
		sum.Items++
		sum.OriginalChars += before
		sum.CompressedChars += after
		if after < before {
			sum.Truncated++
		}
		var buf strings.Builder
		if werr := WriteTrajectory(&buf, comp, FormatOpenAI); werr != nil {
			return Compressed{}, werr
		}
		if werr := writeFileAtomic(filepath.Join(dir, tr.ID+"."+string(FormatOpenAI)+".compressed.json"), []byte(buf.String())); werr != nil {
			return Compressed{}, werr
		}
	}
	raw, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return Compressed{}, fmt.Errorf("batch: encode compress summary: %w", err)
	}
	return sum, writeFileAtomic(filepath.Join(outDir, "compress-summary.json"), append(raw, '\n'))
}

func readTrajectoryFile(path string) (_ *Trajectory, err error) {
	f, err := os.Open(path) // #nosec G304 -- path is under the explicit outDir
	if err != nil {
		return nil, fmt.Errorf("batch: open trajectory: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return ReadTrajectory(f)
}

func totalChars(steps []Step) int {
	n := 0
	for _, s := range steps {
		n += len([]rune(s.Content))
	}
	return n
}
