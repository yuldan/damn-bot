//go:build windows

package sandbox

import (
	"fmt"
	"io/fs"
	"os"
)

// Chmod applies mode to the immediate child name of d. Windows cannot change
// the mode of a read-only file through an ordinary handle: a 0o440 file
// refuses data access ("Access is denied") and
// SetFileInformationByHandle(FileBasicInfo) needs FILE_WRITE_ATTRIBUTES,
// which an O_RDONLY handle does not carry. os.Root.Chmod opens the entry
// with FILE_WRITE_ATTRIBUTES and FILE_FLAG_OPEN_REPARSE_POINT only, so a
// read-only child still takes the mode and a link swapped in for the final
// component receives it instead of its target (JN2-1); os.Root also refuses
// every traversal that would leave the directory.
func (d *Dir) Chmod(name string, mode fs.FileMode) error {
	if err := dirChild(name); err != nil {
		return err
	}
	r, err := os.OpenRoot(d.path)
	if err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	defer closeDir(r)
	if err := r.Chmod(name, mode); err != nil {
		if linkOnPath(r, name) {
			return escaped(d.child(name), err)
		}
		return fmt.Errorf("sandbox: chmod: %w", err)
	}
	return nil
}
