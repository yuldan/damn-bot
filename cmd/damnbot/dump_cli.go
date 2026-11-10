//go:build !minimal

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/redact"
)

// f8Dump is the deterministic support summary of `damnbot dump` (CLI-036).
type f8Dump struct {
	Version      string   `json:"version"`
	Go           string   `json:"go"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	ConfigDir    string   `json:"config_dir"`
	DataDir      string   `json:"data_dir"`
	ConfigVer    int      `json:"config_version"`
	Model        string   `json:"model"`
	Language     string   `json:"language"`
	Theme        string   `json:"theme"`
	MCPServers   int      `json:"mcp_servers"`
	Connectors   bool     `json:"connectors_enabled"`
	AutoFeatures []string `json:"auto_features"`
}

// runDump implements `damnbot dump [--format text|json]` (CLI-036, OPS-010).
func runDump(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory (default: profile data dir)")
	cfgPath := fs.String("config", "", "config.toml path (default: profile path)")
	format := fs.String("format", "text", "text|json")
	if err := fs.Parse(args); err != nil {
		return f8ExitUsage
	}
	if fs.NArg() != 0 || (*format != "text" && *format != "json") {
		say(stderr, "usage: damnbot dump [--format text|json] [--config FILE] [--data-dir DIR]\n")
		return f8ExitUsage
	}
	cfg, paths, dir, err := f8LoadSetup(*cfgPath, *dataDir)
	if err != nil {
		return f8CmdFail(stderr, "dump", err)
	}
	d := f8DumpReport(&cfg, paths, dir)
	if *format == "json" {
		b, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return f8CmdFail(stderr, "dump", fmt.Errorf("encode: %w", err))
		}
		if say(stdout, "%s\n", f8Redact(string(b))) != 0 {
			return f8ExitFail
		}
		return f8ExitOK
	}
	if say(stdout, "%s\n", f8Redact(f8DumpText(&d))) != 0 {
		return f8ExitFail
	}
	return f8ExitOK
}

// f8LoadSetup resolves the config, profile paths, and effective data dir in
// one place; every F8 diagnostic reads the same snapshot.
func f8LoadSetup(cfgPath, dataDir string) (config.Config, config.Paths, string, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return config.Config{}, config.Paths{}, "", fmt.Errorf("config deps: %w", err)
	}
	cfg, err := loadRunConfig(loader, cfgPath)
	if err != nil {
		return config.Config{}, config.Paths{}, "", err
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return config.Config{}, config.Paths{}, "", fmt.Errorf("config paths: %w", err)
	}
	if dataDir == "" {
		dataDir = paths.DataDir
	}
	return cfg, paths, dataDir, nil
}

// f8DumpReport projects the config onto the fixed summary. It never copies a
// secret value: only booleans, counts, names, and paths reach this struct.
func f8DumpReport(cfg *config.Config, paths config.Paths, dataDir string) f8Dump {
	return f8Dump{
		Version: version, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		ConfigDir: paths.ConfigDir, DataDir: dataDir, ConfigVer: cfg.ConfigVersion,
		Model: cfg.Provider.Model, Language: cfg.UI.Language, Theme: cfg.UI.Theme,
		MCPServers: len(cfg.Modules.MCP.Servers), Connectors: cfg.Modules.Connectors.Enabled,
		AutoFeatures: f8AutoEnabled(cfg),
	}
}

// f8AutoEnabled lists the auto features that are on, sorted by name.
func f8AutoEnabled(cfg *config.Config) []string {
	out := []string{}
	for name, on := range map[string]bool{
		"review": cfg.Modules.Auto.Review.Enabled, "title": cfg.Modules.Auto.Title.Enabled,
		"curator": cfg.Modules.Auto.Curator.Enabled, "compaction": cfg.Modules.Auto.Compaction.Enabled,
		"blueprints": cfg.Modules.Auto.Blueprints.Enabled, "suggestions": cfg.Modules.Auto.Suggestions.Enabled,
		"goals": cfg.Modules.Auto.Goals.Enabled, "loops": cfg.Modules.Auto.Loops.Enabled,
		"heartbeat": cfg.Modules.Auto.Heartbeat.Enabled, "disk_cleanup": cfg.Modules.Auto.DiskCleanup.Enabled,
		"maintenance": cfg.Modules.Auto.Maintenance.Enabled, "webhooks": cfg.Modules.Auto.Webhooks.Enabled,
	} {
		if on {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func f8DumpText(d *f8Dump) string {
	var b strings.Builder
	fmt.Fprintf(&b, "damnbot %s\n", d.Version)
	fmt.Fprintf(&b, "go %s\n", d.Go)
	fmt.Fprintf(&b, "os %s/%s\n", d.OS, d.Arch)
	fmt.Fprintf(&b, "config_dir %s\n", d.ConfigDir)
	fmt.Fprintf(&b, "data_dir %s\n", d.DataDir)
	fmt.Fprintf(&b, "config_version %d\n", d.ConfigVer)
	fmt.Fprintf(&b, "model %s\n", d.Model)
	fmt.Fprintf(&b, "language %s\n", d.Language)
	fmt.Fprintf(&b, "theme %s\n", d.Theme)
	fmt.Fprintf(&b, "mcp_servers %d\n", d.MCPServers)
	fmt.Fprintf(&b, "connectors_enabled %t\n", d.Connectors)
	fmt.Fprintf(&b, "auto_features %s\n", strings.Join(d.AutoFeatures, ","))
	return strings.TrimSuffix(b.String(), "\n")
}

// f8Redact applies SPEC §14.3 secret patterns plus the home path and e-mail
// addresses (PII) to one dump payload.
func f8Redact(text string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		text = strings.ReplaceAll(text, home, "~")
	}
	text = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`).ReplaceAllString(text, "[REDACTED:email]")
	r := redact.New(redact.Options{Secrets: redact.SecretsFromEnv(os.Environ())})
	return string(r.Redact([]byte(text)))
}
