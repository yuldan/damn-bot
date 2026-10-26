package importer

import (
	"path/filepath"
	"strings"
)

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
		content := strings.ReplaceAll(string(raw), "\r\n", "\n")
		for entry := range strings.SplitSeq(content, "\n§\n") {
			text := strings.TrimSpace(entry)
			if text == "" {
				continue
			}
			c.memories = append(c.memories, MemoryEntry{Store: store.store, Text: text})
			c.report.item(SourceMemory)
		}
	}
	return nil
}
