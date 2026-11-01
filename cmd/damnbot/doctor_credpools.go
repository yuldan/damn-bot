//go:build !minimal

package main

import (
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/doctor"
)

// attachCredentialPools adds the configured pool identities to the doctor
// options of the full build.
func attachCredentialPools(opts *doctor.Options, cfg *config.Config) {
	opts.CredentialPools = doctorCredentialPools(cfg)
}

// doctorCredentialPools reports configured SPEC §4.1 pools by identity. It
// reads only the named-secret index, so a missing keychain entry is visible
// without touching a value.
func doctorCredentialPools(cfg *config.Config) []doctor.CredentialPoolStatus {
	var out []doctor.CredentialPoolStatus
	present := map[string]bool{}
	if st, err := secretsStore(); err == nil {
		if entries, lerr := st.List(); lerr == nil {
			for _, e := range entries {
				present[e.Name] = true
			}
		}
	}
	for i := range cfg.Providers.Custom {
		c := &cfg.Providers.Custom[i]
		if c.Credentials == nil {
			continue
		}
		ids := make([]string, 0, len(c.Credentials))
		var missing []string
		for _, cred := range c.Credentials {
			ids = append(ids, cred.ID)
			if !present[strings.TrimPrefix(cred.Secret, "keychain:")] {
				missing = append(missing, cred.ID)
			}
		}
		out = append(out, doctor.CredentialPoolStatus{Provider: c.Name, IDs: ids, Missing: missing})
	}
	return out
}
