//go:build minimal

package assemble

// replyLine is absent from the minimal build; auto and every code add no
// stable line there.
func replyLine(string) string { return "" }
