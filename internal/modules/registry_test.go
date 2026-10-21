package modules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// stubModule is a do-nothing Module for registry tests.
type stubModule struct{}

func (stubModule) Start(context.Context) error { return nil }
func (stubModule) Status() host.Status         { return host.StatusReady }
func (stubModule) Invoke(context.Context, string, json.RawMessage, host.Env) (host.Result, error) {
	return host.Result{}, nil
}
func (stubModule) Stop(context.Context) error { return nil }

func stubFactory(host.Host, host.Config) (Module, error) { return stubModule{}, nil }

// descriptor returns a minimal valid descriptor with a tool.
func descriptor(id string) Descriptor {
	return Descriptor{
		ID:      id,
		Group:   id,
		Catalog: "- " + id + ": one, two",
		Tools: []ToolDescriptor{{
			Name:        strings.ReplaceAll(id, ".", "_") + "_tool",
			Description: "does " + id,
			Tags:        []string{id},
			Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Effect:      "read",
		}},
		Factory: stubFactory,
	}
}

func TestRegisterAndFreeze(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(descriptor("browser")); err != nil {
		t.Fatalf("register browser: %v", err)
	}
	if err := r.Register(descriptor("codeintel")); err != nil {
		t.Fatalf("register codeintel: %v", err)
	}
	if got := r.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}
	if _, ok := r.Lookup("browser"); !ok {
		t.Fatal("browser not found")
	}
	if id, ok := r.ToolModule("codeintel_tool"); !ok || id != "codeintel" {
		t.Fatalf("ToolModule = %q,%v", id, ok)
	}
	described := r.Descriptors()
	if len(described) != 2 || described[0].ID != "browser" || described[1].ID != "codeintel" {
		t.Fatalf("Descriptors not sorted: %+v", described)
	}
	if r.Frozen() {
		t.Fatal("registry frozen before Freeze")
	}
	if err := r.Freeze(); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if !r.Frozen() {
		t.Fatal("registry not frozen")
	}
	if err := r.Freeze(); err != nil {
		t.Fatalf("second Freeze: %v", err)
	}
}

func TestRegisterRejectsLateDescriptor(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(descriptor("browser")); err != nil {
		t.Fatal(err)
	}
	if err := r.Freeze(); err != nil {
		t.Fatal(err)
	}
	err := r.Register(descriptor("late"))
	if !errors.Is(err, ErrFrozen) {
		t.Fatalf("late Register error = %v, want ErrFrozen", err)
	}
	if _, ok := r.Lookup("late"); ok {
		t.Fatal("late descriptor was stored")
	}
	if got := r.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(descriptor("browser")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(descriptor("browser")); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate id error = %v, want ErrDuplicateID", err)
	}
	other := descriptor("media")
	other.Tools[0].Name = "browser_tool"
	if err := r.Register(other); !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("duplicate tool error = %v, want ErrDuplicateTool", err)
	}
	if err := r.Validate(); !errors.Is(err, ErrDuplicateID) || !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("Validate = %v, want both duplicate errors", err)
	}
}

func TestValidateReportsWithoutPanic(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Descriptor)
		want error
	}{
		{"empty id", func(d *Descriptor) { d.ID = "" }, ErrInvalidID},
		{"uppercase id", func(d *Descriptor) { d.ID = "Browser" }, ErrInvalidID},
		{"dotted id", func(d *Descriptor) { d.ID = "gateway.telegram" }, nil},
		{"bad dotted id", func(d *Descriptor) { d.ID = "gateway..telegram" }, ErrInvalidID},
		{"empty group", func(d *Descriptor) { d.Group = " " }, ErrInvalidGroup},
		{"empty catalog", func(d *Descriptor) { d.Catalog = "" }, ErrInvalidCatalog},
		{"long catalog", func(d *Descriptor) {
			d.Catalog = "- x: " + strings.Repeat("word ", maxCatalogTokens+1)
		}, ErrInvalidCatalog},
		{"nil factory", func(d *Descriptor) { d.Factory = nil }, ErrInvalidFactory},
		{"bad config key", func(d *Descriptor) { d.ConfigKey = "browser" }, ErrInvalidConfigKey},
		{"bad tool name", func(d *Descriptor) { d.Tools[0].Name = "Read File" }, ErrInvalidTool},
		{"empty description", func(d *Descriptor) { d.Tools[0].Description = " " }, ErrInvalidTool},
		{"bad schema", func(d *Descriptor) { d.Tools[0].Schema = json.RawMessage("{") }, ErrInvalidTool},
		{"bad effect", func(d *Descriptor) { d.Tools[0].Effect = "write" }, ErrInvalidTool},
		{"repeated tool", func(d *Descriptor) { d.Tools = append(d.Tools, d.Tools[0]) }, ErrInvalidTool},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := descriptor("browser")
			c.edit(&d)
			r := NewRegistry()
			err := r.Register(d)
			if c.want == nil {
				if err != nil {
					t.Fatalf("Register: %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("Register error = %v, want %v", err, c.want)
			}
			if got := r.Len(); got != 0 {
				t.Fatalf("invalid descriptor stored (%d modules)", got)
			}
		})
	}
}

// TestValidateBypass reports a descriptor injected past Register, proving
// Validate never panics and still names the structural problem.
func TestValidateBypass(t *testing.T) {
	r := NewRegistry()
	r.descs = append(r.descs, Descriptor{ID: "Bad ID", Group: "g", Catalog: "x"})
	err := r.Validate()
	if !errors.Is(err, ErrInvalidID) || !errors.Is(err, ErrInvalidFactory) {
		t.Fatalf("Validate = %v, want invalid id and factory", err)
	}
}

// TestRegisterDoesNotStart is the P16 registration contract: Register only
// records data; the factory, the host and the module see no call.
func TestRegisterDoesNotStart(t *testing.T) {
	r := NewRegistry()
	started, emitted := 0, 0
	d := descriptor("browser")
	d.Factory = func(h host.Host, _ host.Config) (Module, error) {
		started++
		return stubModule{}, nil
	}
	h := &recordingHost{onEmit: func() { emitted++ }}
	_ = NewRuntime(Deps{Registry: r, Host: h})
	if err := r.Register(d); err != nil {
		t.Fatal(err)
	}
	if err := r.Freeze(); err != nil {
		t.Fatal(err)
	}
	if started != 0 || emitted != 0 {
		t.Fatalf("registration started %d factories and emitted %d events", started, emitted)
	}
}

// TestDefaultRegistry exercises the package-level init path in isolation.
func TestDefaultRegistry(t *testing.T) {
	old := defaultRegistry
	defaultRegistry = NewRegistry()
	t.Cleanup(func() { defaultRegistry = old })

	if err := Register(descriptor("browser")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := Freeze(); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if err := Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := Register(descriptor("late")); !errors.Is(err, ErrFrozen) {
		t.Fatalf("late Register = %v, want ErrFrozen", err)
	}
	if got := Default().Len(); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
}

func TestDescriptorConfigKey(t *testing.T) {
	d := descriptor("browser")
	if got := d.ConfigKeyOrID(); got != "modules.browser" {
		t.Fatalf("ConfigKeyOrID = %q", got)
	}
	d.ConfigKey = "modules.web"
	if got := d.ConfigKeyOrID(); got != "modules.web" {
		t.Fatalf("ConfigKeyOrID = %q", got)
	}
}

// recordingHost is a Host whose non-event methods are inert.
type recordingHost struct{ onEmit func() }

func (h *recordingHost) Emit(context.Context, host.Event) error {
	if h.onEmit != nil {
		h.onEmit()
	}
	return nil
}
func (h *recordingHost) I18n() host.Translator { return nil }
func (h *recordingHost) Spawn(context.Context, host.SpawnSpec) (host.Process, error) {
	return nil, errors.New("not implemented")
}
func (h *recordingHost) LLM(string) host.Completer { return nil }
func (h *recordingHost) Storage(string) (host.Store, error) {
	return nil, errors.New("not implemented")
}

// TestRecordingHost keeps every inert method of the test double live: the
// lifecycle tests replace this fake with one that records emitted events.
func TestRecordingHost(t *testing.T) {
	var h host.Host = &recordingHost{}
	if err := h.Emit(context.Background(), host.Event{}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if h.I18n() != nil || h.LLM("purpose") != nil {
		t.Fatal("recordingHost must have no translator or completer")
	}
	if _, err := h.Spawn(context.Background(), host.SpawnSpec{}); err == nil {
		t.Fatal("Spawn must report not implemented")
	}
	if _, err := h.Storage("module/test"); err == nil {
		t.Fatal("Storage must report not implemented")
	}
}
