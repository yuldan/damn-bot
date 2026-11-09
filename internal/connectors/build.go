package connectors

func (m *Module) build(name string) (Handler, error) {
	switch name {
	case "graph":
		return m.newGraph()
	case "teams":
		return m.newTeams()
	case "feishu":
		return m.newFeishu()
	case "meet":
		return m.newMeet()
	case "xsearch":
		return m.newXSearch()
	case "discord":
		return m.newDiscord()
	case "osv":
		return m.newOSV()
	}
	return nil, nil
}
