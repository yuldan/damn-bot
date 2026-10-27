package skills

import (
	"embed"
	"io/fs"
)

// bundledFS is the builtin skill corpus compiled into the binary. The files
// live under bundled/<name>/SKILL.md so a skill can ship helper files next to
// its SKILL.md without a second embed pattern.
//
//go:embed bundled/*/SKILL.md
var bundledFS embed.FS

// BundledFS returns the embedded builtin corpus. It is read-only; SyncBundled
// copies it into the config directory and indexes it through Service.Install.
func BundledFS() fs.FS { return bundledFS }
