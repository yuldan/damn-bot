//go:build !minimal

package toolset

// f8Toolsets carries the F8 connector toolsets; the composite bundle has no
// registry of its own.
func f8Toolsets() []Definition {
	return []Definition{
		{Name: "discord", Description: "Discord messages, member search and threads (default off)", Tools: []string{"discord"}},
		{Name: "discord_admin", Description: "Discord admin channels, roles and pins (default off)", Tools: []string{"discord_admin"}},
		{Name: "feishu_doc", Description: "Read Feishu/Lark documents (default off)", Tools: []string{"feishu_doc_read"}},
		{Name: "feishu_drive", Description: "Feishu drive comments and replies (default off)", Tools: []string{
			"feishu_drive_list_comments", "feishu_drive_list_comment_replies",
			"feishu_drive_reply_comment", "feishu_drive_add_comment",
		}},
		{Name: "x_search", Description: "Search X posts and threads through xAI (default off)", Tools: []string{"x_search"}},
		{Name: "connections", Description: "Manage hosted connector accounts and vault references (default off)", Tools: []string{"manage_connections"}},
		{Name: "catalog", Description: "Search the plugin catalog and request installs through the desktop card (default off)", Tools: []string{"manage_catalog"}},
		{
			Name: "desktop_ui", Description: "Desktop and web GUI pane, preview, terminal, theme and tip tools (default off)",
			Tools: []string{
				"annotate_preview", "close_terminal", "drive_preview", "focus_pane",
				"desktop_preview", "react_to_message", "read_terminal", "read_window_below",
				"set_theme", "show_tip", "gui_tour",
			},
		},
		{Name: "project", Description: "Named Desktop Project workspaces (default off)", Tools: []string{"desktop_project"}},
		{Name: "onboarding", Description: "First-run onboarding card (default off)", Tools: []string{"setup_beat"}},
		{
			Name: "hermes-connectors", Description: "Connector bundle: Graph, Teams, Feishu, Meet, X search, Discord, OSV (default off)",
			Tools: []string{
				"ms_graph_mail", "ms_graph_calendar", "ms_graph_files", "teams_pipeline", "google_meet", "osv_check",
			},
			Includes: []string{"discord", "discord_admin", "feishu_doc", "feishu_drive", "x_search"},
		},
	}
}
