// Package host is the stable API between the core and lazy feature modules
// (SPEC §3.2.1, ENGINEERING E-06). A feature module imports only this
// package, internal/netx and internal/schema: it never sees the loop, the
// tool pipeline, the provider or the event store. Everything it needs
// arrives through [Host], which the composition root implements once.
//
// The package is a leaf contract: types, interfaces, argument validation and
// the fail-closed defaults of the capability seam, with no I/O. Interfaces stay
// small (E-09: at most five methods) so an implementation and a test fake are
// cheap; capabilities beyond the five Host methods live in the separate
// [CapabilityProvider] seam (ADR-268).
package host

import (
	"context"
	"encoding/json"
	"os"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// Status is the module status vocabulary of SPEC §3.2.2 (ADR-063 V-05).
// The set is closed: a module never invents a status string.
type Status string

// Module statuses (SPEC §3.2.2).
const (
	// StatusStopped is a module that is not running: registered but not yet
	// used, disabled, or stopped by the idle timeout.
	StatusStopped Status = "stopped"
	// StatusStarting is a module whose Start is in flight.
	StatusStarting Status = "starting"
	// StatusReady is a started module that can serve calls.
	StatusReady Status = "ready"
	// StatusDegraded is a ready module with an open circuit: calls may fail
	// but the next probe can recover it without owner action.
	StatusDegraded Status = "degraded"
	// StatusNeedsAttention is a failure that does not recover on its own
	// (breaker GW-11: logged_out, banned, temporary_ban,
	// protocol_outdated). Only an owner action or the one-shot
	// temporary_ban timer leaves this state (SPEC §3.2.2).
	StatusNeedsAttention Status = "needs_attention"
	// StatusFailed is a module whose Start or Invoke failed; a manual
	// restart or the next use after the retry window starts it again.
	StatusFailed Status = "failed"
)

// Statuses returns every module status in SPEC order.
func Statuses() []Status {
	return []Status{
		StatusStopped, StatusStarting, StatusReady,
		StatusDegraded, StatusNeedsAttention, StatusFailed,
	}
}

// Valid reports whether s is a closed-set status.
func (s Status) Valid() bool {
	switch s {
	case StatusStopped, StatusStarting, StatusReady,
		StatusDegraded, StatusNeedsAttention, StatusFailed:
		return true
	}
	return false
}

// Config is one module's TOML section, keyed by Descriptor.ConfigKey
// (default "modules.<id>"), passed undecoded. A module owns its schema and
// decodes cfg inside its Factory; the core never parses module config.
type Config struct {
	Key string
	Raw json.RawMessage
}

// Env carries per-call facts the core owns and a module must honour. A
// module never reads configuration or session state behind the core's back.
type Env struct {
	TurnID   string
	CallID   string
	Cwd      string
	Approved bool // the user approved this call's outside/irreversible verdict
}

// Result is one module tool result as the pipeline receives it (SPEC §4.3).
// Meta is optional structured data; UntrustedSource wraps Content in
// <untrusted> and taints the turn (SPEC §14.5, ADR-070).
type Result struct {
	Content         string
	Meta            map[string]any
	UntrustedSource string
}

// Error is a module tool failure in the closed tool-code vocabulary of
// SPEC §4.3. The adapter in internal/tools validates Code against
// tools.CodeSet before the model sees it.
type Error struct {
	Code    string
	Message string
	Hint    string
}

// Error implements error.
func (e *Error) Error() string {
	return "module tool error: " + e.Code + ": " + e.Message
}

// Persisted event kinds modules may emit. Re-exported so a module never has
// to import internal/loop/event (E-06 allows it only through this package).
const (
	EventModuleStart  = event.ModuleStart
	EventModuleError  = event.ModuleError
	EventSystemNotice = event.SystemNotice
)

// Event is one notice a module emits into the event log. Kind must be a
// persisted kind (event.Kind); the core accepts module_start, module_error
// and system_notice from modules and rejects anything else.
type Event struct {
	Kind    event.Kind
	Content string
	Payload json.RawMessage
}

// Host is the complete capability surface of a module (SPEC §3.2.1). The
// concrete value is owned by the composition root and shared by every
// module; methods must be safe for concurrent use.
//
// Five methods is the E-09 maximum: emit an event, translate a message,
// spawn a process, borrow the ledger-backed LLM, and open a namespaced
// store.
type Host interface {
	// Emit appends ev to the event log. A module reports lifecycle and
	// faults here; it never writes the event store directly.
	Emit(ctx context.Context, ev Event) error
	// I18n returns the translator for module-facing user text.
	I18n() Translator
	// Spawn starts an external process owned by the caller. The returned
	// Process is reaped by the module through Wait.
	Spawn(ctx context.Context, spec SpawnSpec) (Process, error)
	// LLM returns the completer for one purpose; every call is recorded on
	// the budget ledger under that purpose (SPEC §3.2.1).
	LLM(purpose string) Completer
	// Storage opens the module's namespaced key/value store. Namespace is
	// "module/<id>"; the host enforces isolation between modules.
	Storage(namespace string) (Store, error)
}

// Translator renders user-facing module text in the session locale.
type Translator interface {
	// Message returns the localized template key with args applied. An
	// unknown key returns a deterministic fallback, never an empty string.
	Message(locale, key string, args ...any) string
}

// SpawnSpec is one external process request. Args is passed verbatim (no
// shell); Dir and Env are applied as given. Modules must use this instead of
// os/exec so the core keeps process ownership, cancellation and reaping.
type SpawnSpec struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

// Process is one spawned external process. The caller owns it and must call
// Wait (or select Done) before dropping the reference. Signal and Kill are
// safe to call concurrently with Wait.
type Process interface {
	Done() <-chan struct{}
	Wait() error
	Signal(sig os.Signal) error
	Kill() error
}

// Completer performs one LLM call for a module.
type Completer interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// Message is one provider-neutral chat message.
type Message struct {
	Role    string
	Content string
}

// Request is one completer call. Schema, when non-nil, is a JSON Schema the
// provider enforces for structured output.
type Request struct {
	Model           string
	Messages        []Message
	MaxOutputTokens int
	Schema          json.RawMessage
}

// Response is one completer result. ToolCalls are raw provider tool-call
// objects; modules that do not use tools ignore the field.
type Response struct {
	Content   string
	ToolCalls []json.RawMessage
	Usage     Usage
}

// Usage is the ledger-relevant usage of one call (COST-09).
type Usage struct {
	InputTokens  int
	OutputTokens int
	CostMicroUSD int64
}

// Store is a module-scoped, durable key/value store. Keys are opaque bytes,
// values are copied; implementations must be safe for concurrent use.
type Store interface {
	Get(ctx context.Context, key []byte) ([]byte, bool, error)
	Put(ctx context.Context, key, value []byte) error
	Delete(ctx context.Context, key []byte) error
	List(ctx context.Context, prefix []byte) ([][]byte, error)
}
