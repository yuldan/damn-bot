package builtin

// AuthProvider is one dashboard auth provider row (PLUG-016). No token or
// secret is part of this descriptor; the web layer stores credentials in its
// cookie/session store.
type AuthProvider struct {
	Name string
	Mode string
}

// AuthProviders returns the four closed providers in parity order.
func AuthProviders() []AuthProvider {
	return dashboards()
}
