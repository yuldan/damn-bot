package connectors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Section is part of the connector framework.
type Section struct {
	Enabled          bool  `json:"enabled"`
	Offline          bool  `json:"offline"`
	NoBackground     bool  `json:"no_background"`
	TimeoutS         int   `json:"timeout_s"`
	MaxRetries       int   `json:"max_retries"`
	MaxResponseBytes int64 `json:"max_response_bytes"`
}

// Framework bounds are hard caps: config may lower them, never raise them.
const (
	MaxTimeout       = 120 * time.Second
	MaxRetries       = 3
	MaxResponseBytes = 8 << 20
)

// SecretSource resolves one named secret (an env reference from config).
type SecretSource func(name string) (string, error)

// Config is part of the connector framework.
type Config struct {
	Enabled      bool
	Offline      bool
	NoBackground bool
	Timeout      time.Duration
	Retries      int
	MaxBytes     int64
}

// DecodeSection decodes one strict [modules.connectors] section.
func DecodeSection(raw json.RawMessage) (Section, error) {
	var s Section
	if len(bytes.TrimSpace(raw)) == 0 {
		return s, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("%w: %w", ErrInvalidConnector, err)
	}
	return s, nil
}

// ConfigFrom is part of the connector framework.
func ConfigFrom(s *Section) (Config, error) {
	if s.TimeoutS < 0 || s.TimeoutS > int(MaxTimeout/time.Second) {
		return Config{}, fmt.Errorf("%w: timeout_s must be 1..%d", ErrInvalidConnector, int(MaxTimeout/time.Second))
	}
	if s.MaxRetries < 0 || s.MaxRetries > MaxRetries {
		return Config{}, fmt.Errorf("%w: max_retries must be 0..%d", ErrInvalidConnector, MaxRetries)
	}
	if s.MaxResponseBytes < 0 || s.MaxResponseBytes > MaxResponseBytes {
		return Config{}, fmt.Errorf("%w: max_response_bytes must be 1..%d", ErrInvalidConnector, MaxResponseBytes)
	}
	return Config{
		Enabled: s.Enabled, Offline: s.Offline, NoBackground: s.NoBackground,
		Timeout: time.Duration(s.TimeoutS) * time.Second, Retries: s.MaxRetries, MaxBytes: s.MaxResponseBytes,
	}, nil
}
