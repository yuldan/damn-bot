// Command budget menulis laporan JSON harness token P1 untuk snapshot hermes.
//
//	cd bench && go run ./budget/cmd/budget -snapshot budget/testdata/hermes-868b9f244f > budget/results/hermes-868b9f244f.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/damnbot/damnbot/bench/budget"
	"github.com/damnbot/damnbot/bench/budget/o200k"
)

type report struct {
	Tokenizer      string          `json:"tokenizer"`
	RanksSHA256    string          `json:"ranks_sha256"`
	Default        budget.Snapshot `json:"default"`
	NoContextFiles budget.Snapshot `json:"no_context_files"`
	Checks         []budget.Check  `json:"checks"`
	AllGatesOK     bool            `json:"all_gates_ok"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	snap := flag.String("snapshot", "budget/testdata/hermes-868b9f244f", "direktori ekstraksi (default/, no_context_files/)")
	flag.Parse()
	tok, err := o200k.Load()
	if err != nil {
		return err
	}
	def, err := budget.LoadSnapshot(tok, filepath.Join(*snap, "default"))
	if err != nil {
		return err
	}
	noCtx, err := budget.LoadSnapshot(tok, filepath.Join(*snap, "no_context_files"))
	if err != nil {
		return err
	}
	r := report{
		Tokenizer: "o200k_base", RanksSHA256: o200k.RanksSHA256,
		Default: def, NoContextFiles: noCtx, Checks: budget.Compare(def, noCtx), AllGatesOK: true,
	}
	for _, c := range r.Checks {
		if c.Gate && !c.OK() {
			r.AllGatesOK = false
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	return nil
}
