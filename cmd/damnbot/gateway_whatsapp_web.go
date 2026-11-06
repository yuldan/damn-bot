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
	c, err := openConfig()
	if err != nil {
		return configFail(stderr, "gateway whatsapp-web enable", err)
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
		if err := recordWAAck(*dataDir, ack); err != nil {
			say(stderr, "gateway whatsapp-web enable: audit: %s\n", cleanLine(err.Error()))
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
	data := upsertWAWebSection(c.data, enabled, ack)
	if _, err := c.loader.Parse(data, c.file); err != nil {
		return configFail(stderr, "gateway whatsapp-web", err)
	}
	if err := os.MkdirAll(c.paths.ConfigDir, 0o700); err != nil {
		return configFail(stderr, "gateway whatsapp-web", err)
	}
	tmp, err := os.CreateTemp(c.paths.ConfigDir, ".config.toml.wa-*")
	if err != nil {
		return configFail(stderr, "gateway whatsapp-web", err)
	}
	name := tmp.Name()
	if _, werr := tmp.Write(data); werr != nil {
		say(stderr, "gateway whatsapp-web: write: %s\n", cleanLine(werr.Error()))
		return 1
	}
	if cerr := tmp.Close(); cerr != nil {
		say(stderr, "gateway whatsapp-web: close: %s\n", cleanLine(cerr.Error()))
		return 1
	}
	if err := commitConfig(name, c.file); err != nil {
		return configFail(stderr, "gateway whatsapp-web", err)
	}
	return 0
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

// recordWAAck appends the single `system_notice{kind:"wa_ban_risk_ack"}` audit
// event. Failure is reported but never rolls the config back silently.
func recordWAAck(dataDir, ack string) error {
	db, err := openSessionsDB(context.Background(), dataDir)
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
