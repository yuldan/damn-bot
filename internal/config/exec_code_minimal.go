//go:build minimal

package config

// ExecExtra is empty in the minimal build: execute_code is not linked and an
// unknown code_auto_languages key is rejected by the strict decoder.
type ExecExtra struct{}

func defaultExecCode(*Exec) {}

func validateExecCode(*Config) error { return nil }
