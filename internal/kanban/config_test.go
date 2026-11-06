package kanban

import "testing"

// TestF7KanbanSettingsFromEnv proves the opt-in switches: absent, nil getter
// and any non-"1"/"true" value stay off; only an explicit truthy value turns
// one on.
func TestF7KanbanSettingsFromEnv(t *testing.T) {
	t.Parallel()
	if got := SettingsFromEnv(nil); got != (Settings{}) {
		t.Fatalf("nil getter = %+v, want all off", got)
	}
	env := map[string]string{
		"DAMNBOT_KANBAN_DISPATCH":       " TRUE ",
		"DAMNBOT_KANBAN_AUTO_DECOMPOSE": "1",
		"DAMNBOT_KANBAN_NOTIFY":         "yes",
	}
	got := SettingsFromEnv(func(k string) string { return env[k] })
	want := Settings{DispatchInGateway: true, AutoDecompose: true}
	if got != want {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}
}
