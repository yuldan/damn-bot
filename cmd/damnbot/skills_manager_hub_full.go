//go:build !minimal

package main

import (
	"io"
	"io/fs"
	"time"
)

// emptyFS is a readable root with no files, used for a hub skill whose only
// verified artifact is SKILL.md. Open(".") is a real empty directory so the
// lint walker sees zero script references instead of a missing root.
//
// It is full-build code: the minimal binary has no skills hub, so no skill
// can ever carry skills.SourceHub there (ADR-325).
type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	if name != "." {
		return nil, fs.ErrNotExist
	}
	return emptyDir{}, nil
}

type emptyDir struct{}

func (emptyDir) Stat() (fs.FileInfo, error)         { return emptyDirInfo{}, nil }
func (emptyDir) Read([]byte) (int, error)           { return 0, io.EOF }
func (emptyDir) Close() error                       { return nil }
func (emptyDir) ReadDir(int) ([]fs.DirEntry, error) { return nil, nil }

type emptyDirInfo struct{}

func (emptyDirInfo) Name() string       { return "." }
func (emptyDirInfo) Size() int64        { return 0 }
func (emptyDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o500 }
func (emptyDirInfo) ModTime() time.Time { return time.Time{} }
func (emptyDirInfo) IsDir() bool        { return true }
func (emptyDirInfo) Sys() any           { return nil }

// hubSkillFiles is the source root of a hub skill: only SKILL.md was
// downloaded and verified, so the lint walker must see an empty directory and
// fail closed on every script reference instead of trusting unpinned files.
func hubSkillFiles() (fs.FS, error) { return emptyFS{}, nil }
