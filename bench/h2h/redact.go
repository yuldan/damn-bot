package h2h

import (
	"regexp"
	"strings"
)

// redactedPlaceholder replaces credential material in recorded artifacts.
const redactedPlaceholder = "<redacted>"

var (
	// secretFlag matches "<flag><separator><value>" where the value is a
	// credential. The value stops at whitespace and quotes so the surrounding
	// quoting of the recorded command survives.
	secretFlag   = regexp.MustCompile(`(?i)(--api[-_]?key|--api[-_]?token|--token|--password|--secret|--key|-k)([= ]+)([^\s"']+)`)
	secretHeader = regexp.MustCompile(`(?i)((?:authorization|x-api-key|api-key)\s*:\s*(?:bearer\s+)?)([^\s"']+)`)
	secretBearer = regexp.MustCompile(`(?i)(\bbearer\s+)([^\s"']+)`)
)

// secretFlags are the flags whose *following* argv element is a credential.
const secretFlags = " --api-key --apikey --api-token --token --password --secret --key -k "

// isSecretFlag reports whether one argv element is a credential-bearing flag.
func isSecretFlag(s string) bool {
	v := strings.ToLower(s)
	if k, _, ok := strings.Cut(v, "="); ok {
		v = k
	}
	return v != "" && !strings.ContainsAny(v, " \t") && strings.Contains(secretFlags, " "+v+" ")
}

// RedactCommand masks credential values inside one recorded command line so a
// bench artifact can never carry key material. The h2h scenarios artifact of
// #134 shipped a literal `--api-key <value>` because the writer had no such
// step (SEC-review); the guard in security_test.go now fails on any artifact
// under results/ that still matches a token pattern.
func RedactCommand(line string) string {
	line = secretHeader.ReplaceAllString(line, "$1"+redactedPlaceholder)
	line = secretBearer.ReplaceAllString(line, "$1"+redactedPlaceholder)
	return secretFlag.ReplaceAllString(line, "$1$2"+redactedPlaceholder)
}

// RedactArgs is RedactCommand for an argv: inline forms are masked in place and
// the element after a credential flag is replaced wholesale.
func RedactArgs(argv []string) []string {
	out := make([]string, len(argv))
	copy(out, argv)
	for i, a := range out {
		out[i] = RedactCommand(a)
		if isSecretFlag(a) && i+1 < len(out) {
			out[i+1] = redactedPlaceholder
		}
	}
	return out
}
