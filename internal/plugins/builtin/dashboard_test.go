package builtin

import (
	"errors"
	"reflect"
	"testing"
)

// TestF8PLUG016DashboardAuth: exactly the four dashboard auth providers exist,
// each maps to a web auth mode, unknown names are rejected, and the descriptor
// carries no credential field (tokens live in the web session store).
func TestF8PLUG016DashboardAuth(t *testing.T) {
	t.Parallel()
	want := []AuthProvider{
		{Name: "basic", Mode: "basic"},
		{Name: "drain", Mode: "token"},
		{Name: "nous_oauth", Mode: "oidc"},
		{Name: "oidc_self_hosted", Mode: "oidc"},
	}
	if got := AuthProviders(); !reflect.DeepEqual(got, want) {
		t.Fatalf("AuthProviders = %+v, want %+v", got, want)
	}
	for _, p := range want {
		if mode, err := AuthMode(p.Name); err != nil || mode != p.Mode {
			t.Errorf("AuthMode(%q) = (%q, %v)", p.Name, mode, err)
		}
	}
	if _, err := AuthMode("foreign"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("foreign provider err = %v, want ErrUnknown", err)
	}
	p, ok := Lookup(DashboardAuthID)
	if !ok || !p.DefaultOff || len(p.Providers) != 4 {
		t.Fatalf("Lookup(%q) = (%+v, %t)", DashboardAuthID, p, ok)
	}
	if _, err := Resolve(Targets{}, DashboardAuthID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unwired target err = %v, want ErrUnavailable", err)
	}
	if _, err := Resolve(Targets{DashboardAuth: true}, DashboardAuthID); err != nil {
		t.Fatalf("wired target: %v", err)
	}
}
