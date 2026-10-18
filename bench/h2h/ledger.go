package h2h

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Ledger adalah biaya LLM kumulatif lintas run untuk satu cap (ADR-062 butir 3, ADR-075 AUD-037):
// setiap biaya yang dihitung proxy ditambahkan dan file ditulis ulang secara atomik, sehingga
// cap berlaku untuk jumlah semua run, bukan per proses.
type Ledger struct {
	CapUSD   float64   `json:"cap_usd"`
	SpentUSD float64   `json:"spent_usd"`
	Updated  time.Time `json:"updated"`

	path string
	mu   sync.Mutex
}

// OpenLedger membaca ledger di path, atau membuatnya dengan capUSD bila belum ada.
// Cap yang tersimpan tidak bisa dinaikkan dari command line: menaikkannya = ubah file lewat ADR.
func OpenLedger(path string, capUSD float64) (*Ledger, error) {
	l := &Ledger{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if capUSD <= 0 {
			return nil, fmt.Errorf("ledger %s: cap baru harus > 0", path)
		}
		l.CapUSD = capUSD
		return l, l.save()
	case err != nil:
		return nil, fmt.Errorf("ledger %s: %w", path, err)
	}
	if err := json.Unmarshal(b, l); err != nil {
		return nil, fmt.Errorf("ledger %s: %w", path, err)
	}
	if l.CapUSD <= 0 || l.SpentUSD < 0 {
		return nil, fmt.Errorf("ledger %s: cap %.4f / spent %.4f tidak valid", path, l.CapUSD, l.SpentUSD)
	}
	return l, nil
}

// Spent mengembalikan total USD yang tercatat.
func (l *Ledger) Spent() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.SpentUSD
}

// Add mencatat biaya dan menulis ledger sebelum kembali.
func (l *Ledger) Add(usd float64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.SpentUSD += usd
	return l.save()
}

// save menulis ke file sementara di direktori yang sama, fsync, lalu rename (atomik).
func (l *Ledger) save() error {
	l.Updated = time.Now().UTC()
	b, err := json.MarshalIndent(l, "", " ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".ledger-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op setelah rename berhasil
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), l.path)
}
