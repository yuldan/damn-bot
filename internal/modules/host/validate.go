package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Limits of the host contract. They keep one misbehaving module from turning
// a model-facing result or an event into unbounded memory.
const (
	// MaxResultBytes caps one module tool result's content. Larger output
	// must be spilled through internal/tools, not returned inline.
	MaxResultBytes = 8 << 20
	// MaxEventText is the longest module event text (an id or a reason).
	MaxEventText = 512
	// MaxSpawnPath bounds an executable path.
	MaxSpawnPath = 4096
)

// Validation errors. Callers match with errors.Is.
var (
	// ErrInvalidConfig is a malformed host.Config.
	ErrInvalidConfig = errors.New("host: invalid config")
	// ErrInvalidSpawn is a malformed SpawnSpec.
	ErrInvalidSpawn = errors.New("host: invalid spawn spec")
	// ErrInvalidEvent is a malformed or forbidden Event.
	ErrInvalidEvent = errors.New("host: invalid event")
	// ErrInvalidResult is a module result outside the contract.
	ErrInvalidResult = errors.New("host: invalid result")
	// ErrInvalidNamespace is a storage namespace that is not "module/<id>".
	ErrInvalidNamespace = errors.New("host: invalid storage namespace")
)

// Validate checks a module config section. The zero Config is valid: a
// module without a section simply sees no bytes.
func (c Config) Validate() error {
	if c.Key != "" {
		if !strings.HasPrefix(c.Key, "modules.") || strings.ContainsAny(c.Key, " \t\r\n") {
			return fmt.Errorf("%w: key %q", ErrInvalidConfig, c.Key)
		}
		if len(c.Key) > 128 {
			return fmt.Errorf("%w: key too long", ErrInvalidConfig)
		}
	}
	if len(c.Raw) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(c.Raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return fmt.Errorf("%w: raw section is not a JSON object", ErrInvalidConfig)
	}
	return nil
}

// Validate checks one spawn request before the host executes it. The module
// never builds a shell command; args are passed verbatim.
func (s *SpawnSpec) Validate() error {
	if s.Path == "" || len(s.Path) > MaxSpawnPath || strings.ContainsRune(s.Path, 0) {
		return fmt.Errorf("%w: path %q", ErrInvalidSpawn, s.Path)
	}
	if strings.ContainsRune(s.Dir, 0) {
		return fmt.Errorf("%w: dir contains NUL", ErrInvalidSpawn)
	}
	for i, arg := range s.Args {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("%w: arg %d contains NUL", ErrInvalidSpawn, i)
		}
	}
	for i, kv := range s.Env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" || strings.ContainsRune(key, 0) {
			return fmt.Errorf("%w: env %d is not KEY=VALUE", ErrInvalidSpawn, i)
		}
	}
	return nil
}

// Validate checks an event a module wants to emit. Modules may only publish
// lifecycle notices; anything else is a contract violation.
func (e Event) Validate() error {
	switch e.Kind { //nolint:exhaustive // modules may emit only these three kinds
	case EventModuleStart, EventModuleError, EventSystemNotice:
	default:
		return fmt.Errorf("%w: kind %q is not a module event", ErrInvalidEvent, string(e.Kind))
	}
	if len(e.Content) > MaxEventText || strings.ContainsRune(e.Content, 0) {
		return fmt.Errorf("%w: content", ErrInvalidEvent)
	}
	if len(e.Payload) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(e.Payload)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return fmt.Errorf("%w: payload is not a JSON object", ErrInvalidEvent)
	}
	return nil
}

// Validate checks a module tool result before the pipeline redacts and
// spills it. UntrustedSource is the wrapper label, so it is a short slug.
func (r Result) Validate() error {
	if len(r.Content) > MaxResultBytes {
		return fmt.Errorf("%w: %d bytes > %d", ErrInvalidResult, len(r.Content), MaxResultBytes)
	}
	if r.UntrustedSource != "" {
		if len(r.UntrustedSource) > 64 || strings.ContainsAny(r.UntrustedSource, "\"<>\r\n") {
			return fmt.Errorf("%w: untrusted source %q", ErrInvalidResult, r.UntrustedSource)
		}
	}
	for k := range r.Meta {
		if k == "" || strings.ContainsRune(k, 0) {
			return fmt.Errorf("%w: meta key", ErrInvalidResult)
		}
	}
	return nil
}

// Validate checks a module tool error. Code is validated against the closed
// tool set by internal/tools; here only the shape is checked.
func (e *Error) Validate() error {
	if e == nil {
		return nil
	}
	if e.Code == "" || len(e.Code) > 64 || strings.ContainsAny(e.Code, " \t\r\n") {
		return fmt.Errorf("%w: error code %q", ErrInvalidResult, e.Code)
	}
	if len(e.Message) > MaxEventText {
		return fmt.Errorf("%w: error message too long", ErrInvalidResult)
	}
	return nil
}

// Namespace returns the storage namespace for moduleID: "module/<id>". The
// host rejects any other namespace from Storage.
func Namespace(moduleID string) (string, error) {
	if !validModuleID(moduleID) {
		return "", fmt.Errorf("%w: module id %q", ErrInvalidNamespace, moduleID)
	}
	return "module/" + moduleID, nil
}

// ValidateNamespace checks that ns was produced by Namespace.
func ValidateNamespace(ns string) error {
	id, ok := strings.CutPrefix(ns, "module/")
	if !ok {
		return fmt.Errorf("%w: %q", ErrInvalidNamespace, ns)
	}
	if !validModuleID(id) {
		return fmt.Errorf("%w: %q", ErrInvalidNamespace, ns)
	}
	return nil
}

// validModuleID mirrors modules.validID without importing it (modules/host
// must stay a leaf; the registry re-validates the same shape).
func validModuleID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for part := range strings.SplitSeq(id, ".") {
		if part == "" {
			return false
		}
		for i := range len(part) {
			c := part[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
				return false
			}
		}
	}
	return true
}
