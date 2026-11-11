//go:build !minimal

package main

import (
	"errors"
	"fmt"
	"os"
)

// f8MemoryState is <data_dir>/memory.json (MEM-010, CLI-052). It carries only
// the provider name and the keychain entry NAME: the token itself never leaves
// the secret store.
type f8MemoryState struct {
	Provider    string `json:"provider"`
	TokenSecret string `json:"token_secret,omitempty"`
	BaseURL     string `json:"base_url,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	PeerID      string `json:"peer_id,omitempty"`
	Active      bool   `json:"active"`
}

func memoryStatePath(dir string) string { return f8StorePath(dir, "memory.json") }

// loadMemoryState reads the operator state; ok is false when absent.
func loadMemoryState(dir string) (f8MemoryState, bool, error) {
	var st f8MemoryState
	if dir == "" {
		return st, false, nil
	}
	path := memoryStatePath(dir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return st, false, nil
	} else if err != nil {
		return st, false, fmt.Errorf("memory state: %w", err)
	}
	if err := f8Load(path, &st); err != nil {
		return st, false, err
	}
	return st, true, nil
}

// memoryStateDir resolves the data directory that owns memory.json.
func memoryStateDir(cfgPath string) (string, error) {
	_, _, dir, err := f8LoadSetup(cfgPath, "")
	if err != nil {
		return "", err
	}
	return dir, nil
}

// saveMemoryState persists the active provider reference below the data dir.
func saveMemoryState(cfgPath string, st *f8MemoryState) error {
	dir, err := memoryStateDir(cfgPath)
	if err != nil || dir == "" {
		return err
	}
	return f8Save(memoryStatePath(dir), st)
}

// removeMemoryState clears the operator state after reset/off.
func removeMemoryState(cfgPath string) error {
	dir, err := memoryStateDir(cfgPath)
	if err != nil || dir == "" {
		return err
	}
	if err := os.Remove(memoryStatePath(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove memory state: %w", err)
	}
	return nil
}
