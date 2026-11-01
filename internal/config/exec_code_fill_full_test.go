//go:build !minimal

package config

func fillExecCode(e *Exec) { e.CodeAutoLanguages = []string{} }
