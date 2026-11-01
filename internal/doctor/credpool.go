//go:build !minimal

package doctor

import (
	"fmt"
	"strings"
)

// credentialPoolOptions carries the configured pools; the minimal build has
// no credential pool (P13).
type credentialPoolOptions struct {
	// CredentialPools reports configured SPEC §4.1 pools by identity only;
	// a missing keychain entry is a warning, never an error, and no value is
	// ever present here.
	CredentialPools []CredentialPoolStatus
}

// CredentialPoolStatus is one configured provider pool. IDs are stable
// identities; Missing names entries whose keychain reference has no stored
// value. Neither field ever carries a credential.
type CredentialPoolStatus struct {
	Provider string   `json:"provider"`
	IDs      []string `json:"ids"`
	Missing  []string `json:"missing,omitempty"`
}

// checkCredentialPools reports the configured SPEC §4.1 pools by identity.
// A missing keychain entry is a warning (the run would fail closed at the
// first call); the check never prints a value.
func credentialPoolChecks(o *Options) []Check {
	const id, name = "DOC-012", "credential pool"
	if len(o.CredentialPools) == 0 {
		return []Check{{id, name, StatusOK, "none configured"}}
	}
	warn := false
	parts := make([]string, 0, len(o.CredentialPools))
	for _, p := range o.CredentialPools {
		msg := fmt.Sprintf("%s: %d credential(s) [%s]", p.Provider, len(p.IDs), strings.Join(p.IDs, ", "))
		if len(p.Missing) > 0 {
			warn = true
			msg += "; missing keychain entries for [" + strings.Join(p.Missing, ", ") + "]"
		}
		parts = append(parts, msg)
	}
	status := StatusOK
	if warn {
		status = StatusWarn
	}
	return []Check{{id, name, status, strings.Join(parts, "; ")}}
}
