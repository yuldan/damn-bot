package whatsapp_cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Secret names the Cloud API descriptor declares. `damnbot whatsapp-cloud
// setup` records these references; a token value is never written to disk.
const (
	AccessTokenSecret = "whatsapp_cloud_access_token" // #nosec G101 -- secret name, not a value
	AppSecretSecret   = "whatsapp_cloud_app_secret"   // #nosec G101 -- secret name, not a value
	VerifyTokenSecret = "whatsapp_cloud_verify_token" // #nosec G101 -- secret name, not a value
)

// SetupFile is the setup record below the gateway data directory.
const SetupFile = "whatsapp_cloud.json"

// Setup is the persisted activation record of `damnbot whatsapp-cloud setup`:
// the phone-number id, the pinned Graph version and the keychain names of the
// three credentials. The adapter reads the values from the secret seam at
// runtime, so this struct never holds one.
type Setup struct {
	PhoneNumberID     string `json:"phone_number_id"`
	APIVersion        string `json:"api_version,omitempty"`
	AccessTokenSecret string `json:"access_token_secret"`
	AppSecretSecret   string `json:"app_secret_secret"`
	VerifyTokenSecret string `json:"verify_token_secret"`
}

// Validate rejects an incomplete record: a phone-number id is numeric, and
// every credential is a name, never a value.
func (s *Setup) Validate() error {
	if s == nil {
		return fmt.Errorf("%w: nil setup", ErrConfig)
	}
	if !numericID(s.PhoneNumberID) {
		return fmt.Errorf("%w: phone-number id must be numeric", ErrConfig)
	}
	for _, name := range []string{s.AccessTokenSecret, s.AppSecretSecret, s.VerifyTokenSecret} {
		if !secretName(name) {
			return fmt.Errorf("%w: %q is not a secret name", ErrConfig, name)
		}
	}
	return nil
}

// SaveSetup writes the record atomically (0600) below <dataDir>/gateway and
// returns the file path. A failure leaves the previous record untouched.
func SaveSetup(dataDir string, s *Setup) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	dir := filepath.Join(dataDir, "gateway")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("whatsapp_cloud: setup dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", fmt.Errorf("whatsapp_cloud: encode setup: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".whatsapp_cloud-*")
	if err != nil {
		return "", fmt.Errorf("whatsapp_cloud: setup temp: %w", err)
	}
	path := filepath.Join(dir, SetupFile)
	if err := writeSetup(tmp, data, path); err != nil {
		return "", err
	}
	return path, nil
}

// writeSetup stages data in tmp, then renames it over path.
func writeSetup(tmp *os.File, data []byte, path string) error {
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("whatsapp_cloud: setup mode: %w", discard(tmp, name, err))
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("whatsapp_cloud: setup write: %w", discard(tmp, name, err))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("whatsapp_cloud: setup close: %w", discard(nil, name, err))
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("whatsapp_cloud: setup rename: %w", discard(nil, name, err))
	}
	return nil
}

// discard removes the staging file and joins its error into primary.
func discard(tmp *os.File, name string, primary error) error {
	var closeErr error
	if tmp != nil {
		closeErr = tmp.Close()
	}
	removeErr := os.Remove(name)
	if errors.Is(removeErr, fs.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(primary, closeErr, removeErr)
}

// numericID reports whether s is a Meta phone-number id (digits only).
func numericID(s string) bool {
	if len(s) < 5 || len(s) > 32 {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// secretName mirrors the gwplat secret-name grammar: lowercase, digits and
// underscore, at most 64 bytes.
func secretName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}
