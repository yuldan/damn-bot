package kanban

import "strings"

// Settings are the opt-in switches of the F7 gateway surface. All three
// default off (AUTO-017/018/019); the CLI and tests pass an explicit getter,
// so the process environment is read only at the composition root.
type Settings struct {
	DispatchInGateway bool
	AutoDecompose     bool
	NotifyInGateway   bool
}

// SettingsFromEnv reads the DAMNBOT_KANBAN_* switches. Absent or any value
// other than "1"/"true" keeps the switch off; there is no autodetect.
func SettingsFromEnv(get func(string) string) Settings {
	if get == nil {
		return Settings{}
	}
	on := func(name string) bool {
		switch strings.ToLower(strings.TrimSpace(get(name))) {
		case "1", "true":
			return true
		}
		return false
	}
	return Settings{
		DispatchInGateway: on("DAMNBOT_KANBAN_DISPATCH"),
		AutoDecompose:     on("DAMNBOT_KANBAN_AUTO_DECOMPOSE"),
		NotifyInGateway:   on("DAMNBOT_KANBAN_NOTIFY"),
	}
}
