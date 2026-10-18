// Command gen-corpus membangkitkan korpus C2/C3 (CODE-TOOLS §10.1) dan mencetak baris corpora.lock.
//
//	go run ./gencorpus/cmd/gen-corpus -seed 1 -files 80000 -mean 13750 -out /tmp/damnbot-corpora/C2
//	go run ./gencorpus/cmd/gen-corpus -seed 3 -files 1000 -mean 12000 -out /tmp/damnbot-corpora/C3
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/damnbot/damnbot/bench/gencorpus"
)

func main() {
	seed := flag.Uint64("seed", 1, "seed PCG")
	files := flag.Int("files", 1000, "jumlah file utama (tanpa file .gitignore/ter-ignore)")
	mean := flag.Int("mean", 12000, "rata-rata byte file teks")
	out := flag.String("out", "", "direktori output (belum ada)")
	name := flag.String("name", "C?", "ID korpus untuk baris lock")
	flag.Parse()
	st, err := gencorpus.Generate(*out, gencorpus.Params{Seed: *seed, Files: *files, MeanBytes: *mean})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s seed=%d files=%d mean=%d total_files=%d dirs=%d bytes=%d binary=%d large=%d minified=%d ignored=%d sha256=%s\n",
		*name, *seed, *files, *mean, st.Files, st.Dirs, st.Bytes, st.Binary, st.Large, st.Minified, st.Ignored, st.Digest)
}
