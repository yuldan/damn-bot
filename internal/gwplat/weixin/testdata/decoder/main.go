// Command decoder is the fake SILK decoder for the Weixin voice tests.
package main

import (
	"io"
	"os"
)

func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "", "ok":
		_, _ = os.Stdout.Write([]byte{0x01, 0x00, 0x02, 0x00})
	case "fail":
		_, _ = os.Stderr.WriteString("bad silk")
		os.Exit(3)
	}
}
