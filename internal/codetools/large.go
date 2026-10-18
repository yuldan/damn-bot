package codetools

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

func hashFile(f *os.File, size int64) ([32]byte, error) {
	h := sha256.New()
	var sum [32]byte
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, size)); err != nil {
		return sum, fmt.Errorf("codetools: hash: %w", err)
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
