//go:build !minimal

package main

// addAuthCompletion exposes the auth subcommands in the full build only.
func addAuthCompletion(m map[string][]string) {
	m["setup auth"] = []string{"login", "logout", "status"}
}
