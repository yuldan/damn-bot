package main

import "fmt"

type portableCounts struct {
	Items, Changed, Skipped, Conflicts, Config, Skills, Memory, Rejected, Protected int
}

func portableReportLine(c portableCounts, dry bool) string {
	if dry {
		return fmt.Sprintf("items=%d changed=%d skipped=%d conflicts=%d config=%d skills=%d memory=%d rejected=%d protected=%d (dry run; nothing was written)", c.Items, c.Changed, c.Skipped, c.Conflicts, c.Config, c.Skills, c.Memory, c.Rejected, c.Protected)
	}
	return fmt.Sprintf("items=%d changed=%d skipped=%d conflicts=%d config=%d skills=%d memory=%d rejected=%d protected=%d", c.Items, c.Changed, c.Skipped, c.Conflicts, c.Config, c.Skills, c.Memory, c.Rejected, c.Protected)
}
