//go:build !minimal

package doctor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCredentialPoolCheck(t *testing.T) {
	t.Parallel()
	rep := Run(context.Background(), &Options{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(),
		Keyring: func(context.Context) (bool, error) { return false, nil },
		CredentialPools: []CredentialPoolStatus{
			{Provider: "kantor", IDs: []string{"work", "home"}, Missing: []string{"home"}},
			{Provider: "lokal", IDs: []string{"only"}},
		},
	})
	var found *Check
	for i := range rep.Checks {
		if rep.Checks[i].ID == "DOC-012" {
			found = &rep.Checks[i]
		}
	}
	if found == nil {
		t.Fatal("DOC-012 missing")
	}
	if found.Status != StatusWarn || !strings.Contains(found.Message, "kantor: 2 credential(s) [work, home]") ||
		!strings.Contains(found.Message, "missing keychain entries for [home]") ||
		!strings.Contains(found.Message, "lokal: 1 credential(s) [only]") {
		t.Fatalf("check = %+v", *found)
	}
	if strings.Contains(found.Message, "sk-") {
		t.Fatalf("check leaked a value: %s", found.Message)
	}
	if _, err := json.Marshal(found); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialPoolCheckNone(t *testing.T) {
	t.Parallel()
	rep := Run(context.Background(), &Options{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(),
		Keyring: func(context.Context) (bool, error) { return false, nil },
	})
	for _, c := range rep.Checks {
		if c.ID == "DOC-012" {
			if c.Status != StatusOK || c.Message != "none configured" {
				t.Fatalf("check = %+v", c)
			}
			return
		}
	}
	t.Fatal("DOC-012 missing")
}
