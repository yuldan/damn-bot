//go:build !minimal

// Portable archive (GAP-OPS-05): a DMBK1 container without secrets.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/damnbot/damnbot/internal/backup"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/memstore"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/secret"
	"github.com/damnbot/damnbot/internal/skills"
)

const (
	portableSchema        = 1
	portableConfigName    = "config.toml"
	portableMaxItems      = 4096
	portableMaxManifest   = 1 << 20
	portableMaxItemBytes  = 1 << 20
	portableMaxTotalBytes = 32 << 20
)

type portableKind string

const (
	portableConfig portableKind = "config"
	portableSkill  portableKind = "skill"
	portableMemory portableKind = "memory"
)

func (c *portableCounts) add(kind portableKind, changed bool) { //nolint:unparam // import passes false in the next PR
	c.Items++
	if changed {
		c.Changed++
	} else {
		c.Skipped++
	}
	switch kind {
	case portableConfig:
		c.Config++
	case portableSkill:
		c.Skills++
	case portableMemory:
		c.Memory++
	}
}

type portableItem struct {
	Kind      portableKind `json:"kind"`
	Name      string       `json:"name,omitempty"`
	Scope     string       `json:"scope,omitempty"`
	Category  string       `json:"category,omitempty"`
	CreatedAt int64        `json:"created_at,omitempty"`
	Size      int64        `json:"size"`
	SHA256    string       `json:"sha256"`
}

type portableManifest struct {
	Schema    int            `json:"schema"`
	App       string         `json:"app"`
	CreatedAt int64          `json:"created_at"`
	Items     []portableItem `json:"items"`
}

func portableRedactor() *redact.Redactor {
	return redact.New(redact.Options{Secrets: redact.SecretsFromEnv(os.Environ())})
}

func portableConfigBytes(data []byte, red *redact.Redactor) ([]byte, error) {
	keys, err := secretConfigKeys(data)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if len(keys) > 0 {
		return nil, fmt.Errorf("config contains plaintext secrets; replace with keychain: references: %s", strings.Join(keys, ", "))
	}
	return red.Redact(data), nil
}

func exportPortable(ctx context.Context, p config.Paths, out string, key secret.BackupKey, codec ctxdb.Codec) (portableCounts, string, error) {
	var c portableCounts
	red := portableRedactor()
	var items []portableItem
	var blobs [][]byte
	add := func(kind portableKind, name, scope, category string, created int64, data []byte) {
		items = append(items, portableItem{
			Kind: kind, Name: name, Scope: scope, Category: category, CreatedAt: created,
			Size: int64(len(data)), SHA256: sha256Hex(data),
		})
		blobs = append(blobs, data)
		c.add(kind, true)
	}
	if data, err := os.ReadFile(p.ConfigFile()); err == nil {
		clean, cerr := portableConfigBytes(data, red)
		if cerr != nil {
			return c, "", fmt.Errorf("export config: %w", cerr)
		}
		add(portableConfig, portableConfigName, "", "", 0, clean)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return c, "", fmt.Errorf("read %s: %w", p.ConfigFile(), err)
	}
	seen := map[string]bool{}
	skillItems, skillBlobs, err := diskPortableSkills(p.ConfigDir, red, seen)
	if err != nil {
		return c, "", err
	}
	for i := range skillItems {
		items = append(items, skillItems[i])
		blobs = append(blobs, skillBlobs[i])
		c.add(portableSkill, true)
	}
	if db, derr := openPortableDB(ctx, p, codec, false); derr != nil {
		return c, "", derr
	} else if db != nil {
		defer closePortableDB(ctx, db)
		moreItems, moreBlobs, merr := dbPortableSkills(ctx, db, red, seen)
		if merr != nil {
			return c, "", merr
		}
		for i := range moreItems {
			items = append(items, moreItems[i])
			blobs = append(blobs, moreBlobs[i])
			c.add(portableSkill, true)
		}
		memItems, memBlobs, rerr := dbPortableMemory(ctx, db, red)
		if rerr != nil {
			return c, "", rerr
		}
		for i := range memItems {
			items = append(items, memItems[i])
			blobs = append(blobs, memBlobs[i])
			c.add(portableMemory, true)
		}
	}
	if len(items) == 0 {
		return c, "", errors.New("nothing to export: no config, skill, or approved memory")
	}
	if out == "" {
		dir := filepath.Join(p.DataDir, "backups")
		out = filepath.Join(dir, "portable-"+time.Now().UTC().Format("20060102T150405Z")+"-"+sha256Hex([]byte(fmt.Sprint(time.Now().UnixNano())))[:8]+".dmbk")
	}
	m := portableManifest{Schema: portableSchema, App: version, CreatedAt: time.Now().UTC().Unix(), Items: items}
	if err := writePortableArchive(out, key, m, blobs); err != nil {
		return c, "", err
	}
	return c, out, nil
}

// openPortableDB opens the profile database only when it exists.
func openPortableDB(ctx context.Context, p config.Paths, codec ctxdb.Codec, create bool) (*ctxdb.Store, error) {
	if !create {
		if _, err := os.Stat(filepath.Join(p.DataDir, "damnbot.db")); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			return nil, fmt.Errorf("stat database: %w", err)
		}
	}
	if err := os.MkdirAll(p.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: p.DataDir, Kind: "cli", Codec: codec})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
}

func closePortableDB(ctx context.Context, db *ctxdb.Store) {
	_ = db.CloseContext(ctx) //nolint:errcheck // best-effort close after the read/write path
}

// diskPortableSkills exports trusted skills; symlinks are refused.
func diskPortableSkills(configDir string, red *redact.Redactor, seen map[string]bool) ([]portableItem, [][]byte, error) {
	rootDir := filepath.Join(configDir, "skills")
	ents, err := os.ReadDir(rootDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", rootDir, err)
	}
	var items []portableItem
	var blobs [][]byte
	for _, ent := range ents {
		name := ent.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if ent.Type()&fs.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("skill %s: symlink is not allowed", name)
		}
		data, err := readPortableSkillFile(filepath.Join(rootDir, name))
		if err != nil {
			return nil, nil, err
		}
		if data == nil {
			continue
		}
		data = red.Redact(data)
		sk, err := skills.Parse(data, filepath.Join(rootDir, name, "SKILL.md"), skills.SourceUser)
		if err != nil {
			return nil, nil, fmt.Errorf("skill %s: %w", name, err)
		}
		if seen[sk.Name] {
			continue
		}
		seen[sk.Name] = true
		items = append(items, portableItem{Kind: portableSkill, Name: sk.Name, Size: int64(len(data)), SHA256: sha256Hex(data)})
		blobs = append(blobs, data)
	}
	return items, blobs, nil
}

func readPortableSkillFile(dir string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open skill dir: %w", err)
	}
	defer func() { _ = root.Close() }() //nolint:errcheck // read-only root
	info, err := root.Lstat("SKILL.md")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat SKILL.md: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("skill %s: SKILL.md must be a regular file", filepath.Base(dir))
	}
	data, err := root.ReadFile("SKILL.md")
	if err != nil {
		return nil, fmt.Errorf("read SKILL.md: %w", err)
	}
	if len(data) > skills.MaxBytes {
		return nil, fmt.Errorf("skill %s: SKILL.md > %d bytes", filepath.Base(dir), skills.MaxBytes)
	}
	return data, nil
}

// dbPortableSkills exports approved skills already exported from disk win.
func dbPortableSkills(ctx context.Context, db *ctxdb.Store, red *redact.Redactor, seen map[string]bool) ([]portableItem, [][]byte, error) {
	rows, err := skillstore.Search(ctx, db, "", portableMaxItems)
	if err != nil {
		return nil, nil, fmt.Errorf("list approved skills: %w", err)
	}
	var items []portableItem
	var blobs [][]byte
	for i := range rows {
		if seen[rows[i].Name] {
			continue
		}
		data := portableSkillBytes(&rows[i], red)
		sk, err := skills.Parse(data, rows[i].Path, skills.SourceImport)
		if err != nil || sk.Name != rows[i].Name {
			return nil, nil, fmt.Errorf("skill %s: stored row is not a valid SKILL.md", rows[i].Name)
		}
		seen[rows[i].Name] = true
		items = append(items, portableItem{Kind: portableSkill, Name: sk.Name, Size: int64(len(data)), SHA256: sha256Hex(data)})
		blobs = append(blobs, data)
	}
	return items, blobs, nil
}

func portableSkillBytes(row *skillstore.Row, red *redact.Redactor) []byte {
	if data, ok := readIndexedSkill(row.Path, row.SHA256); ok {
		return red.Redact(data)
	}
	body := red.Redact([]byte(row.Body))
	desc := strings.ReplaceAll(strings.ReplaceAll(row.Description, "\r", " "), "\n", " ")
	return []byte("---\nname: " + row.Name + "\ndescription: " + desc + "\n---\n" + string(body))
}

func readIndexedSkill(path, wantSHA string) ([]byte, bool) {
	if path == "" {
		return nil, false
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, false
	}
	defer func() { _ = root.Close() }() //nolint:errcheck // read-only root
	info, err := root.Lstat(filepath.Base(path))
	if err != nil || info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > skills.MaxBytes {
		return nil, false
	}
	data, err := root.ReadFile(filepath.Base(path))
	if err != nil || sha256Hex(data) != wantSHA {
		return nil, false
	}
	return data, true
}

func dbPortableMemory(ctx context.Context, db *ctxdb.Store, red *redact.Redactor) ([]portableItem, [][]byte, error) {
	var scopes []string
	err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		var derr error
		scopes, derr = tx.Strings(`SELECT DISTINCT scope FROM v_memory_status WHERE kind = 'memory' AND status = 'approved' ORDER BY scope`)
		if derr != nil {
			return fmt.Errorf("query memory scopes: %w", derr)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list memory scopes: %w", err)
	}
	var items []portableItem
	var blobs [][]byte
	now := time.Now().UnixMilli()
	for _, scope := range scopes {
		hits, serr := memstore.Search(ctx, db, &memstore.Query{
			Scopes: []string{scope}, Statuses: []string{"approved"}, Limit: -1, Now: now,
		})
		if serr != nil {
			return nil, nil, fmt.Errorf("list memory %s: %w", scope, serr)
		}
		for i := range hits {
			data := red.Redact([]byte(hits[i].Row.Content))
			items = append(items, portableItem{
				Kind: portableMemory, Scope: hits[i].Row.Scope, Category: hits[i].Row.Category,
				CreatedAt: hits[i].Row.CreatedAt, Size: int64(len(data)), SHA256: sha256Hex(data),
			})
			blobs = append(blobs, data)
		}
	}
	return items, blobs, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writePortableArchive(out string, key secret.BackupKey, m portableManifest, blobs [][]byte) (err error) { //nolint:gocyclo // publish path: validate, O_EXCL, write, fsync
	if len(m.Items) != len(blobs) {
		return errors.New("portable archive: item/blob mismatch")
	}
	if verr := validatePortableManifest(&m, blobs); verr != nil {
		return verr
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if len(raw) > portableMaxManifest {
		return fmt.Errorf("manifest is %d bytes; the limit is %d", len(raw), portableMaxManifest)
	}
	if derr := os.MkdirAll(filepath.Dir(out), 0o700); derr != nil {
		return fmt.Errorf("create archive dir: %w", derr)
	}
	if _, lerr := os.Lstat(out); lerr == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite", cleanArg(out))
	} else if !errors.Is(lerr, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", cleanArg(out), lerr)
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // explicit CLI output path
	if err != nil {
		return fmt.Errorf("create %s: %w", cleanArg(out), err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(out) //nolint:errcheck // remove the unpublished partial file
		}
	}()
	sum := sha256.Sum256(raw)
	w, werr := backup.NewWriter(f, key, &backup.WriterOptions{
		CreatedAt: time.Unix(m.CreatedAt, 0), App: m.App, IncludeSecrets: false, ManifestSHA256: hex.EncodeToString(sum[:]),
	})
	if werr != nil {
		return errors.Join(fmt.Errorf("write header: %w", werr), f.Close())
	}
	var lb [4]byte
	binary.BigEndian.PutUint32(lb[:], uint32(len(raw))) //nolint:gosec // bounded by portableMaxManifest
	if _, werr = w.Write(lb[:]); werr == nil {
		_, werr = w.Write(raw)
	}
	for i := 0; werr == nil && i < len(blobs); i++ {
		_, werr = w.Write(blobs[i])
	}
	if werr == nil {
		werr = w.Close()
	}
	if werr != nil {
		return errors.Join(fmt.Errorf("write archive: %w", werr), f.Close())
	}
	if serr := errors.Join(f.Sync(), f.Close()); serr != nil {
		return fmt.Errorf("finish %s: %w", cleanArg(out), serr)
	}
	return nil
}

func openPortableArchive(path string, key secret.BackupKey) (portableManifest, [][]byte, error) { //nolint:gocognit,gocyclo // framing validation is one linear pass
	var m portableManifest
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return m, nil, fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // read-only archive handle
	h, r, err := backup.NewReader(f, key)
	if err != nil {
		return m, nil, fmt.Errorf("open archive: %w", err)
	}
	if h.IncludeSecrets {
		return m, nil, errors.New("archive was created with secrets; a portable archive must not include them")
	}
	var lb [4]byte
	if _, err := io.ReadFull(r, lb[:]); err != nil {
		return m, nil, fmt.Errorf("read manifest length: %w", err)
	}
	n := binary.BigEndian.Uint32(lb[:])
	if n == 0 || n > portableMaxManifest {
		return m, nil, fmt.Errorf("manifest length %d is invalid", n)
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(r, raw); err != nil {
		return m, nil, fmt.Errorf("read manifest: %w", err)
	}
	if h.ManifestSHA256 != "" && h.ManifestSHA256 != sha256Hex(raw) {
		return m, nil, errors.New("manifest digest does not match the archive header")
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, nil, fmt.Errorf("decode manifest: %w", err)
	}
	var total int64
	for i := range m.Items {
		if m.Items[i].Size < 0 || m.Items[i].Size > portableMaxItemBytes {
			return m, nil, fmt.Errorf("item %d size %d is invalid", i, m.Items[i].Size)
		}
		total += m.Items[i].Size
		if total > portableMaxTotalBytes {
			return m, nil, fmt.Errorf("archive payload exceeds %d bytes", portableMaxTotalBytes)
		}
	}
	blobs := make([][]byte, len(m.Items))
	for i := range m.Items {
		blobs[i] = make([]byte, m.Items[i].Size)
		if _, err := io.ReadFull(r, blobs[i]); err != nil {
			return m, nil, fmt.Errorf("read item %d: %w", i, err)
		}
	}
	var one [1]byte
	if got, rerr := r.Read(one[:]); got != 0 || !errors.Is(rerr, io.EOF) {
		return m, nil, errors.New("archive has trailing payload")
	}
	if err := validatePortableManifest(&m, blobs); err != nil {
		return m, nil, err
	}
	return m, blobs, nil
}

func validatePortableManifest(m *portableManifest, blobs [][]byte) error { //nolint:gocognit,gocyclo // one switch over the closed item kinds
	if m.Schema != portableSchema {
		return fmt.Errorf("unsupported archive schema %d (want %d)", m.Schema, portableSchema)
	}
	if len(m.Items) == 0 || len(m.Items) > portableMaxItems || len(m.Items) != len(blobs) {
		return fmt.Errorf("archive item count %d is invalid", len(m.Items))
	}
	seen := map[string]bool{}
	configs := 0
	var total int64
	for i := range m.Items {
		it := &m.Items[i]
		b := blobs[i]
		if it.Size != int64(len(b)) || it.Size < 0 || it.Size > portableMaxItemBytes {
			return fmt.Errorf("item %d size %d is invalid", i, it.Size)
		}
		if sha256Hex(b) != it.SHA256 {
			return fmt.Errorf("item %d digest does not match the manifest", i)
		}
		total += it.Size
		if total > portableMaxTotalBytes {
			return fmt.Errorf("archive payload exceeds %d bytes", portableMaxTotalBytes)
		}
		switch it.Kind {
		case portableConfig:
			configs++
			if configs > 1 || it.Name != portableConfigName {
				return fmt.Errorf("archive has an invalid config entry %q", it.Name)
			}
			if _, err := toml.Decode(string(b), &map[string]any{}); err != nil {
				return fmt.Errorf("archive config is invalid TOML: %w", err)
			}
			if keys, err := secretConfigKeys(b); err != nil {
				return fmt.Errorf("archive config: %w", err)
			} else if len(keys) > 0 {
				return fmt.Errorf("archive config contains plaintext secrets: %s", strings.Join(keys, ", "))
			}
		case portableSkill:
			if !validPortableName(it.Name) {
				return fmt.Errorf("archive skill name %q is invalid", it.Name)
			}
			key := "skill\x00" + it.Name
			if seen[key] {
				return fmt.Errorf("archive repeats skill %q", it.Name)
			}
			seen[key] = true
			if _, err := skills.Parse(b, "archive/SKILL.md", skills.SourceImport); err != nil {
				return fmt.Errorf("archive skill %q is invalid: %w", it.Name, err)
			}
		case portableMemory:
			if !memory.Category(it.Category).Valid() {
				return fmt.Errorf("archive memory metadata is invalid")
			}
			if _, err := memory.NormalizeScope(it.Scope); err != nil {
				return fmt.Errorf("archive memory scope: %w", err)
			}
			key := "memory\x00" + it.Scope + "\x00" + it.SHA256
			if seen[key] {
				return fmt.Errorf("archive repeats a memory entry")
			}
			seen[key] = true
			if strings.TrimSpace(string(b)) == "" || len([]rune(string(b))) > memory.MaxContentChars {
				return fmt.Errorf("archive memory content is invalid")
			}
		default:
			return fmt.Errorf("archive item %d has unknown kind %q", i, it.Kind)
		}
	}
	return nil
}

func validPortableName(name string) bool {
	if name == "" || len(name) > skills.MaxName {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}
