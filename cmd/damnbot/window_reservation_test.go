package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/limitstore"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// TestRunWindowReservation (EC-CTX-017): the composition root feeds the
// resolved model window to the loop guard, so input above window-max_tokens
// stops before the provider is called (the 400 the guard prevents).
func TestRunWindowReservation(t *testing.T) {
	t.Parallel()
	prov := &injectProvider{text: "must not be called"}
	cfg := config.Default()
	// A learned window of MinWindow with the 8 192 default max_output leaves
	// no room for input (window - max_tokens = 0).
	dir := t.TempDir()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if serr := (limitstore.Store{DB: db}).Save(context.Background(), "openai", "fake", registry.MinWindow, "learned", 100); serr != nil {
		t.Fatal(serr)
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	var out bytes.Buffer
	code, err := runOne(context.Background(), &runOptions{
		prompt: "hello", model: "openai/fake", dataDir: dir, output: "json",
		prov: prov, cfgOverride: &cfg,
	}, &out)
	if err != nil || code != 2 { // a non-completed turn exits 2
		t.Fatalf("runOne = %d, %v (stdout %s)", code, err, out.String())
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.calls)
	}
	if s := out.String(); !strings.Contains(s, `"stop_reason":"max_tokens"`) ||
		!strings.Contains(s, `"stop_detail":"context_overflow"`) {
		t.Fatalf("turn result = %s, want max_tokens/context_overflow", s)
	}
}
