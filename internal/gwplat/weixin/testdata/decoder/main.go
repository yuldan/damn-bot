// Command decoder is the fake SILK decoder for the Weixin voice tests. It
// records its own invocation next to the executable so a composed decoder path
// can prove it ran, and it fails when argv[1] or its filename carries "fail".
package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = os.WriteFile(os.Args[0]+".ran", []byte("1"), 0o600)
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if mode == "fail" || strings.Contains(filepath.Base(os.Args[0]), "fail") {
		_, _ = os.Stderr.WriteString("bad silk")
		os.Exit(3)
	}
	if mode == "empty" {
		return
	}
	_, _ = os.Stdout.Write([]byte{0x01, 0x00, 0x02, 0x00})
}
