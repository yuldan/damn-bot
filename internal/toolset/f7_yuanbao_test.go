package toolset

import "testing"

// TestF7TSET032Yuanbao is the hermes-yuanbao toolset: five tools, available
// without a tool inventory, and never pulled in by another bundle.
func TestF7TSET032Yuanbao(t *testing.T) {
	t.Parallel()
	assertOptionalToolset(t, "hermes-yuanbao", []string{
		"yb_query_group_info", "yb_query_group_members", "yb_search_sticker", "yb_send_dm", "yb_send_sticker",
	})
}
