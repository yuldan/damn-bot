package secret

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
)

func (s *Store) readKeyFileNamed(name string) ([]byte, error) { return s.readSmall(name) }

func encodeJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("secret: encode key metadata: %w", err)
	}
	return b, nil
}

func decodeJSON(b []byte, v any) error {
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("secret: decode key metadata: %w", err)
	}
	return nil
}

func syncRootDir(root *os.Root) error {
	d, err := root.Open(".")
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return fmt.Errorf("secret: open config dir: %w", err)
	}
	serr := d.Sync()
	cerr := d.Close()
	if runtime.GOOS == "windows" {
		return cerr //nolint:wrapcheck // directory close on Windows
	}
	return errors.Join(serr, cerr)
}
