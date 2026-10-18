// Package exec is the local command runner of the core (EXEC-AND-CODING
// §A, ADR-016/072/082/088, ADR-105): one process tree per call, killed as a
// whole (process group / Job Object, marker sweep), with a closed result
// status set, timeouts, cancel with a 1 s result deadline, and pipe or PTY
// runners. Classification and approval live in exec/classify.
package exec

import (
	"errors"
	"time"
)

// Status is ExecStatus, the closed result set of SPEC §4.3 (ADR-082).
type Status string

// Result statuses.
const (
	StatusExited             Status = "exited"
	StatusSignaled           Status = "signaled"
	StatusTimeout            Status = "timeout"
	StatusIdleTimeout        Status = "idle_timeout"
	StatusWaitingForInput    Status = "waiting_for_input"
	StatusCancelledConfirmed Status = "cancelled_confirmed"
	StatusStillRunning       Status = "still_running"
	StatusSpawnFailed        Status = "spawn_failed"
)

// Runner kinds reported in Result.Runner (§A.1).
const (
	RunnerPipe = "pipe"
	RunnerPTY  = "pty"
)

// Limits (§A.4, ADR-072).
const (
	DefaultTimeout   = 120 * time.Second
	MaxTimeout       = 600 * time.Second
	DefaultKillGrace = 3 * time.Second
	MinKillGrace     = time.Second
	MaxKillGrace     = 3 * time.Second
	// CancelDeadline is the time after a cancel by which a result is
	// returned (cancelled_confirmed or still_running, REL-06).
	CancelDeadline = time.Second
	// MarkerEnv carries the per-call execution id into every descendant;
	// the orphan sweep finds processes by it (§A.4).
	MarkerEnv = "DAMNBOT_EXEC_ID"
)

// pollEvery paces tree-liveness checks during kill_grace.
const pollEvery = 10 * time.Millisecond

// Spec is one command execution.
type Spec struct {
	Argv      []string // Argv[0] absolute, or resolved through PATH in Env
	Dir       string   // working directory (validated by the caller, SPEC §14.2)
	Env       []string // complete child environment (see BuildEnv)
	Stdin     []byte   // nil: /dev/null (NUL); never the parent's stdin
	PTY       bool
	Timeout   time.Duration // 0: DefaultTimeout
	KillGrace time.Duration // 0: DefaultKillGrace; clamped to 1–3 s
	// OnOutput, if set, receives raw output chunks as they arrive (UI
	// streaming, redacted by the caller); the slice is reused after return.
	OnOutput func([]byte)
}

// Result is the outcome of Run.
type Result struct {
	Status   Status
	ExitCode *int   // nil when signaled or not exited
	Signal   string // e.g. "SIGKILL"
	Duration time.Duration
	Runner   string
	ExecID   string
	Output   []byte // raw combined output (head + tail when Truncated)
	// Truncated: more than OutputCap bytes were produced; Output holds the
	// first and last OutputCap/2 bytes.
	Truncated bool
	BytesRaw  int64
	// Prompt and InputKind describe a detected interactive prompt
	// (StatusWaitingForInput, §A.6).
	Prompt    string
	InputKind string
	// Done is closed when a still_running tree has been confirmed dead;
	// nil otherwise.
	Done <-chan struct{}
}

// OutputCap bounds the raw output kept in memory per call.
const OutputCap = 16 << 20

// Errors.
var (
	// ErrSpec rejects an invalid Spec (empty argv, bad limits).
	ErrSpec = errors.New("exec: invalid spec")
	// ErrClosed: the Runner was closed.
	ErrClosed = errors.New("exec: runner closed")
)

// exit is the reaped leader's status: code, or -1 with the signal name.
type exit struct {
	code   int
	signal string
}
