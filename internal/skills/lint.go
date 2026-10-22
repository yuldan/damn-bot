package skills

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Bounds of SEC26 lint scanning (ADR-133 amendment): one referenced script
// and the whole skill directory.
const (
	MaxScriptBytes = 256 << 10
	MaxScanFiles   = 64
	MaxScanBytes   = 1 << 20
)

// Finding is one lint observation. It is JSON-identical to memory.Finding so
// it merges into the stored scan_result; Detail never holds a secret value.
type Finding struct {
	Category string `json:"category"`
	Severity string `json:"severity"`
	Detail   string `json:"detail,omitempty"`
}

// LintEnv is what [Lint] may consult. Files is the skill directory (the
// SKILL.md parent); production passes an os.Root FS so a symlink cannot
// escape it. Known reports an installed skill name. A nil field fails
// closed: every reference it would resolve is invalid.
type LintEnv struct {
	Files fs.FS
	Known func(name string) bool
}

// Lint validates the references of sk and scans its text and scripts (SPEC
// §22.1 GAP-SEC-26 and GAP-SEC-15, ADR-133). Errors wrap [ErrInvalid]: a
// `requires` name or `skill://` link that is neither sk nor Known; a script
// reference (see scriptRef) outside the skill directory, missing, or larger
// than [MaxScriptBytes]; a builtin skill that defaults to a mainnet endpoint.
// Findings: credential_access (high: reads a secret file or credential
// store, or prints a secret), payment (medium: declared, highlighted for
// review), payment_undeclared (medium) and mainnet_default (medium, non
// builtin).
func Lint(sk *Skill, env LintEnv) ([]Finding, error) {
	known := func(n string) bool { return n == sk.Name || (env.Known != nil && env.Known(n)) }
	for _, r := range sk.Requires {
		if !known(r) {
			return nil, fmt.Errorf("%w: requires unknown skill %q", ErrInvalid, r)
		}
	}
	rules := newLintRules()
	for _, m := range rules.link.FindAllStringSubmatch(sk.Body, -1) {
		if !known(m[1]) {
			return nil, fmt.Errorf("%w: skill://%s does not resolve", ErrInvalid, m[1])
		}
	}
	if env.Files == nil {
		return nil, fmt.Errorf("%w: skill directory cannot be verified", ErrInvalid)
	}
	for _, ref := range scriptRefs(sk.Body) {
		if err := checkScript(env.Files, ref); err != nil {
			return nil, err
		}
	}
	texts, err := scanDir(env.Files)
	if err != nil {
		return nil, err
	}
	texts = append([]string{sk.Description, sk.Body}, texts...)
	return lintFindings(sk, rules, foldText(strings.Join(texts, "\n")))
}

// lintFindings classifies the folded text and returns the findings; a
// builtin defaulting to mainnet is invalid.
func lintFindings(sk *Skill, rules *lintRules, all string) ([]Finding, error) {
	var out []Finding
	if d := rules.credential(all); d != "" {
		out = append(out, Finding{Category: "credential_access", Severity: "high", Detail: d})
	}
	if d := rules.obfuscated.FindString(all); d != "" {
		out = append(out, Finding{Category: "obfuscated_exec", Severity: "medium", Detail: d})
	}
	switch paid := containsAny(all, paymentPatterns()); {
	case sk.Payment():
		out = append(out, Finding{Category: "payment", Severity: "medium", Detail: "declares side_effects: payment"})
	case paid != "":
		out = append(out, Finding{Category: "payment_undeclared", Severity: "medium", Detail: paid})
	}
	if m := rules.mainnet.FindString(all); m != "" {
		if sk.Source == SourceBuiltin {
			return nil, fmt.Errorf("%w: builtin skill defaults to mainnet (%s); use testnet/devnet", ErrInvalid, m)
		}
		out = append(out, Finding{Category: "mainnet_default", Severity: "medium", Detail: m})
	}
	return out, nil
}

// scanDir reads every regular file of the skill directory (SEC10-2): an
// unreferenced helper or import can hide credential access. The walk is
// bounded; exceeding a bound is invalid_skill, never a partial scan.
func scanDir(files fs.FS) ([]string, error) {
	var out []string
	var total int64
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrInvalid, p, err)
		}
		if d.IsDir() {
			return nil
		}
		if p == "SKILL.md" {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink %s in skill directory", ErrInvalid, p) // SEC10-12: no outside reads
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return fmt.Errorf("%w: %s: %w", ErrInvalid, p, ierr)
		}
		if len(out) >= MaxScanFiles || total+info.Size() > MaxScanBytes {
			return fmt.Errorf("%w: skill directory exceeds %d files / %d bytes", ErrInvalid, MaxScanFiles, MaxScanBytes)
		}
		data, rerr := fs.ReadFile(files, p)
		if rerr != nil {
			return fmt.Errorf("%w: %s: %w", ErrInvalid, p, rerr)
		}
		total += int64(len(data))
		out = append(out, string(data))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("skills: walk: %w", err)
	}
	return out, nil
}

// lintRules are compiled per call: the package keeps no mutable globals.
type lintRules struct {
	link, readVerb, printVerb, secretRef, envDump, tellSecret, mainnet, obfuscated *regexp.Regexp
}

func newLintRules() *lintRules {
	nouns := `(?:api[ _-]?keys?|access[ _-]?tokens?|auth[ _-]?tokens?|bearer[ _-]?tokens?|api[ _-]?tokens?|github[ _-]?tokens?|secret[ _-]?keys?|secrets|passwords?|credentials|private[ _-]?keys?)`
	return &lintRules{
		link:      regexp.MustCompile("skill://([^\\s)\\]>\"'`#?/]+)"),
		readVerb:  regexp.MustCompile(`\b(?:cat|head|tail|source|grep|awk|sed|read\w*|load\w*|open|get-content|cp|base64|xxd|print\w*|echo|curl|parse\w*)\b|(?:^|\s)\.\s`),
		printVerb: regexp.MustCompile(`\b(?:echo|printf|print|println|puts|console\.log|write-host|write-output|fmt\.print\w*)\b`),
		secretRef: regexp.MustCompile(`(?:\$\{?|\$env:|environ\[["']?|environ\.get\(["']?|getenv\(["']?|process\.env\.)(?:\w*_)?(?:api_?key|key|token|secret|password|passwd|credentials?)(?:_\w*)?\b`),
		envDump:   regexp.MustCompile(`^(?:\$\s*)?(?:env|printenv|export -p|set|get-childitem env:|gci env:|ls env:)\s*(?:\|.*)?$|\bprintenv\s+(?:\w*_)?(?:key|token|secret|password)`),
		tellSecret: regexp.MustCompile(`\b(?:print|echo|show|display|output|reveal|paste)\s+(?:(?:the|your|their|its|my|our|this|that|all|every)\s+)*(?:[\w'-]+\s+)?` +
			nouns + `\b`),
		mainnet: regexp.MustCompile(`https?://[^\s'"` + "`" + `)]*mainnet[^\s'"` + "`" + `)]*|--(?:url|rpc-url|network|cluster|chain)[ =]mainnet\S*|(?:^|\s)-um(?:\s|$)`),
		obfuscated: regexp.MustCompile(`base64\s+(?:-d|--decode|-[dD])\b[^\n|]*\|\s*(?:ba|z|da|k)?sh\b|` +
			`\b(?:base64\s*\.\s*b64decode|b64decode)\s*\(|` +
			`\beval\s+["']?\$\(|\bexec\s*\(\s*(?:base64|b64decode)|` +
			`(?:zcat|gunzip|rev)\b[^\n|]*\|\s*(?:ba|z|da|k)?sh\b`),
	}
}

// foldText normalises before every match (SEC10-4): NFKC, lower case,
// literal-assignment expansion, empty-quote removal, format-character
// stripping (zero-width, Cf) and a Cyrillic/Greek confusable fold, so
// `gh аuth token`, `gh au​th token` and `a=gh; $a auth token` hit the
// same rules.
func foldText(s string) string {
	s = expandLiterals(strings.ToLower(norm.NFKC.String(s)))
	return strings.Map(foldRune, s)
}

// expandLiterals substitutes simple literal shell assignments (`a=gh;
// $a auth token`) and drops empty quote pairs (`tok""en`). One scan collects
// the assignments and one pass replaces references, so the cost is linear in
// the text (SEC10-11); at most 32 assignments and 1 MiB are considered.
func expandLiterals(s string) string {
	s = strings.NewReplacer(`""`, "", `''`, "").Replace(s)
	if len(s) > 2*MaxScanBytes {
		return s
	}
	assign := regexp.MustCompile(`(?m)(?:^|[;\s` + "`" + `])\s*([a-z_][a-z0-9_]{0,15})=([^\s;&|]{1,64})`)
	vals := make(map[string]string, 8)
	for _, m := range assign.FindAllStringSubmatchIndex(s, 33) {
		if len(vals) >= 32 {
			break
		}
		v := strings.Trim(s[m[4]:m[5]], `"'`)
		if v != "" && !strings.ContainsAny(v, "$`(<>") {
			vals[s[m[2]:m[3]]] = v
		}
	}
	if len(vals) == 0 {
		return s
	}
	ref := regexp.MustCompile(`\$\{([a-z_][a-z0-9_]{0,15})\}|\$([a-z_][a-z0-9_]{0,15})\b`)
	return ref.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.Trim(m, "${}")
		if v, ok := vals[name]; ok {
			return v
		}
		return m
	})
}

func foldRune(r rune) rune {
	if unicode.Is(unicode.Cf, r) {
		return -1
	}
	from := [...]rune{
		'а', 'α', 'е', 'ε', 'о', 'ο', 'օ', 'р', 'ρ', 'с', 'ϲ', 'х', 'χ', 'у', 'γ', 'і', 'ι', 'ı',
		'ѕ', 'ԁ', 'һ', 'ј', 'ӏ', 'ν', 'ɡ', 'т', 'г',
	}
	to := [...]rune{
		'a', 'a', 'e', 'e', 'o', 'o', 'o', 'p', 'p', 'c', 'c', 'x', 'x', 'y', 'y', 'i', 'i', 'i',
		's', 'd', 'h', 'j', 'l', 'v', 'g', 't', 'r',
	}
	for i := range from {
		if from[i] == r {
			return to[i]
		}
	}
	return r
}

// credential returns the first credential_access match in lower-cased
// text, line by line, or "".
func (r *lintRules) credential(lower string) string {
	cmds, files := credentialCommands(), secretFiles()
	words := []string{"key", "token", "secret", "password", "passwd", "credential"}
	for ln := range strings.SplitSeq(lower, "\n") {
		ln = strings.Trim(ln, " \t`>-*")
		if p := containsAny(ln, cmds); p != "" {
			return p
		}
		if f := secretFile(ln, files); f != "" && r.readVerb.MatchString(ln) {
			return "reads " + f
		}
		if containsAny(ln, words) != "" &&
			(r.printVerb.MatchString(ln) && r.secretRef.MatchString(ln) || r.tellSecret.MatchString(ln)) {
			return "prints a secret"
		}
		if (strings.Contains(ln, "env") || strings.Contains(ln, "export -p") || strings.HasPrefix(ln, "set")) &&
			r.envDump.MatchString(ln) {
			return "prints a secret"
		}
	}
	return ""
}

// credentialCommands read a credential store or token outright.
func credentialCommands() []string {
	return []string{
		"gh auth token", "--show-token", "git credential fill", "git config --get credential",
		"security find-generic-password", "security find-internet-password", "security dump-keychain",
		"secret-tool lookup", "keyring get", "cmdkey /list", "get-storedcredential", "kwallet-query",
		"gcloud auth print-access-token", "az account get-access-token",
		"load_dotenv", "dotenv.config", "require('dotenv')", `require("dotenv")`, "dotenv/config", "godotenv.load",
	}
}

// secretFile returns a credential file named on the line: `.env` (and
// `.env.<suffix>`, but not `process.env` or `.envrc`) or a fixed list.
func secretFiles() []string {
	return []string{
		".git-credentials", ".netrc", ".npmrc", ".pypirc", ".aws/credentials",
		"gh/hosts.yml", ".docker/config.json", ".ssh/id_", ".kube/config", "secrets.enc",
	}
}

func secretFile(ln string, files []string) string {
	if f := containsAny(ln, files); f != "" {
		return f
	}
	word := func(b byte) bool {
		return b == '_' || (b < 0x80 && (unicode.IsLetter(rune(b)) || unicode.IsDigit(rune(b))))
	}
	for i := strings.Index(ln, ".env"); i >= 0; {
		end := i + len(".env")
		if (i == 0 || !word(ln[i-1])) && (end == len(ln) || !word(ln[end])) {
			return ".env"
		}
		next := strings.Index(ln[end:], ".env")
		if next < 0 {
			break
		}
		i = end + next
	}
	return ""
}

// paymentPatterns mark text that performs payments (SEC-15).
func paymentPatterns() []string {
	return []string{
		"stripe", "payment_intent", "spend-request", "link-cli", "solana transfer", "cast send",
		"gh sponsor", "payout", "sendtransaction", "send_transaction",
	}
}

func containsAny(s string, pats []string) string {
	for _, p := range pats {
		if strings.Contains(s, p) {
			return p
		}
	}
	return ""
}

// scriptRefs lists the distinct script references of a body in order.
// Quoted placeholders (`"$SKILL_DIR"/scripts/x`) are unwrapped before the
// whitespace split so the quoted form is not misread as an absolute path
// (SEC10-5).
func scriptRefs(body string) []string {
	body = strings.NewReplacer(
		`"$SKILL_DIR"/`, `$SKILL_DIR/`, `"${SKILL_DIR}"/`, `${SKILL_DIR}/`,
		`'$SKILL_DIR'/`, `$SKILL_DIR/`, `'${SKILL_DIR}'/`, `${SKILL_DIR}/`,
	).Replace(body)
	var out []string
	seen := map[string]bool{}
	split := func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("()[]<>`'\"=,;|&", r) }
	for _, tok := range strings.FieldsFunc(body, split) {
		if ref, ok := scriptRef(strings.TrimRight(tok, ".:")); ok && !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

// scriptRef reports whether tok names a skill file: anything after a skill
// directory placeholder (`$SKILL_DIR/`, `${SKILL_DIR}/`, `{baseDir}/`), a
// path starting with `scripts/` (or `./scripts/`), or a script-extension
// path that is absolute, home- or parent-relative (outside by construction).
// Other relative paths are workspace files and are not checked.
func scriptRef(tok string) (string, bool) {
	if strings.Contains(tok, "://") {
		return "", false
	}
	for _, p := range []string{"${SKILL_DIR}/", "$SKILL_DIR/", "{baseDir}/"} {
		if rest, ok := strings.CutPrefix(tok, p); ok {
			return rest, rest != ""
		}
	}
	rel := strings.TrimPrefix(tok, "./")
	if strings.HasPrefix(rel, "scripts/") && len(rel) > len("scripts/") {
		return rel, true
	}
	outside := strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "~") || strings.HasPrefix(tok, "..") ||
		(len(tok) > 2 && tok[1] == ':')
	exts := []string{".sh", ".bash", ".zsh", ".py", ".js", ".mjs", ".cjs", ".ts", ".rb", ".pl", ".ps1", ".bat", ".cmd"}
	return tok, outside && slices.Contains(exts, path.Ext(tok))
}

// checkScript verifies one referenced script is inside the skill directory,
// exists, is a regular file and fits [MaxScriptBytes]. Its content is read
// once by scanDir.
func checkScript(files fs.FS, ref string) error {
	clean := path.Clean(ref)
	if strings.HasPrefix(ref, "~") || strings.ContainsAny(ref, `\:`) || !fs.ValidPath(clean) {
		return fmt.Errorf("%w: %s is outside the skill directory", ErrInvalid, ref)
	}
	if files == nil {
		return fmt.Errorf("%w: script %s cannot be verified", ErrInvalid, ref)
	}
	info, err := fs.Stat(files, clean)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: script %s does not exist", ErrInvalid, ref)
	}
	if info.Size() > MaxScriptBytes {
		return fmt.Errorf("%w: script %s > %d bytes", ErrInvalid, ref, MaxScriptBytes)
	}
	return nil
}
