//go:build !minimal

package doctor

import (
	"fmt"
	"strings"
)

// CredentialPoolStatus is one configured provider pool. IDs are stable
// identities; Missing names entries whose keychain reference has no stored
// value. Neither field ever carries a credential.
type CredentialPoolStatus struct {
	Provider string   `json:"provider"`
	IDs      []string `json:"ids"`
	Missing  []string `json:"missing,omitempty"`
}

// PoolChecks reports DOC-012 without any value.
func PoolChecks(pools []CredentialPoolStatus) []Check {
	const id, name = "DOC-012", "credential pool"
	if len(pools) == 0 {
		return []Check{{id, name, StatusOK, "none configured"}}
	}
	warn := false
	parts := make([]string, 0, len(pools))
	for _, p := range pools {
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
