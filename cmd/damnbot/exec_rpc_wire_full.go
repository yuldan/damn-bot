//go:build !minimal

package main

import "github.com/damnbot/damnbot/internal/tools"

// executeRPC is the full-build execute_code RPC invoker.
type executeRPC = *tools.DispatcherInvoker

func newExecuteRPC() executeRPC { return &tools.DispatcherInvoker{} }

func bindExecuteRPC(rpc executeRPC, disp *tools.Dispatcher) {
	if rpc != nil {
		rpc.D = disp
	}
}
