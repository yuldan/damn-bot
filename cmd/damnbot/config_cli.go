package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/toolset"
)

type configCLI struct {
	loader *config.Loader
	cfg    config.Config
	paths  config.Paths
	file   string
	data   []byte
	exists bool
}

// runConfig is `damnbot config` (SPEC §10); modules/hooks land with their engines.
func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		configUsage(stderr)
		return 2
	}
	switch args[0] {
	case "show":
		return configShow(args[1:], stdout, stderr)
	case "get":
		return configLookup("get", args[1:], stdout, stderr, false)
	case "explain":
		return configLookup("explain", args[1:], stdout, stderr, true)
	case "schema":
		return configSchemaCmd(args[1:], stdout, stderr)
	case "modules":
		return configModules(args[1:], stdout, stderr)
	case "edit":
		return configEdit(args[1:], stdout, stderr)
	case "profiles":
		return configProfiles(args[1:], stdout, stderr)
	}
	say(stderr, "config: unknown subcommand %q\n", cleanArg(args[0]))
	configUsage(stderr)
	return 2
}

func configUsage(w io.Writer) {
	say(w, "usage: damnbot config show\n       damnbot config get <key>\n       damnbot config explain <key>\n       damnbot config schema <section>\n       damnbot config modules\n       damnbot config edit [--dry-run]\n       damnbot config profiles ls\n       damnbot config profiles export <name> [-o FILE]\n       damnbot config profiles import <file> [--name NAME] [--yes] [--dry-run]\n")
}

func configFail(stderr io.Writer, cmd string, err error) int {
	say(stderr, "%s: %s\n", cmd, cleanLine(err.Error()))
	return 1
}

func openConfig() (*configCLI, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return nil, fmt.Errorf("config paths: %w", err)
	}
	c := &configCLI{loader: loader, paths: paths, file: paths.ConfigFile()}
	data, err := os.ReadFile(c.file)
	if err == nil {
		c.data, c.exists = data, true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", c.file, err)
	}
	if c.cfg, err = loader.Parse(c.data, c.file); err != nil {
		return nil, fmt.Errorf("load %s: %w", c.file, err)
	}
	if err = loader.Effective(&c.cfg); err != nil {
		return nil, fmt.Errorf("load %s: %w", c.file, err)
	}
	return c, nil
}

func configShow(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		say(stderr, "config show: unexpected argument %q\n", cleanArg(args[0]))
		return 2
	}
	c, err := openConfig()
	if err != nil {
		return configFail(stderr, "config show", err)
	}
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(maskConfig(&c.cfg)); err != nil {
		return configFail(stderr, "config show", err)
	}
	return say(stdout, "%s", b.String())
}

func configLookup(cmd string, args []string, stdout, stderr io.Writer, explain bool) int {
	if len(args) != 1 {
		say(stderr, "config %s: one key expected\n", cmd)
		configUsage(stderr)
		return 2
	}
	c, err := openConfig()
	if err != nil {
		return configFail(stderr, "config "+cmd, err)
	}
	v, ok := configValue(reflect.ValueOf(c.cfg), args[0])
	if !ok {
		say(stderr, "config %s: unknown key %q\n", cmd, cleanArg(args[0]))
		return 2
	}
	if !configScalar(v) {
		say(stderr, "config %s: %q names a table; use `damnbot config show`\n", cmd, cleanArg(args[0]))
		return 2
	}
	if !explain {
		return say(stdout, "%s\n", formatConfigValue(args[0], v))
	}
	source, line := configOrigin(c, args[0])
	if code := say(stdout, "%s = %s\nsource = %s\n", args[0], formatConfigValue(args[0], v), source); code != 0 {
		return code
	}
	if line > 0 {
		return say(stdout, "file = %s:%d\n", c.file, line)
	}
	return 0
}

// configOrigin reports GAP-OPS-01 provenance. F3 defines one environment
// override, DAMNBOT_NETWORK_BACKGROUND (GAP-OPS-02); flag overrides are F4.
func configOrigin(c *configCLI, key string) (string, int) {
	if key == "network.background" && c.loader.Env("DAMNBOT_NETWORK_BACKGROUND") != "" {
		return "env DAMNBOT_NETWORK_BACKGROUND", 0
	}
	var raw map[string]any
	md, err := toml.Decode(string(c.data), &raw)
	set := err == nil && slices.ContainsFunc(md.Keys(), func(k toml.Key) bool {
		return slices.Equal(splitDotted(k.String()), splitDotted(key))
	})
	if !c.exists || !set {
		return "default", 0
	}
	source := "file"
	if activeProfile() != "" {
		source = "profile"
	}
	return source, configKeyLine(c.data, key)
}

func configKeyLine(data []byte, key string) int {
	want := splitDotted(key)
	if len(want) == 0 {
		return 0
	}
	leaf := want[len(want)-1]
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		left, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if parts := splitDotted(left); len(parts) > 0 && parts[len(parts)-1] == leaf {
			return i + 1
		}
	}
	return 0
}

func splitDotted(s string) []string {
	parts := strings.Split(s, ".")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(strings.TrimSpace(p), `"'`)
		if _, err := strconv.Atoi(p); err != nil {
			out = append(out, p)
		}
	}
	return out
}

func configEdit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config edit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dry := fs.Bool("dry-run", false, "validate without writing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		configUsage(stderr)
		return 2
	}
	c, err := openConfig()
	if err != nil {
		return configFail(stderr, "config edit", err)
	}
	if *dry || !configTerminal(stdout) {
		if _, err := c.loader.Parse(c.data, c.file); err != nil {
			return configFail(stderr, "config edit", err)
		}
		why := "no terminal"
		if *dry {
			why = "dry run"
		}
		return say(stdout, "config: %s valid; not written (%s)\n", c.file, why)
	}
	return editInEditor(c, stdout, stderr)
}

// editInEditor validates a 0600 temp copy in $VISUAL/$EDITOR before renaming.
func editInEditor(c *configCLI, stdout, stderr io.Writer) int {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		say(stderr, "config edit: set $VISUAL or $EDITOR\n")
		return 1
	}
	if err := os.MkdirAll(c.paths.ConfigDir, 0o700); err != nil {
		return configFail(stderr, "config edit", err)
	}
	tmp, err := os.CreateTemp(c.paths.ConfigDir, ".config.toml.edit-*")
	if err != nil {
		return configFail(stderr, "config edit", err)
	}
	name := tmp.Name()
	defer func() {
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			configFail(stderr, "config edit", rerr)
		}
	}()
	if _, werr := tmp.Write(c.data); werr != nil {
		return configFail(stderr, "config edit", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return configFail(stderr, "config edit", cerr)
	}
	if rerr := runEditor(editor, name); rerr != nil {
		return configFail(stderr, "config edit", rerr)
	}
	edited, err := fs.ReadFile(os.DirFS(c.paths.ConfigDir), filepath.Base(name))
	if err != nil {
		return configFail(stderr, "config edit", err)
	}
	if _, err := c.loader.Parse(edited, c.file); err != nil {
		return configFail(stderr, "config edit", err)
	}
	if bytes.Equal(edited, c.data) {
		return say(stdout, "config: %s unchanged\n", c.file)
	}
	if err := commitConfig(name, c.file); err != nil {
		return configFail(stderr, "config edit", err)
	}
	return say(stdout, "config: wrote %s\n", c.file)
}

func runEditor(editor, path string) error {
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		return errors.New("empty editor command")
	}
	return startEditor(fields[0], append(fields[1:], path)...)
}

func startEditor(bin string, args ...string) error {
	cmd := exec.CommandContext(context.Background(), bin, args...) // #nosec G204 G702 -- bin is the user's $VISUAL/$EDITOR
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", bin, err)
	}
	return nil
}

// commitConfig renames the 0600 temp over path; a symlink at path is refused.
func commitConfig(tmp, path string) error {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink", path)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// configTerminal excludes /dev/null, which is a character device too.
func configTerminal(stdout io.Writer) bool {
	outFile, ok := stdout.(*os.File)
	if !ok {
		return false
	}
	in, ierr := os.Stdin.Stat()
	out, oerr := outFile.Stat()
	if ierr != nil || oerr != nil || in.Mode()&os.ModeCharDevice == 0 || out.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(in, null)
}

func configProfiles(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		configUsage(stderr)
		return 2
	}
	switch args[0] {
	case "ls":
		if len(args) != 1 {
			configUsage(stderr)
			return 2
		}
		return configProfilesLs(stdout, stderr)
	case "export":
		return configProfilesExport(args[1:], stdout, stderr)
	case "import":
		return configProfilesImport(args[1:], stdout, stderr)
	}
	configUsage(stderr)
	return 2
}

func configProfilesLs(stdout, stderr io.Writer) int {
	loader, err := config.New(config.OS())
	if err != nil {
		return configFail(stderr, "config profiles", err)
	}
	paths, err := loader.Paths("")
	if err != nil {
		return configFail(stderr, "config profiles", err)
	}
	entries, err := os.ReadDir(filepath.Join(paths.ConfigDir, "profiles"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return configFail(stderr, "config profiles", err)
	}
	names := []string{"default"}
	for _, e := range entries {
		if e.IsDir() && e.Name() != "default" {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names[1:])
	for _, name := range names {
		if code := say(stdout, "%s\n", name); code != 0 {
			return code
		}
	}
	return 0
}

func maskConfig(cfg *config.Config) config.Config {
	out := cfg.Clone()
	for i := range out.Providers.Custom {
		p := &out.Providers.Custom[i]
		if p.APIKey != "" {
			p.APIKey = "***"
		}
		for k := range p.ExtraHeaders {
			p.ExtraHeaders[k] = "***"
		}
		for k, v := range p.ExtraBody {
			if _, ok := v.(string); ok && configSecretPath("providers.custom.extra_body."+k) {
				p.ExtraBody[k] = "***"
			}
		}
	}
	for k := range out.Exec.Env {
		out.Exec.Env[k] = "***"
	}
	return out
}

func configValue(v reflect.Value, key string) (reflect.Value, bool) {
	for part := range strings.SplitSeq(key, ".") {
		v = indirectValue(v)
		if !v.IsValid() {
			return reflect.Value{}, false
		}
		switch v.Kind() {
		case reflect.Struct:
			f, ok := fieldByTag(v, part)
			if !ok {
				return reflect.Value{}, false
			}
			v = f
		case reflect.Map:
			var ok bool
			if v, ok = configMapIndex(v, part); !ok {
				return reflect.Value{}, false
			}
		case reflect.Slice, reflect.Array:
			var ok bool
			if v, ok = configSliceIndex(v, part); !ok {
				return reflect.Value{}, false
			}
		case reflect.Invalid, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
			reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.Chan, reflect.Func,
			reflect.Interface, reflect.Pointer, reflect.String, reflect.UnsafePointer:
			return reflect.Value{}, false
		}
	}
	return v, true
}

func configMapIndex(v reflect.Value, part string) (reflect.Value, bool) {
	if v.Type().Key().Kind() != reflect.String {
		return reflect.Value{}, false
	}
	mv := v.MapIndex(reflect.ValueOf(part).Convert(v.Type().Key()))
	return mv, mv.IsValid()
}

func configSliceIndex(v reflect.Value, part string) (reflect.Value, bool) {
	i, err := strconv.Atoi(part)
	if err != nil || i < 0 || i >= v.Len() {
		return reflect.Value{}, false
	}
	return v.Index(i), true
}

func fieldByTag(v reflect.Value, name string) (reflect.Value, bool) {
	t := v.Type()
	for i := range t.NumField() {
		if tomlTag(t, i) == name {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

func indirectValue(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func tomlTag(t reflect.Type, i int) string {
	tag, _, _ := strings.Cut(t.Field(i).Tag.Get("toml"), ",")
	if tag == "" || tag == "-" {
		return ""
	}
	return tag
}

// configSecretPath is the schema's secret marker. It names value fields only:
// api_key_env/key_cmd stay visible because they are references.
func configSecretPath(path string) bool {
	parts := strings.Split(path, ".")
	last := strings.ToLower(parts[len(parts)-1])
	if last == "api_key" || last == "token" || last == "secret" || last == "password" || last == "passwd" || last == "authorization" || last == "env" || last == "extra_headers" {
		return true
	}
	return len(parts) >= 2 && (parts[len(parts)-2] == "env" || parts[len(parts)-2] == "extra_headers")
}

func configDataOnly(section string) bool {
	return section == "gateway" || section == "providers"
}

func configScalar(v reflect.Value) bool {
	v = indirectValue(v)
	if !v.IsValid() || v.Type() == reflect.TypeFor[config.Duration]() {
		return true
	}
	kind := v.Kind()
	return kind == reflect.String || kind == reflect.Bool ||
		(kind >= reflect.Int && kind <= reflect.Int64) ||
		(kind >= reflect.Uint && kind <= reflect.Uint64) ||
		(kind >= reflect.Float32 && kind <= reflect.Float64)
}

func formatConfigValue(path string, v reflect.Value) string {
	v = indirectValue(v)
	if !v.IsValid() {
		return `""`
	}
	if configSecretPath(path) && !v.IsZero() {
		return `"***"`
	}
	if v.Type() == reflect.TypeFor[config.Duration]() {
		return strconv.Quote(config.Duration(v.Int()).String())
	}
	switch kind := v.Kind(); kind {
	case reflect.String:
		return strconv.Quote(v.String())
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case reflect.Invalid, reflect.Uintptr, reflect.Complex64, reflect.Complex128,
		reflect.Array, reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice, reflect.Struct, reflect.UnsafePointer:
		return `""`
	default:
		return `""`
	}
}

func configSchemaCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		say(stderr, "config schema: one section expected\n")
		configUsage(stderr)
		return 2
	}
	text, ok := configSchemaText(args[0])
	if !ok {
		say(stderr, "config schema: unknown section %q\n", cleanArg(args[0]))
		return 2
	}
	return say(stdout, "%s", text)
}

func schemaSample() config.Config {
	base := config.Default()
	base.Gateway.Users = map[string]config.GatewayUser{"<name>": {}}
	base.Providers.Custom = []config.CustomProvider{{
		KeyCmd:       []string{},
		Models:       []config.CustomModel{{Capabilities: []string{}}},
		ExtraHeaders: map[string]string{"<name>": ""},
		ExtraBody:    map[string]any{"<name>": ""},
		TLS:          &config.CustomTLS{SSLVerify: new(bool)},
		Streaming:    new(bool),
	}}
	base.Budget.Providers = map[string]float64{"<name>": 0}
	base.Exec.Env = map[string]string{"<name>": ""}
	base.Toolsets.Active, base.Toolsets.Disabled = []string{}, []string{}
	base.Toolsets.Platform = map[string]toolset.Override{"<os>": {Active: []string{}, Disabled: []string{}}}
	base.Toolsets.Custom = map[string]toolset.Definition{"<name>": {Tools: []string{}, Includes: []string{}}}
	return base
}

func configSchemaText(section string) (string, bool) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(schemaSample()); err != nil {
		return "", false
	}
	lines := strings.Split(b.String(), "\n")
	start := -1
	for i, line := range lines {
		if line == "["+section+"]" || line == "[["+section+"]]" {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := start + 1
	for end < len(lines) && configSchemaHeader(lines[end], section) {
		end++
	}
	var out strings.Builder
	out.WriteString("# generated from config.Default(); values are schema samples\n")
	if configDataOnly(section) {
		out.WriteString("# data-only: accepted and validated, no runtime consumer in this phase\n")
	}
	for _, key := range configSecretKeys(section) {
		out.WriteString("# secret: " + key + "\n")
	}
	out.WriteString(strings.Join(lines[start:end], "\n") + "\n")
	return out.String(), true
}

func configSchemaHeader(line, section string) bool {
	name := strings.Trim(line, "[]")
	return !strings.HasPrefix(line, "[") || name == section || strings.HasPrefix(name, section+".")
}

func configSecretKeys(section string) []string {
	switch section {
	case "exec":
		return []string{"exec.env.<name>"}
	case "providers":
		return []string{"providers.custom.api_key", "providers.custom.extra_headers.<name>"}
	default:
		return nil
	}
}
