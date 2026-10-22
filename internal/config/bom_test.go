package config

import (
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestConfigBOM (EC-FIO-007): the TOML reader reads over a UTF-8 BOM (the
// decoder does it before lexing, so no preprocessing in this package is
// needed), and a config.toml saved by a Windows editor therefore parses. The
// mark is only ever a leading prefix and is not a repair.
func TestConfigBOM(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-FIO-007", "F3", "covered") {
		return
	}
	t.Run("EC-FIO-007", func(t *testing.T) {
		t.Parallel()

		cfg, err := parse(t, "\ufeff[loop]\nmax_iterations = 5\n")
		if err != nil {
			t.Fatalf("BOM-prefixed config rejected: %v", err)
		}
		if cfg.Loop.MaxIterations != 5 {
			t.Fatalf("max_iterations = %d, want 5", cfg.Loop.MaxIterations)
		}

		// Negative control: the mark is not a repair.
		if _, perr := parse(t, "\ufeff[loop]\nmax_iterations = \n"); !errors.Is(perr, ErrSyntax) {
			t.Fatalf("invalid TOML after a BOM: err = %v, want ErrSyntax", perr)
		}
		if _, perr := parse(t, "\ufeff[loop]\nmax_iterations_typo = 1\n"); !errors.Is(perr, ErrUnknownKey) {
			t.Fatalf("unknown key after a BOM: err = %v, want ErrUnknownKey", perr)
		}
		// Negative control: only the prefix is a mark, so an interior U+FEFF is
		// content and survives.
		cfg, err = parse(t, "[provider]\nmodel = \"\ufeffx\"\n")
		if err != nil {
			t.Fatalf("interior U+FEFF: %v", err)
		}
		if got := cfg.Provider.Model; got != "\ufeffx" {
			t.Fatalf("model = %q, want the interior U+FEFF kept", got)
		}
	})
}
