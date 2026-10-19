//go:build linux || darwin

package exec

import "bytes"

// markerValue finds "DAMNBOT_EXEC_ID=<v>" in a NUL-separated environ.
func markerValue(env []byte) ([]byte, bool) {
	key := []byte(MarkerEnv + "=")
	for len(env) > 0 {
		var kv []byte
		kv, env, _ = bytes.Cut(env, []byte{0})
		if v, ok := bytes.CutPrefix(kv, key); ok {
			return v, true
		}
	}
	return nil, false
}
