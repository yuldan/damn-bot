package h2h

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// F1LedgerPath adalah satu-satunya ledger biaya LLM F1 (ADR-085, relatif ke direktori modul
// bench). File ini di-commit; cap US$2,00 (ADR-062) tersimpan di dalamnya.
const F1LedgerPath = "h2h/results/f1-spend.json"

// F1CapUSD adalah cap total biaya LLM F1 saat ledger dibuat (ADR-062 butir 3).
const F1CapUSD = 2.00

// Ledger adalah biaya LLM kumulatif lintas run dan lintas proses untuk satu cap (ADR-062 butir 3,
// ADR-075 AUD-037, ADR-085 NEW-14). Setiap baca dan tambah memakai read-modify-write di bawah
// flock pada file `<path>.lock`, sehingga proses lain (cmd/h2h, cmd/startup) selalu melihat
// belanja terbaru. File data ditulis ulang secara atomik (temp + fsync + rename).
type Ledger struct {
	CapUSD   float64   `json:"cap_usd"`
	SpentUSD float64   `json:"spent_usd"`
	Updated  time.Time `json:"updated"`

	path string
	mu   sync.Mutex // serialisasi dalam proses; flock untuk antar-proses
}

// OpenLedger membaca ledger di path, atau membuatnya dengan capUSD bila belum ada.
// Cap yang tersimpan tidak bisa dinaikkan dari command line: menaikkannya = ubah file lewat ADR.
func OpenLedger(path string, capUSD float64) (*Ledger, error) {
	l := &Ledger{path: path}
	err := l.locked(true, func() error {
		switch rerr := l.load(); {
		case errors.Is(rerr, os.ErrNotExist):
			if capUSD <= 0 {
				return fmt.Errorf("ledger %s: cap baru harus > 0", path)
			}
			l.CapUSD = capUSD
			return l.save()
		default:
			return rerr
		}
	})
	if err != nil {
		return nil, err
	}
	return l, nil
}

// Spent mengembalikan total USD yang tercatat di file saat ini. Bila file tidak bisa dibaca,
// hasilnya +Inf sehingga setiap cap menolak request (fail-closed).
func (l *Ledger) Spent() float64 {
	var v float64
	if err := l.locked(false, func() error {
		err := l.load()
		v = l.SpentUSD
		return err
	}); err != nil {
		return math.Inf(1)
	}
	return v
}

// Add mencatat biaya (read-modify-write di bawah lock eksklusif) dan menulis ledger sebelum kembali.
func (l *Ledger) Add(usd float64) error {
	return l.locked(true, func() error {
		if err := l.load(); err != nil {
			return err
		}
		l.SpentUSD += usd
		return l.save()
	})
}

func (l *Ledger) locked(exclusive bool, fn func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	unlock, err := lockFile(l.path+".lock", exclusive)
	if err != nil {
		return fmt.Errorf("ledger %s: lock: %w", l.path, err)
	}
	return errors.Join(fn(), unlock())
}

// load membaca file ke l dan memvalidasinya; os.ErrNotExist diteruskan apa adanya.
func (l *Ledger) load() error {
	b, err := os.ReadFile(l.path)
	if err != nil {
		return err //nolint:wrapcheck // os.ErrNotExist dicek pemanggil
	}
	var f struct {
		CapUSD   float64   `json:"cap_usd"`
		SpentUSD float64   `json:"spent_usd"`
		Updated  time.Time `json:"updated"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("ledger %s: %w", l.path, err)
	}
	if f.CapUSD <= 0 || f.SpentUSD < 0 {
		return fmt.Errorf("ledger %s: cap %.4f / spent %.4f tidak valid", l.path, f.CapUSD, f.SpentUSD)
	}
	l.CapUSD, l.SpentUSD, l.Updated = f.CapUSD, f.SpentUSD, f.Updated
	return nil
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
