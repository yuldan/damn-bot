package importer

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// maxMemoryChars mirrors the memory content cap of SPEC §4.8.
const maxMemoryChars = 500

// memoryFiles are the two curated stores of SPEC §9.15's Hermes import.
func memoryFiles() []struct{ file, store string } {
	return []struct{ file, store string }{
		{"MEMORY.md", "memory"},
		{"USER.md", "user"},
	}
}

func (i *Importer) collectMemory(root string, c *collected) error {
	for _, store := range memoryFiles() {
		raw, err := i.readOptional(filepath.Join(root, store.file))
		if err != nil {
			return err
		}
		if raw == nil {
			continue
		}
		if !utf8.Valid(raw) {
			return syntaxError(filepath.Join(root, store.file), errors.New("file is not valid UTF-8"))
		}
		content := strings.ReplaceAll(string(raw), "\r\n", "\n")
		for entry := range strings.SplitSeq(content, "\n§\n") {
			text := strings.TrimSpace(entry)
			if text == "" {
				continue
			}
			if n := utf8.RuneCountInString(text); n > maxMemoryChars {
				// The entry itself is never echoed: a memory file may hold
				// private text and this error reaches a terminal.
				return syntaxError(filepath.Join(root, store.file),
					fmt.Errorf("entry is %d chars (max %d)", n, maxMemoryChars))
			}
			if c.reject(SourceMemory, store.store) {
				continue
			}
			c.memories = append(c.memories, MemoryEntry{Store: store.store, Text: text})
			c.report.item(SourceMemory)
		}
	}
	return nil
}
