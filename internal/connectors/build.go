package connectors

func (m *Module) build(name string) (Handler, error) {
	switch name {
	case "graph":
		return m.newGraph()
	case "teams":
		return m.newTeams()
	}
	return nil, nil
}
