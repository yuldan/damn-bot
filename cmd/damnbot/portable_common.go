package main

import "fmt"

type portableCounts struct {
	Items, Changed, Skipped, Conflicts, Config, Skills, Memory, Rejected, Protected int
	ChangedItems                                                                    []string
}

func portableReportLine(c portableCounts, dry bool) string { //nolint:gocritic // small immutable report value
	line := fmt.Sprintf("items=%d changed=%d skipped=%d conflicts=%d config=%d skills=%d memory=%d rejected=%d protected=%d", c.Items, c.Changed, c.Skipped, c.Conflicts, c.Config, c.Skills, c.Memory, c.Rejected, c.Protected)
	if len(c.ChangedItems) > 0 {
		line += fmt.Sprintf(" changed_items=%v", c.ChangedItems)
	}
	if dry {
		line += " (dry run; nothing was written)"
	}
	return line
}
