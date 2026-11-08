package connectors

func (m *Module) build(name string) (Handler, error) {
	if name == "graph" {
		return m.newGraph()
	}
	return nil, nil
}
