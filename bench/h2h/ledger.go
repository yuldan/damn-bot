package h2h

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// F1LedgerPath adalah satu-satunya ledger biaya LLM F1 (ADR-085, relatif ke direktori modul
// bench). File ini di-commit; cap US$2,00 (ADR-062) tersimpan di dalamnya.
const F1LedgerPath = "h2h/results/f1-spend.json"

// F1CapUSD adalah cap total biaya LLM F1 saat ledger dibuat (ADR-062 butir 3).
const F1CapUSD = 2.00

// Ledger adalah biaya LLM kumulatif lintas run dan lintas proses untuk satu cap (ADR-062 butir 3,
// ADR-075 AUD-037, ADR-085 NEW-14, ADR-091 NEW2-24). Setiap operasi memakai read-modify-write di
// bawah flock pada file `<path>.lock`, sehingga proses lain (cmd/h2h, cmd/startup) selalu melihat
// belanja dan reservasi terbaru. File data ditulis ulang secara atomik (temp + fsync + rename).
//
// Satu request berbayar: Reserve (cek cap + catat proyeksi terburuk, atomik) sebelum forward, lalu
// Settle (reservasi diganti biaya yang dicatat) sesudahnya.
type Ledger struct {
	// CapUSD adalah cap di file saat ledger dibuka; Reserve selalu memakai cap di file terbaru.
	CapUSD float64

	path string
	mu   sync.Mutex // serialisasi dalam proses; flock untuk antar-proses
}

// ledgerFile adalah format persisten. Tanpa reservasi berjalan isinya sama dengan format awal
// (cap_usd, spent_usd, updated). spent_usd hanya bertambah.
type ledgerFile struct {
	CapUSD   float64   `json:"cap_usd"`
	SpentUSD float64   `json:"spent_usd"`
	Updated  time.Time `json:"updated"`
	// ReservedUSD: proyeksi terburuk request yang sedang diteruskan, per id `<pid>-<nonce>`.
	ReservedUSD map[string]float64 `json:"reserved_usd,omitempty"`
}

// OpenLedger membaca ledger di path, atau membuatnya dengan capUSD bila belum ada.
// Cap yang tersimpan tidak bisa dinaikkan dari command line: menaikkannya = ubah file lewat ADR.
func OpenLedger(path string, capUSD float64) (*Ledger, error) {
	l := &Ledger{path: path}
	err := l.locked(true, func() error {
		f, err := l.load()
		if errors.Is(err, os.ErrNotExist) {
			if capUSD <= 0 {
				return fmt.Errorf("ledger %s: cap baru harus > 0", path)
			}
			l.CapUSD = capUSD
			return l.save(&ledgerFile{CapUSD: capUSD})
		}
		if err != nil {
			return err
		}
		l.CapUSD = f.CapUSD
		if f.reclaim() {
			return l.save(f)
		}
		return nil // tidak berubah: file (dan `updated`) tidak ditulis ulang
	})
	if err != nil {
		return nil, err
	}
	return l, nil
}

// Spent mengembalikan total USD yang sudah dibebankan di file saat ini (tanpa reservasi berjalan).
// Bila file tidak bisa dibaca, hasilnya +Inf (fail-closed).
func (l *Ledger) Spent() float64 {
	var v float64
	if err := l.locked(false, func() error {
		f, err := l.load()
		if err == nil {
			v = f.SpentUSD
		}
		return err
	}); err != nil {
		return math.Inf(1)
	}
	return v
}

// Reserve memesan usd untuk satu request sebelum diteruskan. Cek cap dan pencatatan reservasi
// terjadi dalam satu read-modify-write di bawah flock eksklusif, sehingga request bersamaan — juga
// dari proses lain — saling melihat reservasinya. Ditolak bila terpakai + reservasi berjalan + usd
// > cap; cap = cap file, atau limit bila 0 < limit < cap file. id dipakai Settle.
func (l *Ledger) Reserve(usd, limit float64) (string, error) {
	id := strconv.Itoa(os.Getpid()) + "-" + rand.Text()
	err := l.update(func(f *ledgerFile) error {
		capUSD := f.CapUSD
		if limit > 0 && limit < capUSD {
			capUSD = limit
		}
		if held := f.reserved(); f.SpentUSD+held+usd > capUSD {
			return fmt.Errorf("budget cap %.2f USD: terpakai %.4f + reservasi %.4f + proyeksi %.4f melebihi cap",
				capUSD, f.SpentUSD, held, usd)
		}
		if f.ReservedUSD == nil {
			f.ReservedUSD = map[string]float64{}
		}
		f.ReservedUSD[id] = usd
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Settle mengganti reservasi id dengan usd (biaya nyata, atau proyeksi terburuk bila usage tidak
// diketahui). Bila Settle gagal, reservasi tetap di file dan tetap dihitung terhadap cap.
func (l *Ledger) Settle(id string, usd float64) error {
	return l.update(func(f *ledgerFile) error {
		delete(f.ReservedUSD, id)
		f.SpentUSD += usd
		return nil
	})
}

// update menjalankan fn atas isi file terbaru di bawah lock eksklusif lalu menyimpannya; fn yang
// gagal tidak menulis apa pun.
func (l *Ledger) update(fn func(*ledgerFile) error) error {
	return l.locked(true, func() error {
		f, err := l.load()
		if err != nil {
			return err
		}
		f.reclaim()
		if err := fn(f); err != nil {
			return err
		}
		return l.save(f)
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

// reserved menjumlahkan reservasi berjalan dalam urutan id tetap (hasil deterministik).
func (f *ledgerFile) reserved() float64 {
	sum := 0.0
	for _, id := range slices.Sorted(maps.Keys(f.ReservedUSD)) {
		sum += f.ReservedUSD[id]
	}
	return sum
}

// reclaim membebankan reservasi milik proses yang sudah tidak hidup (mati di antara forward dan
// settle) sebagai biaya terburuk, karena upstream mungkin tetap menagih. true bila f berubah.
func (f *ledgerFile) reclaim() bool {
	changed := false
	for _, id := range slices.Sorted(maps.Keys(f.ReservedUSD)) {
		pid, _, _ := strings.Cut(id, "-")
		if n, err := strconv.Atoi(pid); err == nil && processAlive(n) {
			continue
		}
		f.SpentUSD += f.ReservedUSD[id]
		delete(f.ReservedUSD, id)
		changed = true
	}
	return changed
}

// load membaca file dan memvalidasinya; os.ErrNotExist diteruskan apa adanya.
func (l *Ledger) load() (*ledgerFile, error) {
	b, err := os.ReadFile(l.path)
	if err != nil {
		return nil, err //nolint:wrapcheck // os.ErrNotExist dicek pemanggil
	}
	var f ledgerFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("ledger %s: %w", l.path, err)
	}
	if f.CapUSD <= 0 || f.SpentUSD < 0 {
		return nil, fmt.Errorf("ledger %s: cap %.4f / spent %.4f tidak valid", l.path, f.CapUSD, f.SpentUSD)
	}
	for id, usd := range f.ReservedUSD {
		if usd < 0 {
			return nil, fmt.Errorf("ledger %s: reservasi %s %.4f tidak valid", l.path, id, usd)
		}
	}
	return &f, nil
}

// save menulis f ke file sementara di direktori yang sama, fsync, lalu rename (atomik).
func (l *Ledger) save(f *ledgerFile) error {
	f.Updated = time.Now().UTC()
	b, err := json.MarshalIndent(f, "", " ")
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
