package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/damnbot/damnbot/internal/config"
)

const (
	profileArchiveSchema    = 1
	maxProfileArchiveBytes  = 8 << 20
	maxProfileManifestBytes = 64 << 10
	profileManifestEntry    = "manifest.json"
	profileConfigEntry      = "config.toml"
)

var errProfileExists = errors.New("profile already exists; rerun with --yes")

type profileManifest struct {
	Schema    int    `json:"schema"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Damnbot   string `json:"damnbot"`
}

func configProfilesExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config profiles export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "output archive")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return usageError(stderr, "config profiles export", err)
	}
	if len(pos) != 1 || pos[0] == "" {
		configUsage(stderr)
		return 2
	}
	written, err := exportProfile(pos[0], *out)
	if err != nil {
		return configFail(stderr, "config profiles export", err)
	}
	return say(stdout, "config: exported profile %s to %s\n", cleanLine(pos[0]), cleanLine(written))
}

func configProfilesImport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config profiles import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "import under this profile name")
	yes := fs.Bool("yes", false, "overwrite an existing profile")
	dry := fs.Bool("dry-run", false, "validate without writing")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return usageError(stderr, "config profiles import", err)
	}
	if len(pos) != 1 || pos[0] == "" {
		configUsage(stderr)
		return 2
	}
	imported, target, err := importProfile(pos[0], *name, *yes, *dry)
	if err != nil {
		if errors.Is(err, errProfileExists) {
			say(stderr, "config profiles import: %s\n", cleanLine(err.Error()))
			return 2
		}
		return configFail(stderr, "config profiles import", err)
	}
	if *dry {
		return say(stdout, "config: would import profile %s to %s (dry run)\n", cleanLine(imported), cleanLine(target))
	}
	return say(stdout, "config: imported profile %s to %s\n", cleanLine(imported), cleanLine(target))
}

func exportProfile(name, out string) (string, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return "", fmt.Errorf("config deps: %w", err)
	}
	paths, canonical, err := profilePaths(loader, name)
	if err != nil {
		return "", fmt.Errorf("profile %q: %w", name, err)
	}
	data, err := os.ReadFile(paths.ConfigFile())
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("profile %q has no config.toml", canonical)
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", paths.ConfigFile(), err)
	}
	secrets, err := secretConfigKeys(data)
	if err != nil {
		return "", fmt.Errorf("scan %s: %w", paths.ConfigFile(), err)
	}
	if len(secrets) > 0 {
		return "", fmt.Errorf("refusing to export secrets; remove or replace: %s", strings.Join(secrets, ", "))
	}
	if _, perr := loader.Parse(data, paths.ConfigFile()); perr != nil {
		return "", fmt.Errorf("load %s: %w", paths.ConfigFile(), perr)
	}
	if out == "" {
		out = canonical + ".tar.gz"
	}
	if info, serr := os.Stat(out); serr == nil {
		if cfgInfo, cerr := os.Stat(paths.ConfigFile()); cerr == nil && os.SameFile(info, cfgInfo) {
			return "", errors.New("output must not be the profile config.toml")
		}
	}
	manifest := profileManifest{
		Schema:    profileArchiveSchema,
		Name:      canonical,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Damnbot:   version,
	}
	archive, err := buildProfileArchive(manifest, data)
	if err != nil {
		return "", err
	}
	if len(archive) > maxProfileArchiveBytes {
		return "", fmt.Errorf("archive is %d bytes; the limit is %d", len(archive), maxProfileArchiveBytes)
	}
	if err := writeAtomicFile(out, ".profiles-*.tar.gz", archive); err != nil {
		return "", err
	}
	return out, nil
}

func importProfile(file, nameOverride string, yes, dry bool) (string, string, error) {
	raw, err := readArchiveFile(file)
	if err != nil {
		return "", "", err
	}
	manifest, cfg, err := readProfileArchive(raw)
	if err != nil {
		return "", "", err
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return "", "", fmt.Errorf("config deps: %w", err)
	}
	name := manifest.Name
	if nameOverride != "" {
		name = nameOverride
	}
	if name == "" {
		return "", "", errors.New("archive manifest has no profile name")
	}
	paths, canonical, err := profilePaths(loader, name)
	if err != nil {
		return "", "", err
	}
	if verr := validateArchivedConfig(loader, cfg); verr != nil {
		return "", "", verr
	}
	exists, err := profileConfigExists(paths, canonical)
	if err != nil {
		return "", "", err
	}
	if exists && !yes {
		return "", "", fmt.Errorf("%w: profile %q", errProfileExists, canonical)
	}
	if !dry {
		if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
			return "", "", fmt.Errorf("create %s: %w", paths.ConfigDir, err)
		}
		if err := writeAtomicFile(paths.ConfigFile(), ".config.toml.import-*", cfg); err != nil {
			return "", "", err
		}
	}
	return canonical, paths.ConfigFile(), nil
}

func validateArchivedConfig(loader *config.Loader, cfg []byte) error {
	secrets, err := secretConfigKeys(cfg)
	if err != nil {
		return fmt.Errorf("scan archive config.toml: %w", err)
	}
	if len(secrets) > 0 {
		return fmt.Errorf("archive contains secrets; remove or replace: %s", strings.Join(secrets, ", "))
	}
	if _, perr := loader.Parse(cfg, profileConfigEntry); perr != nil {
		return fmt.Errorf("validate archive config.toml: %w", perr)
	}
	return nil
}

func profilePaths(loader *config.Loader, name string) (config.Paths, string, error) {
	profile, canonical := name, name
	if name == "" || name == "default" {
		profile, canonical = "", "default"
	}
	paths, err := loader.Paths(profile)
	if err != nil {
		return config.Paths{}, "", fmt.Errorf("profile %q: %w", name, err)
	}
	return paths, canonical, nil
}

func profileConfigExists(paths config.Paths, canonical string) (bool, error) {
	if _, err := os.Lstat(paths.ConfigFile()); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", paths.ConfigFile(), err)
	}
	if canonical == "default" {
		return false, nil
	}
	fi, err := os.Stat(paths.ConfigDir)
	if err == nil {
		return fi.IsDir(), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", paths.ConfigDir, err)
	}
	return false, nil
}

func buildProfileArchive(manifest profileManifest, cfg []byte) ([]byte, error) {
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	modTime := time.Now().UTC()
	if err := writeTarEntry(tw, profileManifestEntry, manifestJSON, modTime); err != nil {
		return nil, err
	}
	if err := writeTarEntry(tw, profileConfigEntry, cfg, modTime); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("close gzip: %w", err)
	}
	return buf.Bytes(), nil
}

func writeTarEntry(tw *tar.Writer, name string, data []byte, modTime time.Time) error {
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: modTime, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write %s header: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func readProfileArchive(data []byte) (manifest profileManifest, cfg []byte, err error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return manifest, nil, fmt.Errorf("gzip: %w", err)
	}
	defer func() {
		if cerr := gz.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close gzip: %w", cerr)
		}
	}()
	seen := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		hdr, nerr := tr.Next()
		if errors.Is(nerr, io.EOF) {
			break
		}
		if nerr != nil {
			return manifest, nil, fmt.Errorf("tar: %w", nerr)
		}
		if err := readArchiveEntryAt(tr, hdr, &manifest, &cfg, seen); err != nil {
			return manifest, nil, err
		}
		seen[hdr.Name] = true
	}
	if !seen[profileManifestEntry] {
		return manifest, nil, errors.New("archive has no manifest.json")
	}
	if !seen[profileConfigEntry] {
		return manifest, nil, errors.New("archive has no config.toml")
	}
	if manifest.Schema != profileArchiveSchema {
		return manifest, nil, fmt.Errorf("unsupported manifest schema %d (want %d)", manifest.Schema, profileArchiveSchema)
	}
	return manifest, cfg, nil
}

func readArchiveEntryAt(tr *tar.Reader, hdr *tar.Header, manifest *profileManifest, cfg *[]byte, seen map[string]bool) error {
	if hdr.Typeflag != tar.TypeReg {
		return fmt.Errorf("entry %q is not a regular file", hdr.Name)
	}
	if seen[hdr.Name] {
		return fmt.Errorf("duplicate entry %q", hdr.Name)
	}
	switch hdr.Name {
	case profileManifestEntry:
		raw, err := readArchiveEntry(tr, hdr.Size, maxProfileManifestBytes)
		if err != nil {
			return err
		}
		return decodeManifest(raw, manifest)
	case profileConfigEntry:
		raw, err := readArchiveEntry(tr, hdr.Size, config.MaxFileSize)
		if err != nil {
			return err
		}
		*cfg = raw
		return nil
	default:
		return fmt.Errorf("unexpected entry %q", hdr.Name)
	}
}

func readArchiveEntry(r io.Reader, size, max int64) ([]byte, error) {
	if size < 0 || size > max {
		return nil, fmt.Errorf("entry is %d bytes; the limit is %d", size, max)
	}
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("read entry: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("entry is larger than %d bytes", max)
	}
	return data, nil
}

func decodeManifest(raw []byte, out *profileManifest) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("manifest.json: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("manifest.json has trailing data")
	}
	return nil
}

func readArchiveFile(file string) (data []byte, err error) {
	root, err := os.OpenRoot(filepath.Dir(file))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filepath.Dir(file), err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", filepath.Dir(file), cerr)
		}
	}()
	f, err := root.Open(filepath.Base(file))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", file, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", file, cerr)
		}
	}()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", file, err)
	}
	if fi.Size() > maxProfileArchiveBytes {
		return nil, fmt.Errorf("archive is %d bytes; the limit is %d", fi.Size(), maxProfileArchiveBytes)
	}
	data, err = io.ReadAll(io.LimitReader(f, maxProfileArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	if len(data) > maxProfileArchiveBytes {
		return nil, fmt.Errorf("archive is larger than %d bytes", maxProfileArchiveBytes)
	}
	return data, nil
}

func secretConfigKeys(data []byte) ([]string, error) {
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("parse TOML: %w", err)
	}
	var keys []string
	walkSecrets("", raw, &keys)
	slices.Sort(keys)
	return slices.Compact(keys), nil
}

func walkSecrets(prefix string, value any, keys *[]string) {
	switch v := value.(type) {
	case map[string]any:
		for k, child := range v {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			walkSecrets(path, child, keys)
		}
	case []map[string]any:
		for _, child := range v {
			walkSecrets(prefix, child, keys)
		}
	case []any:
		for _, child := range v {
			walkSecrets(prefix, child, keys)
		}
	default:
		if prefix == "" || !configSecretPath(prefix) || secretValueEmpty(v) || secretReference(v) {
			return
		}
		*keys = append(*keys, prefix)
	}
}

func secretValueEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case int64:
		return x == 0
	case float64:
		return x == 0
	default:
		return false
	}
}

func secretReference(v any) bool {
	s, ok := v.(string)
	return ok && strings.HasPrefix(s, "keychain:")
}

func writeAtomicFile(path, pattern string, data []byte) (err error) {
	tmp, err := writeTempFile(filepath.Dir(path), pattern, data)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if rerr := os.Remove(tmp); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("remove %s: %w", tmp, rerr))
			}
		}
	}()
	return commitConfig(tmp, path)
}

func writeTempFile(dir, pattern string, data []byte) (name string, err error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("create temp in %s: %w", dir, err)
	}
	name = f.Name()
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", name, cerr)
		}
		if err != nil {
			if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("remove %s: %w", name, rerr))
			}
		}
	}()
	if _, werr := f.Write(data); werr != nil {
		return name, fmt.Errorf("write %s: %w", name, werr)
	}
	return name, nil
}
