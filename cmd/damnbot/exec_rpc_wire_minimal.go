//go:build minimal

package main

import "github.com/damnbot/damnbot/internal/tools"

// executeRPC is absent in the minimal build.
type executeRPC = any

func newExecuteRPC() executeRPC { return nil }

func bindExecuteRPC(executeRPC, *tools.Dispatcher) {}
