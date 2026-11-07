//go:build !minimal

package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/gwplat/whatsapp_cloud"
)

// waCloudSection is the single home of the Cloud API setup written by CLI-016.
const waCloudSection = "[modules.gateway.whatsapp_cloud]"

// whatsappCloudUsage documents the Cloud API setup surface.
func whatsappCloudUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot whatsapp-cloud setup --phone-number-id <id>\n")
	sessionsPrintf(w, "         [--api-version v21.0] [--access-token-secret NAME]\n")
	sessionsPrintf(w, "         [--app-secret-secret NAME] [--verify-token-secret NAME]\n")
	sessionsPrintf(w, "setup writes %s; set values with `damnbot setup secrets set NAME`\n", waCloudSection)
}

// runWhatsAppCloud implements `damnbot whatsapp-cloud` (CLI-016): setup writes
// the phone-number id and the keychain names of the three credentials into
// config, then prints the Meta webhook verification contract. No secret value
// is accepted, written or printed.
func runWhatsAppCloud(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) > 0 && isHelp(args[0]):
		whatsappCloudUsage(stdout)
		return 0
	case len(args) == 0 || args[0] != "setup":
		whatsappCloudUsage(stderr)
		return 2
	}
	return whatsAppCloudSetup(args[1:], stdout, stderr)
}

func whatsAppCloudSetup(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("whatsapp-cloud setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	phone := fs.String("phone-number-id", "", "Meta phone-number id (numeric)")
	apiVersion := fs.String("api-version", "", "Graph API version (default "+whatsapp_cloud.DefaultAPIVersion+")")
	access := fs.String("access-token-secret", whatsapp_cloud.AccessTokenSecret, "keychain secret holding the access token")
	appSecret := fs.String("app-secret-secret", whatsapp_cloud.AppSecretSecret, "keychain secret holding the app secret")
	verify := fs.String("verify-token-secret", whatsapp_cloud.VerifyTokenSecret, "keychain secret holding the webhook verify token")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		say(stderr, "whatsapp-cloud setup: unexpected argument %q\n", cleanArg(fs.Arg(0)))
		return 2
	}
	setup := config.ModulesWhatsAppCloud{
		Enabled:           true,
		PhoneNumberID:     *phone,
		APIVersion:        *apiVersion,
		AccessTokenSecret: *access,
		AppSecretSecret:   *appSecret,
		VerifyTokenSecret: *verify,
	}
	c, err := openConfig()
	if err != nil {
		return configFail(stderr, "whatsapp-cloud setup", err)
	}
	if code := writeConfigData(c, upsertWACloudSection(c.data, &setup), "whatsapp-cloud setup", stderr); code != 0 {
		return code
	}
	return printWhatsAppCloudSetup(stdout, c.file, &setup)
}

// upsertWACloudSection writes the Cloud API keys in the dedicated table,
// creating it when absent and preserving every other line.
func upsertWACloudSection(data []byte, s *config.ModulesWhatsAppCloud) []byte {
	attrs := [][2]string{
		{"enabled", fmt.Sprintf("%t", s.Enabled)},
		{"phone_number_id", fmt.Sprintf("%q", s.PhoneNumberID)},
		{"access_token_secret", fmt.Sprintf("%q", s.AccessTokenSecret)},
		{"app_secret_secret", fmt.Sprintf("%q", s.AppSecretSecret)},
		{"verify_token_secret", fmt.Sprintf("%q", s.VerifyTokenSecret)},
	}
	if s.APIVersion != "" {
		attrs = append(attrs, [2]string{"api_version", fmt.Sprintf("%q", s.APIVersion)})
	}
	return upsertSection(data, waCloudSection, attrs)
}

// printWhatsAppCloudSetup prints the config path and the Meta handshake. Every
// credential is named; the values stay in the keychain.
func printWhatsAppCloudSetup(stdout io.Writer, path string, setup *config.ModulesWhatsAppCloud) int {
	apiVersion := setup.APIVersion
	if apiVersion == "" {
		apiVersion = whatsapp_cloud.DefaultAPIVersion
	}
	if code := say(stdout, "whatsapp cloud: %s written (phone-number id %s, Graph %s)\n",
		path, setup.PhoneNumberID, apiVersion); code != 0 {
		return code
	}
	if code := say(stdout, "webhook verification (Meta app > WhatsApp > Configuration):\n"); code != 0 {
		return code
	}
	say(stdout, "  GET  hub.mode=subscribe&hub.verify_token=<value of keychain secret %q>; hub.challenge is echoed\n", setup.VerifyTokenSecret)
	say(stdout, "  POST body is checked with X-Hub-Signature-256 (HMAC-SHA256) using keychain secret %q\n", setup.AppSecretSecret)
	say(stdout, "  access token: keychain secret %q (never printed or stored)\n", setup.AccessTokenSecret)
	say(stdout, "set a value with: damnbot setup secrets set <name>\n")
	return 0
}
