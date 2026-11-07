package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	configos "github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

// waWebSection is the single home of the WhatsApp Web bridge acknowledgement
// (SPEC §9.7.1, GAP-GW-01). The timestamp is the legal record of the ban-risk
// acceptance; there is no second copy and no implicit default.
const waWebSection = "[modules.gateway.whatsapp_web]"

// errWebNeedsAck is the wizard/CLI refusal of the Web mode without the single
// `--accept-ban-risk` acknowledgement.
var errWebNeedsAck = errors.New("whatsapp web requires --accept-ban-risk")

// waWizardMode is one mode a setup surface may offer. Cloud API is the only
// mode offered without an acknowledgement; Web appears only after enable.
type waWizardMode struct {
	Name     string
	Web      bool
	NeedsAck bool
}

// whatsAppWizardModes is the GAP-GW-01 mode table: Cloud API always, Web only
// after the operator acknowledged the ban risk.
func whatsAppWizardModes(webAck bool) []waWizardMode {
	modes := []waWizardMode{{Name: "whatsapp_cloud"}}
	if webAck {
		modes = append(modes, waWizardMode{Name: "whatsapp_web", Web: true, NeedsAck: true})
	}
	return modes
}

// selectWhatsAppMode enforces the same rule for a named choice.
func selectWhatsAppMode(name string, webAck bool) (waWizardMode, error) {
	for _, mode := range whatsAppWizardModes(webAck) {
		if mode.Name == name {
			return mode, nil
		}
	}
	if name == "whatsapp_web" {
		return waWizardMode{}, errWebNeedsAck
	}
	return waWizardMode{}, fmt.Errorf("unknown whatsapp mode %q", cleanArg(name))
}

// gatewayWhatsAppWeb implements `damnbot gateway whatsapp-web enable|disable|status`.
func gatewayWhatsAppWeb(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		say(stderr, "gateway whatsapp-web: enable --accept-ban-risk | disable | status\n")
		return 2
	}
	switch args[0] {
	case "enable":
		return waWebEnable(args[1:], stdout, stderr)
	case "disable":
		return waWebDisable(args[1:], stdout, stderr)
	case "status":
		return waWebStatus(args[1:], stdout, stderr)
	}
	say(stderr, "gateway whatsapp-web: unknown subcommand %q\n", cleanArg(args[0]))
	return 2
}

func waWebEnable(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway whatsapp-web enable", flag.ContinueOnError)
	fs.SetOutput(stderr)
	accept := fs.Bool("accept-ban-risk", false, "acknowledge the WhatsApp Web ban risk (required)")
	dataDir := fs.String("data-dir", "", "data directory for the audit event")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*accept {
		say(stderr, "gateway whatsapp-web enable: %v; use the Cloud API, or pass the flag explicitly\n", errWebNeedsAck)
		return 2
	}
	return enableWhatsAppWeb(*dataDir, "gateway whatsapp-web enable", stdout, stderr)
}

// enableWhatsAppWeb records the single ban-risk acknowledgement and enables
// the bridge. The gateway command and `damnbot whatsapp setup` share this one
// write path, so the ack can never be duplicated or skipped.
func enableWhatsAppWeb(dataDir, cmd string, stdout, stderr io.Writer) int {
	c, err := openConfig()
	if err != nil {
		return configFail(stderr, cmd, err)
	}
	ack := c.cfg.Modules.Gateway.WhatsAppWeb.AckBanRiskAt
	first := ack == ""
	if first {
		ack = time.Now().UTC().Format(time.RFC3339)
	}
	if c.cfg.Modules.Gateway.WhatsAppWeb.Enabled && !first {
		return say(stdout, "whatsapp web: already enabled (ack %s); no second acknowledgement written\n", ack)
	}
	if code := writeWAWeb(c, true, ack, stderr); code != 0 {
		return code
	}
	if first {
		if err := recordWAAck(dataDir, ack); err != nil {
			say(stderr, "%s: audit: %s\n", cmd, cleanLine(err.Error()))
			return 1
		}
	}
	say(stdout, "whatsapp web: enabled (ack %s)\n", ack)
	say(stdout, "use a dedicated number for the bridge, never your personal one\n")
	return 0
}

func waWebDisable(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		say(stderr, "gateway whatsapp-web disable: unexpected argument %q\n", cleanArg(args[0]))
		return 2
	}
	c, err := openConfig()
	if err != nil {
		return configFail(stderr, "gateway whatsapp-web disable", err)
	}
	if code := writeWAWeb(c, false, c.cfg.Modules.Gateway.WhatsAppWeb.AckBanRiskAt, stderr); code != 0 {
		return code
	}
	return say(stdout, "whatsapp web: disabled\n")
}

func waWebStatus(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		say(stderr, "gateway whatsapp-web status: unexpected argument %q\n", cleanArg(args[0]))
		return 2
	}
	c, err := openConfig()
	if err != nil {
		return configFail(stderr, "gateway whatsapp-web status", err)
	}
	web := c.cfg.Modules.Gateway.WhatsAppWeb
	ack := web.AckBanRiskAt
	if ack == "" {
		ack = "never"
	}
	return say(stdout, "whatsapp web: enabled=%t ack_ban_risk_at=%s\n", web.Enabled, ack)
}

// writeWAWeb rewrites only the one section and validates the whole file before
// the atomic rename; comments elsewhere survive.
func writeWAWeb(c *configCLI, enabled bool, ack string, stderr io.Writer) int {
	return writeConfigData(c, upsertWAWebSection(c.data, enabled, ack), "gateway whatsapp-web", stderr)
}

// writeConfigData validates data as the whole config file, then replaces the
// file atomically. The caller supplies the already-upserted bytes.
func writeConfigData(c *configCLI, data []byte, cmd string, stderr io.Writer) int {
	if _, err := c.loader.Parse(data, c.file); err != nil {
		return configFail(stderr, cmd, err)
	}
	if err := os.MkdirAll(c.paths.ConfigDir, 0o700); err != nil {
		return configFail(stderr, cmd, err)
	}
	tmp, err := os.CreateTemp(c.paths.ConfigDir, ".config.toml.*")
	if err != nil {
		return configFail(stderr, cmd, err)
	}
	name := tmp.Name()
	if _, werr := tmp.Write(data); werr != nil {
		say(stderr, "%s: write: %s\n", cmd, cleanLine(werr.Error()))
		return 1
	}
	if cerr := tmp.Close(); cerr != nil {
		say(stderr, "%s: close: %s\n", cmd, cleanLine(cerr.Error()))
		return 1
	}
	if err := commitConfig(name, c.file); err != nil {
		return configFail(stderr, cmd, err)
	}
	return 0
}

// upsertSection rewrites the flat keys of one TOML table, creating the table
// when absent and preserving every other line (comments included). Each attrs
// pair is a raw key and its encoded value.
func upsertSection(data []byte, section string, attrs [][2]string) []byte {
	lines := strings.Split(string(data), "\n")
	want := encodedAttrs(attrs)
	start, end := sectionBounds(lines, section)
	if start < 0 {
		return appendSection(data, section, want)
	}
	have := replaceAttrs(lines, start, end, attrs, want)
	lines = insertAttrs(lines, start, attrs, want, have)
	return []byte(strings.Join(lines, "\n"))
}

// encodedAttrs renders `key = value` lines.
func encodedAttrs(attrs [][2]string) []string {
	want := make([]string, len(attrs))
	for i, kv := range attrs {
		want[i] = kv[0] + " = " + kv[1]
	}
	return want
}

// sectionBounds returns the header index and the first line after the table;
// start is -1 when the table is absent.
func sectionBounds(lines []string, section string) (int, int) {
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == section {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			return start, i
		}
	}
	return start, len(lines)
}

// appendSection adds a new table at the end of the file.
func appendSection(data []byte, section string, want []string) []byte {
	body := strings.TrimRight(string(data), "\n")
	if body != "" {
		body += "\n\n"
	}
	return []byte(body + section + "\n" + strings.Join(want, "\n") + "\n")
}

// replaceAttrs overwrites the known keys of an existing table.
func replaceAttrs(lines []string, start, end int, attrs [][2]string, want []string) []bool {
	have := make([]bool, len(attrs))
	for i := start + 1; i < end; i++ {
		key, _, ok := strings.Cut(strings.TrimSpace(lines[i]), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		for j, kv := range attrs {
			if key == kv[0] {
				lines[i], have[j] = want[j], true
			}
		}
	}
	return have
}

// insertAttrs adds the keys the table did not carry.
func insertAttrs(lines []string, start int, attrs [][2]string, want []string, have []bool) []string {
	var insert []string
	for j := range attrs {
		if !have[j] {
			insert = append(insert, want[j])
		}
	}
	if len(insert) == 0 {
		return lines
	}
	return append(lines[:start+1], append(insert, lines[start+1:]...)...)
}

// upsertWAWebSection writes `enabled` and `ack_ban_risk_at` in the dedicated
// table, creating it when absent and preserving every other line.
func upsertWAWebSection(data []byte, enabled bool, ack string) []byte {
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == waWebSection {
			start = i
			break
		}
	}
	enabledLine := fmt.Sprintf("enabled = %t", enabled)
	ackLine := fmt.Sprintf("ack_ban_risk_at = %q", ack)
	if start < 0 {
		body := strings.TrimRight(string(data), "\n")
		if body != "" {
			body += "\n\n"
		}
		return []byte(body + waWebSection + "\n" + enabledLine + "\n" + ackLine + "\n")
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	haveEnabled, haveAck := false, false
	for i := start + 1; i < end; i++ {
		key := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(key, "enabled") && strings.Contains(key, "="):
			lines[i], haveEnabled = enabledLine, true
		case strings.HasPrefix(key, "ack_ban_risk_at") && strings.Contains(key, "="):
			lines[i], haveAck = ackLine, true
		}
	}
	var insert []string
	if !haveEnabled {
		insert = append(insert, enabledLine)
	}
	if !haveAck {
		insert = append(insert, ackLine)
	}
	if len(insert) > 0 {
		lines = append(lines[:start+1], append(insert, lines[start+1:]...)...)
	}
	return []byte(strings.Join(lines, "\n"))
}

// resolveProfileDataDir returns the operator-selected data dir or the active
// profile's; `enable` must audit even on a fresh install, so the caller can
// create it before opening the store.
func resolveProfileDataDir(dataDir string) (string, error) {
	if dataDir != "" {
		return dataDir, nil
	}
	loader, err := configos.New(configos.OS())
	if err != nil {
		return "", fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return "", fmt.Errorf("config paths: %w", err)
	}
	return paths.DataDir, nil
}

// recordWAAck appends the single `system_notice{kind:"wa_ban_risk_ack"}` audit
// event. Failure is reported but never rolls the config back silently.
func recordWAAck(dataDir, ack string) error {
	dir, err := resolveProfileDataDir(dataDir)
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return fmt.Errorf("create data dir: %w", mkErr)
	}
	db, err := openSessionsDB(context.Background(), dir)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			return
		}
	}()
	payload := `{"kind":"wa_ban_risk_ack","ack_ban_risk_at":"` + ack + `"}`
	if _, err := db.Append(context.Background(), ctxdb.SystemSession, nil, ctxdb.Event{
		Kind: "system_notice", Payload: payload,
	}); err != nil {
		return fmt.Errorf("append wa_ban_risk_ack: %w", err)
	}
	return nil
}
