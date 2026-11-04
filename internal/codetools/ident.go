package codetools

// fileID is the OS identity of one file object: volume (device) and file
// index (inode). Every name of the object — hard link, symlink target,
// case variant — yields the same fileID, and two distinct objects never
// share one.
type fileID struct{ Vol, Hi, Lo uint64 }

// IdentityError reports that the OS identity of Path could not be read.
// Callers fail closed: size and mtime are neither unique nor stable, so a
// stat tuple is not a substitute for a file identity.
type IdentityError struct {
	Path string
	Err  error
}

func (e *IdentityError) Error() string { return "codetools: identify " + e.Path + ": " + e.Err.Error() }

// Unwrap exposes the OS error so errors.Is can still see fs.ErrNotExist.
func (e *IdentityError) Unwrap() error { return e.Err }
