// Command gen-evals regenerates bench/evals/v1 and MANIFEST.sha256 from the
// pinned sources. Run from bench/: go run ./evals/cmd/gen-evals -dir evals
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/damnbot/damnbot/bench/evals"
)

func main() {
	dir := flag.String("dir", "evals", "evals directory (contains sources/)")
	flag.Parse()
	if err := evals.Generate(*dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
