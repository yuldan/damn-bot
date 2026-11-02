package exec

// Mux support for the token-authenticated Windows control pipe (EC-WIN-015):
// the listener hands Mux.Serve connections whose peer is verified on the first
// Read (pipe_windows.go), so a handler always reads before it writes.

// Listener adapts the pipe server to Mux.Serve. A closed server reports
// ErrPipeClosed and Serve returns nil when its context is done; a rejected
// peer surfaces as ErrPeerRejected from that client's Receive, which ends only
// that handler.
func (s *PipeServer) Listener() Listener { return pipeListener{srv: s} }

type pipeListener struct{ srv *PipeServer }

func (l pipeListener) Accept() (Conn, error) {
	conn, err := l.srv.Accept()
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (l pipeListener) Close() { l.srv.Close() }
