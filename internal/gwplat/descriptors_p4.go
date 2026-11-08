package gwplat

import "github.com/damnbot/damnbot/internal/gateway/render"

// p4Descriptors is the additive P4 (REST/webhook) block of the closed
// registry. Every row stays default-off and derives caps from the SEC-10 table.
func p4Descriptors() []Descriptor {
	return []Descriptor{
		{Name: "bluebubbles", Transport: TransportWebhook, Caps: capsOf("bluebubbles"), Render: render.PlatformPlain, Secrets: []string{"bluebubbles_password"}, DefaultOff: true},
		{Name: "line", Transport: TransportWebhook, Caps: capsOf("line"), Render: render.PlatformPlain, Secrets: []string{"line_channel_secret", "line_access_token"}, DefaultOff: true},
		{Name: "sms", Transport: TransportWebhook, Caps: capsOf("sms"), Render: render.PlatformPlain, Secrets: []string{"sms_account_sid", "sms_auth_token", "sms_from"}, DefaultOff: true},
		{Name: "qqbot", Transport: TransportWS, Caps: capsOf("qqbot"), Render: render.PlatformPlain, Secrets: []string{"qqbot_app_id", "qqbot_client_secret"}, DefaultOff: true},
		{Name: "dingtalk", Transport: TransportWS, Caps: capsOf("dingtalk"), Render: render.PlatformPlain, Secrets: []string{"dingtalk_client_id", "dingtalk_client_secret"}, DefaultOff: true},
		{Name: "feishu", Transport: TransportWS, Caps: capsOf("feishu"), Render: render.PlatformPlain, Secrets: []string{"feishu_app_id", "feishu_app_secret", "feishu_verification_token"}, DefaultOff: true},
		{Name: "googlechat", Transport: TransportWebhook, Caps: capsOf("googlechat"), Render: render.PlatformPlain, Secrets: []string{"googlechat_service_account"}, DefaultOff: true},
		{Name: "mattermost", Transport: TransportWS, Caps: capsOf("mattermost"), Render: render.PlatformPlain, Secrets: []string{"mattermost_token"}, DefaultOff: true},
		{Name: "slack", Transport: TransportWS, Caps: capsOf("slack"), Render: render.PlatformSlack, Secrets: []string{"slack_bot_token", "slack_app_token"}, DefaultOff: true},
		{Name: "teams", Transport: TransportWebhook, Caps: capsOf("teams"), Render: render.PlatformPlain, Secrets: []string{"teams_app_id", "teams_app_password"}, DefaultOff: true},
		{Name: "wecom", Transport: TransportWS, Caps: capsOf("wecom"), Render: render.PlatformPlain, Secrets: []string{"wecom_token", "wecom_encoding_aes_key", "wecom_corp_id", "wecom_corp_secret"}, DefaultOff: true},
	}
}
