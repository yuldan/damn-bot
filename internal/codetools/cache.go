package codetools

import "time"

// fileKey identifies one version of a file (CODE-TOOLS §7): device, inode,
// size and mtime; path disambiguates where the OS reports no inode.
type fileKey struct {
	path     string
	dev, ino uint64
	size     int64
	mtimeNs  int64
	ctimeNs  int64
}

// tagEntry is what the session handed to the model as a tag (CODE-TOOLS §5.1).
type tagEntry struct {
	sum  [32]byte // full sha256 of bytes, or of the stat tuple (stat-tag)
	stat fileKey
	at   time.Time
}

type caches struct {
	tags map[string]tagEntry // tagKey(resolved, tag)
}

func newCaches() *caches {
	return &caches{
		tags: map[string]tagEntry{},
	}
}

func tagKey(resolved, tag string) string { return resolved + "\x00" + tag }
