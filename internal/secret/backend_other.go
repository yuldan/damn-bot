//go:build !linux

package secret

func backendPresent() bool { return true }

func backendAbsent(error) bool { return false }
