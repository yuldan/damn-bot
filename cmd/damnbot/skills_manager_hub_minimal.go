//go:build minimal

package main

import "io/fs"

// hubSkillFiles can never be reached in this build: the skills hub and its
// SourceHub installs are not compiled in (ADR-325). The seam stays here so
// the shared Files closure has one shape in both builds.
func hubSkillFiles() (fs.FS, error) { return nil, fs.ErrNotExist }
