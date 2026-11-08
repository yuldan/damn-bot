//go:build !minimal

package netx

// addConnectorEgress registers the F8 connector lanes. The minimal build has
// no connector call sites, so it keeps them out of the binary (P13 headroom).
func addConnectorEgress(reg map[string]Egress) {
	reg["graph"] = Egress{Purpose: Tool, Host: "graph.microsoft.com and login.microsoftonline.com/<tenant>", Trigger: "an approved ms_graph_mail, ms_graph_calendar or ms_graph_files call"}
	reg["teams"] = Egress{Purpose: Tool, Host: "graph.microsoft.com", Trigger: "an approved teams_pipeline call"}
	reg["feishu"] = Egress{Purpose: Tool, Host: "open.feishu.cn", Trigger: "an approved feishu_doc_read or feishu_drive_* call"}
	reg["meet"] = Egress{Purpose: Tool, Host: "meet.node_url from [modules.connectors.meet] (https/wss only)", Trigger: "an approved google_meet call"}
	reg["xsearch"] = Egress{Purpose: Tool, Host: "api.x.ai", Trigger: "an approved x_search call"}
	reg["discord"] = Egress{Purpose: Tool, Host: "discord.com/api", Trigger: "an approved discord or discord_admin call"}
	reg["osv"] = Egress{Purpose: Tool, Host: "api.osv.dev", Trigger: "an approved osv_check call"}
}
