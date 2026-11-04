package wake

// Engine preset names. The set is closed: a configuration typo fails at load
// instead of silently disabling the wake word.
const (
	EngineOpenWakeWord = "openwakeword"
	EngineSherpa       = "sherpa-onnx"
	EnginePorcupine    = "porcupine"
)

// Preset is one reviewed wake engine. Install names the manual installation
// path; the adapter never downloads a binary or a model.
type Preset struct {
	Name    string
	Binary  string
	Args    []string
	KeyEnv  string
	Install string
}

// Presets returns the closed preset set in catalog order.
func Presets() []Preset {
	return []Preset{
		{
			Name: EngineOpenWakeWord, Binary: "openwakeword",
			Install: "install the openWakeWord CLI manually and set wake_model (no automatic download)",
		},
		{
			Name: EngineSherpa, Binary: "sherpa-onnx-kws",
			Install: "install sherpa-onnx KWS manually and set wake_keywords (no automatic download)",
		},
		{
			Name: EnginePorcupine, Binary: "porcupine", KeyEnv: "PICOVOICE_ACCESS_KEY",
			Install: "install the Picovoice Porcupine CLI manually and set wake_key_env",
		},
	}
}

// PresetFor looks up one preset by name.
func PresetFor(name string) (Preset, bool) {
	for _, p := range Presets() {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

// EngineNames returns the closed preset names.
func EngineNames() []string {
	presets := Presets()
	out := make([]string, len(presets))
	for i, p := range presets {
		out[i] = p.Name
	}
	return out
}
