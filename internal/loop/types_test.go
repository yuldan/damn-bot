package loop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

func TestTypes(t *testing.T) {
	t.Parallel()
	root := errors.New("root")
	se := &StreamError{Class: ClassProvider, Err: root}
	if !errors.Is(se, root) || !strings.Contains(se.Error(), "provider") {
		t.Errorf("StreamError = %v", se)
	}
	te := &ToolError{Code: CodeDenied, Message: "no"}
	if te.Error() != "denied: no" {
		t.Errorf("ToolError = %q", te.Error())
	}
	r := TurnResult{Stop: event.StopEndTurn, Detail: event.DetailCompleted, Usage: event.Usage{Iterations: 2}}
	if !strings.Contains(r.String(), "end_turn") {
		t.Errorf("TurnResult = %q", r.String())
	}
	_ = context.Background()
}
