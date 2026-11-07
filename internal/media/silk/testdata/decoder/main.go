// Command decoder is the fake SILK decoder used by the package tests. It lives
// under testdata so it is not part of the module build or lint surface.
package main

import (
	"io"
	"os"
)

func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	switch os.Getenv("SILK_HELPER_MODE") {
	case "ok":
		_, _ = os.Stdout.Write([]byte{0x01, 0x00, 0x02, 0x00, 0xfd, 0xff, 0x00, 0x00})
	case "empty":
	case "fail":
		_, _ = os.Stderr.WriteString("decoder rejected the payload")
		os.Exit(3)
	case "huge":
		buf := make([]byte, 1<<20)
		for i := 0; i < 40; i++ {
			if _, err := os.Stdout.Write(buf); err != nil {
				os.Exit(4)
			}
		}
	}
}
