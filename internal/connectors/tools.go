package connectors

import (
	"encoding/json"
)

// Tool is one deferred catalog entry of the connectors module.
type Tool struct {
	Name        string
	Description string
	Effect      string
	Schema      json.RawMessage
	Tags        []string
}

// Tools returns every connector tool in stable order. The set is fixed at
// build time: config can enable a connector but never add a tool, and a name
// absent here cannot be invoked.
func Tools() []Tool {
	out := graphTools()
	out = append(out, feishuTools()...)
	out = append(out, meetTools()...)
	out = append(out, xSearchTools()...)
	out = append(out, discordTools()...)
	return append(out, osvTools()...)
}

// object renders one connector tool schema with a closed property set.
func object(props string, required ...string) json.RawMessage {
	req := ""
	if len(required) > 0 {
		req = `,"required":[`
		for i, r := range required {
			if i > 0 {
				req += ","
			}
			req += `"` + r + `"`
		}
		req += `]`
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"additionalProperties":false` + req + `}`)
}

const (
	pageProp = `"page":{"type":"integer","minimum":1,"maximum":5}`
	topProp  = `"top":{"type":"integer","minimum":1,"maximum":100}`
	fileProp = `"file_token":{"type":"string","minLength":1,"maxLength":256}`
)

func graphTools() []Tool {
	return []Tool{
		{
			Name: "ms_graph_mail", Description: "Read Microsoft Graph mail messages",
			Effect: "read", Tags: []string{"outlook", "email", "graph"},
			Schema: object(`"folder":{"type":"string","maxLength":128},`+topProp+`,`+pageProp+`,"select":{"type":"array","maxItems":8,"items":{"type":"string","maxLength":64}}`, "folder"),
		},
		{
			Name: "ms_graph_calendar", Description: "Read Microsoft Graph calendar events",
			Effect: "read", Tags: []string{"outlook", "meeting", "graph"},
			Schema: object(`"days":{"type":"integer","minimum":1,"maximum":90},` + topProp + `,` + pageProp + `,"select":{"type":"array","maxItems":8,"items":{"type":"string","maxLength":64}}`),
		},
		{
			Name: "ms_graph_files", Description: "List or read Microsoft Graph drive files",
			Effect: "read", Tags: []string{"onedrive", "sharepoint", "graph"},
			Schema: object(`"path":{"type":"string","maxLength":512},` + topProp + `,` + pageProp),
		},
		{
			Name: "teams_pipeline", Description: "Manage the Microsoft Teams meeting pipeline",
			Effect: "idempotent", Tags: []string{"teams", "meeting", "subscription", "transcript"},
			Schema: object(`"action":{"type":"string","enum":["subscribe","renew","status","transcripts"]},"subscription_id":{"type":"string","maxLength":128},"meeting_id":{"type":"string","maxLength":256}`, "action"),
		},
	}
}

func feishuTools() []Tool {
	return []Tool{
		{
			Name: "feishu_doc_read", Description: "Read a Feishu/Lark document",
			Effect: "read", Tags: []string{"lark", "document"},
			Schema: object(`"document_id":{"type":"string","minLength":1,"maxLength":256}`, "document_id"),
		},
		{
			Name: "feishu_drive_list_comments", Description: "List Feishu drive document comments",
			Effect: "read", Tags: []string{"lark", "comment"},
			Schema: object(fileProp+`,`+pageProp, "file_token"),
		},
		{
			Name: "feishu_drive_list_comment_replies", Description: "List Feishu comment replies",
			Effect: "read", Tags: []string{"lark", "comment", "reply"},
			Schema: object(fileProp+`,"comment_id":{"type":"string","minLength":1,"maxLength":128},`+pageProp, "file_token", "comment_id"),
		},
		{
			Name: "feishu_drive_reply_comment", Description: "Reply to one Feishu document comment",
			Effect: "non_idempotent", Tags: []string{"lark", "comment", "reply"},
			Schema: object(fileProp+`,"comment_id":{"type":"string","minLength":1,"maxLength":128},"content":{"type":"string","minLength":1,"maxLength":4096}`, "file_token", "comment_id", "content"),
		},
		{
			Name: "feishu_drive_add_comment", Description: "Add a comment to a Feishu document",
			Effect: "non_idempotent", Tags: []string{"lark", "comment"},
			Schema: object(fileProp+`,"content":{"type":"string","minLength":1,"maxLength":4096}`, "file_token", "content"),
		},
	}
}

func meetTools() []Tool {
	return []Tool{{
		Name: "google_meet", Description: "Join or transcribe a Google Meet meeting (Linux)",
		Effect: "dynamic", Tags: []string{"meet", "meeting", "transcript"},
		Schema: object(`"action":{"type":"string","enum":["join","transcript","speak","leave"]},"meeting_url":{"type":"string","maxLength":512},"session_id":{"type":"string","maxLength":128},"text":{"type":"string","maxLength":2000},"since":{"type":"integer","minimum":0}`, "action"),
	}}
}

func xSearchTools() []Tool {
	return []Tool{{
		Name: "x_search", Description: "Search X posts and threads through xAI",
		Effect: "read", Tags: []string{"twitter", "xai", "social"},
		Schema: object(`"query":{"type":"string","minLength":1,"maxLength":512},"limit":{"type":"integer","minimum":1,"maximum":20}`, "query"),
	}}
}

func discordTools() []Tool {
	return []Tool{
		{
			Name: "discord", Description: "Read Discord messages, find members, create threads",
			Effect: "dynamic", Tags: []string{"chat", "guild", "thread"},
			Schema: object(`"action":{"type":"string","enum":["messages","members","thread","send"]},"guild_id":{"type":"string","maxLength":64},"channel_id":{"type":"string","maxLength":64},"query":{"type":"string","maxLength":256},"content":{"type":"string","maxLength":2000},"limit":{"type":"integer","minimum":1,"maximum":100}`, "action"),
		},
		{
			Name: "discord_admin", Description: "Discord admin: channels, roles and pins",
			Effect: "dynamic", Tags: []string{"admin", "guild", "role"},
			Schema: object(`"action":{"type":"string","enum":["channels","roles","pin","unpin"]},"guild_id":{"type":"string","minLength":1,"maxLength":64},"channel_id":{"type":"string","maxLength":64},"message_id":{"type":"string","maxLength":64}`, "action", "guild_id"),
		},
	}
}

func osvTools() []Tool {
	return []Tool{{
		Name: "osv_check", Description: "Check a package version against OSV.dev advisories",
		Effect: "read", Tags: []string{"security", "vulnerability", "supply-chain"},
		Schema: object(`"package":{"type":"string","minLength":1,"maxLength":256},"version":{"type":"string","maxLength":128},"ecosystem":{"type":"string","maxLength":64}`, "package"),
	}}
}

// ToolDescriptor is one model-facing connector tool as the composition root
// maps it into modules.ToolDescriptor.
type ToolDescriptor struct {
	Name        string
	Description string
	Effect      string
	Schema      json.RawMessage
	Tags        []string
}
