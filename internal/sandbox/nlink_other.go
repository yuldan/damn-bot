//go:build !unix && !windows

package sandbox

import "os"

// linkCountPath: no link-count API on these OSes (wasip1, js, plan9);
// every file counts as singly linked.
func linkCountPath(string) (uint64, error) { return 1, nil }

// linkCountFile: see linkCountPath.
func linkCountFile(*os.File, os.FileInfo) (uint64, error) { return 1, nil }
