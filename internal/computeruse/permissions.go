package computeruse

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
)

// PermissionStatus is the read-only OS permission view of `cua permissions`.
// Off macOS there is no TCC model, so Supported is false and Ready stays true.
type PermissionStatus struct {
	Platform        string
	Supported       bool
	Accessibility   *bool
	ScreenRecording *bool
	Ready           bool
	Detail          string
}

// Permissions reads the driver's permission state. It never requests or
// grants anything: the only driver argv it can build is
// `permissions status --json`, and only on macOS where TCC applies.
func (m *Module) Permissions(ctx context.Context) (PermissionStatus, error) {
	st := PermissionStatus{Platform: runtime.GOOS, Supported: runtime.GOOS == "darwin", Ready: true}
	if m == nil || !m.enabled {
		return st, fmt.Errorf("%w: computer_use is disabled", ErrUnavailable)
	}
	if !st.Supported {
		st.Detail = "no TCC permission model on " + st.Platform
		return st, nil
	}
	b, ok := m.backend.(*processBackend)
	if !ok {
		return st, fmt.Errorf("%w: permissions probe needs the process backend", ErrUnavailable)
	}
	out, err := b.run(ctx, []string{"permissions", "status", "--json"}, nil, maxOutput)
	if err != nil {
		return st, fmt.Errorf("%w: permissions status: %w", ErrUnavailable, err)
	}
	var payload struct {
		Accessibility   *bool `json:"accessibility"`
		ScreenRecording *bool `json:"screen_recording"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return st, fmt.Errorf("%w: permissions status: %w", ErrUnavailable, err)
	}
	st.Accessibility, st.ScreenRecording = payload.Accessibility, payload.ScreenRecording
	st.Ready = payload.Accessibility != nil && *payload.Accessibility &&
		payload.ScreenRecording != nil && *payload.ScreenRecording
	st.Detail = "macOS TCC: Accessibility and Screen Recording"
	return st, nil
}
