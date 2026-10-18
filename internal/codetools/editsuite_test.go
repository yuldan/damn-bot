package codetools

import (
	"testing"
)

func readAll(t testing.TB, path string) []byte {
	t.Helper()
	return readFixture(t, path)
}
