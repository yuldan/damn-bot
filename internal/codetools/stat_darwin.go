package codetools

import (
	"os"
	"syscall"
)

// keyOf builds the cache/stat-tag identity of fi (CODE-TOOLS §5.1, §7).
func keyOf(path string, fi os.FileInfo) fileKey {
	k := fileKey{path: path, size: fi.Size(), mtimeNs: fi.ModTime().UnixNano()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		k.ino, k.ctimeNs = st.Ino, syscall.TimespecToNsec(st.Ctimespec)
	}
	return k
}
