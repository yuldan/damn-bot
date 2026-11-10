package builtin

import (
	"errors"
	"strings"
	"testing"
)

// TestF8PLUG035Connectors: hosted accounts are accepted only for the closed
// connectors registry and only as keychain references; listing never returns
// a token value.
func TestF8PLUG035Connectors(t *testing.T) {
	t.Parallel()
	const rawToken = "secret-token-value"
	c := NewConnections()
	connectorAccounts(t, c, rawToken)
	connectorTarget(t)
}

func connectorAccounts(t *testing.T, c *Connections, rawToken string) {
	t.Helper()
	acc, err := c.Add("graph", "work", "Work account", "keychain:graph-work")
	if err != nil || acc.TokenRef != "keychain:graph-work" {
		t.Fatalf("Add = (%+v, %v)", acc, err)
	}
	if _, err := c.Add("graph", "raw", "Raw", rawToken); !errors.Is(err, ErrConnection) {
		t.Fatalf("raw token err = %v, want ErrConnection", err)
	}
	if _, err := c.Add("foreign", "x", "X", "keychain:x"); !errors.Is(err, ErrConnection) {
		t.Fatalf("foreign connector err = %v, want ErrConnection", err)
	}
	if err := c.Rename("graph", "work", "Renamed"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	list := c.List("graph")
	if len(list) != 1 || list[0].Label != "Renamed" || list[0].TokenRef != "keychain:graph-work" {
		t.Fatalf("List = %+v", list)
	}
	for _, a := range list {
		if strings.Contains(a.TokenRef, rawToken) || strings.Contains(a.Label, rawToken) {
			t.Fatalf("List leaked a token: %+v", a)
		}
	}
	if err := c.Remove("graph", "work"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(c.List("")) != 0 {
		t.Fatal("Remove left an account")
	}
}

func connectorTarget(t *testing.T) {
	t.Helper()
	if _, err := Resolve(Targets{}, HostedConnectorID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unwired target err = %v, want ErrUnavailable", err)
	}
	if _, err := Resolve(Targets{Connectors: true}, HostedConnectorID); err != nil {
		t.Fatalf("wired target: %v", err)
	}
}
