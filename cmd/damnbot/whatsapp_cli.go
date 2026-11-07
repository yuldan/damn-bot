//go:build !minimal

package main

import (
	"flag"
	"io"
)

// whatsappUsage documents the Cloud-first setup surface (CLI-015).
func whatsappUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot whatsapp setup [--mode whatsapp_cloud|whatsapp_web] [--accept-ban-risk] [--data-dir DIR]\n")
	sessionsPrintf(w, "  cloud: damnbot whatsapp-cloud setup --phone-number-id <id>\n")
	sessionsPrintf(w, "  web:   an external bridge; refused without --accept-ban-risk\n")
}

// runWhatsApp implements `damnbot whatsapp` (CLI-015, GAP-GW-01): setup offers
// the official Cloud API, and the Web bridge is reachable only through the
// single `--accept-ban-risk` acknowledgement.
func runWhatsApp(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) > 0 && isHelp(args[0]):
		whatsappUsage(stdout)
		return 0
	case len(args) > 0 && args[0] == "setup":
		args = args[1:]
	}
	return whatsAppSetup(args, stdout, stderr)
}

// isHelp is the local help spelling of one subcommand.
func isHelp(arg string) bool { return arg == "help" || arg == "-h" || arg == "--help" }

func whatsAppSetup(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("whatsapp setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", "", "mode to set up: whatsapp_cloud or whatsapp_web")
	accept := fs.Bool("accept-ban-risk", false, "acknowledge the WhatsApp Web ban risk (required for web)")
	dataDir := fs.String("data-dir", "", "data directory for the audit event")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		say(stderr, "whatsapp setup: unexpected argument %q\n", cleanArg(fs.Arg(0)))
		return 2
	}
	if *mode == "" {
		return offerWhatsAppModes(stdout, stderr)
	}
	selected, err := selectWhatsAppMode(*mode, *accept)
	if err != nil {
		say(stderr, "whatsapp setup: %s\n", cleanLine(err.Error()))
		return 2
	}
	if selected.Web {
		return enableWhatsAppWeb(*dataDir, "whatsapp setup", stdout, stderr)
	}
	return say(stdout, "whatsapp cloud: run `damnbot whatsapp-cloud setup --phone-number-id <id>`\n")
}

// offerWhatsAppModes prints the mode table. Cloud API is always offered; Web
// appears only after the acknowledgement already recorded in config.
func offerWhatsAppModes(stdout, stderr io.Writer) int {
	c, err := openConfig()
	if err != nil {
		return configFail(stderr, "whatsapp setup", err)
	}
	ack := c.cfg.Modules.Gateway.WhatsAppWeb.AckBanRiskAt
	say(stdout, "whatsapp setup: modes\n")
	say(stdout, "  whatsapp_cloud  official Meta Cloud API (recommended)\n")
	if ack != "" {
		say(stdout, "  whatsapp_web    external bridge (ban risk acknowledged at %s)\n", ack)
	} else {
		say(stdout, "web mode is refused without --accept-ban-risk\n")
	}
	return say(stdout, "cloud: damnbot whatsapp-cloud setup --phone-number-id <id>\n")
}
