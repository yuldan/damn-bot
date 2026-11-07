//go:build !minimal

package main

import (
	"flag"
	"io"

	"github.com/damnbot/damnbot/internal/gwplat/whatsapp_cloud"
)

// whatsappCloudUsage documents the Cloud API setup surface (CLI-016).
func whatsappCloudUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot whatsapp-cloud setup --phone-number-id <id>\n")
	sessionsPrintf(w, "         [--api-version v21.0] [--access-token-secret NAME]\n")
	sessionsPrintf(w, "         [--app-secret-secret NAME] [--verify-token-secret NAME] [--data-dir DIR]\n")
	sessionsPrintf(w, "setup records secret names only; set the values with `damnbot setup secrets set NAME`\n")
}

// runWhatsAppCloud implements `damnbot whatsapp-cloud` (CLI-016): setup writes
// the phone-number id and the keychain names of the three credentials, then
// prints the Meta webhook verification contract. No secret value is written or
// printed.
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
	version := fs.String("api-version", "", "Graph API version (default "+whatsapp_cloud.DefaultAPIVersion+")")
	access := fs.String("access-token-secret", whatsapp_cloud.AccessTokenSecret, "keychain secret holding the access token")
	appSecret := fs.String("app-secret-secret", whatsapp_cloud.AppSecretSecret, "keychain secret holding the app secret")
	verify := fs.String("verify-token-secret", whatsapp_cloud.VerifyTokenSecret, "keychain secret holding the webhook verify token")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		say(stderr, "whatsapp-cloud setup: unexpected argument %q\n", cleanArg(fs.Arg(0)))
		return 2
	}
	setup := whatsapp_cloud.Setup{
		PhoneNumberID:     *phone,
		APIVersion:        *version,
		AccessTokenSecret: *access,
		AppSecretSecret:   *appSecret,
		VerifyTokenSecret: *verify,
	}
	dir, err := resolveProfileDataDir(*dataDir)
	if err != nil {
		return configFail(stderr, "whatsapp-cloud setup", err)
	}
	path, err := whatsapp_cloud.SaveSetup(dir, &setup)
	if err != nil {
		return configFail(stderr, "whatsapp-cloud setup", err)
	}
	return printWhatsAppCloudSetup(stdout, path, &setup)
}

// printWhatsAppCloudSetup prints the record location and the Meta handshake.
// Every credential is named; the values stay in the keychain.
func printWhatsAppCloudSetup(stdout io.Writer, path string, setup *whatsapp_cloud.Setup) int {
	apiVersion := setup.APIVersion
	if apiVersion == "" {
		apiVersion = whatsapp_cloud.DefaultAPIVersion
	}
	if code := say(stdout, "whatsapp cloud: setup written to %s (phone-number id %s, Graph %s)\n",
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
