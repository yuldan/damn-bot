package redact

// holdCap bounds a Stream hold for unbounded-tail patterns (ADR-071 butir 3):
// past it the candidate is redacted fail-closed and the rest of its token is
// dropped as it arrives.
const holdCap = 4096

// Byte sets as inclusive range pairs; a leading '^' negates the set. The
// "\t\r" pair covers \t \n \v \f \r, i.e. RE2 \s plus \v.
const (
	setAlnum  = "AZaz09"
	setTok    = "AZaz09__--" // [A-Za-z0-9_-]
	setWord   = "AZaz09__"
	setUpDig  = "AZ09"
	setHex    = "af09"
	setSlack  = "AZaz09--"
	setJWT    = "AZaz09__--.." // all three JWT segments, used for dropping
	setDigit  = "09"
	setWS     = "  \t\t"
	setOpen   = "\"\"''[[" // quote or Go/Python list opener around a header value
	setSep    = "::=="
	setUser   = "^//::@@  \t\r" // [^/\s:@]
	setNonWS  = "^  \t\r"       // \S
	setHdrVal = "^\"\"''  \t\r" // \S without quotes, so JSON framing survives
	setLabel  = "AZ09  "        // PEM label [A-Z0-9 ]
)

type op uint8

const (
	opLit      op = iota // literal (optionally ASCII case-insensitive)
	opRun                // greedy run of a byte set, min..max (max 0 = unbounded)
	opBehind             // the min bytes before the cursor are all in set
	opMark               // replacement starts here (default: match start)
	opMarkEnd            // replacement ends here (default: match end)
	opKeyLabel           // PEM label read so far names a private key
	opPEMEnd             // skip to the matching -----END … PRIVATE KEY-----
	opLastAt             // run of set (≥ 1 byte) up to its last '@' (URL password)
)

type step struct {
	lit      string
	set      *[256]bool
	min, max int
	op       op
	fold     bool
}

type pattern struct {
	typ   string
	steps []step
	// drop is the byte set of the token tail dropped after a fail-closed cut;
	// for the PEM pattern it is the label set used to find the END line.
	drop   *[256]bool
	idx    int   // index among built-in patterns; -1 for known secrets
	first  *step // the first literal step; its bytes start every match
	secret bool
	pem    bool
}

func (p *pattern) firstLit() *step {
	for k := range p.steps {
		if p.steps[k].op == opLit {
			return &p.steps[k]
		}
	}
	return nil
}

func compile(def string) *[256]bool {
	var t [256]bool
	neg := def != "" && def[0] == '^'
	if neg {
		def = def[1:]
	}
	for k := 0; k+1 < len(def); k += 2 {
		for c := int(def[k]); c <= int(def[k+1]); c++ {
			t[c] = true
		}
	}
	if neg {
		for c := range t {
			t[c] = !t[c]
		}
	}
	return &t
}

func lit(s string) step  { return step{op: opLit, lit: s} }
func ilit(s string) step { return step{op: opLit, lit: s, fold: true} }
func run(set *[256]bool, minLen, maxLen int) step {
	return step{op: opRun, set: set, min: minLen, max: maxLen}
}

// builtinPatterns returns the §14.3 / ADR-071 patterns, most specific first
// among those sharing a first byte. Fixed-length token bodies ({n}) are
// matched as {n,}: a longer run is redacted whole instead of leaking its tail.
func builtinPatterns() []pattern {
	tok, alnum, hdr := compile(setTok), compile(setAlnum), compile(setHdrVal)
	prefixed := func(typ, prefix string, set *[256]bool, minLen int) pattern {
		return pattern{typ: typ, steps: []step{lit(prefix), run(set, minLen, 0)}, drop: set}
	}
	auth := func(typ, scheme string) pattern {
		ws, open := compile(setWS), compile(setOpen)
		return pattern{typ: typ, drop: hdr, steps: []step{
			ilit("authorization"), run(open, 0, 1), run(ws, 0, 8), run(compile(setSep), 1, 1),
			run(ws, 0, 8), run(open, 0, 2), ilit(scheme), run(ws, 1, 8),
			{op: opMark},
			run(hdr, 1, 0),
		}}
	}
	nonWS := compile(setNonWS)
	userinfo := func(anchor string) pattern {
		return pattern{typ: "url_password", drop: nonWS, steps: []step{
			lit(anchor), run(compile(setUser), 0, 0), lit(":"),
			{op: opMark},
			{op: opLastAt, set: nonWS},
			{op: opMarkEnd},
			lit("@"),
		}}
	}
	label := compile(setLabel)
	slack, upDig, hex := compile(setSlack), compile(setUpDig), compile(setHex)
	ps := []pattern{
		{typ: "private_key", pem: true, drop: label, steps: []step{
			lit("-----BEGIN "), run(label, 1, 64), {op: opKeyLabel}, lit("-----"), {op: opPEMEnd, set: label},
		}},
		prefixed("openrouter", "sk-or-v1-", hex, 64),
		prefixed("openai", "sk-", tok, 20),
		prefixed("github_pat", "github_pat_", compile(setWord), 60),
		{typ: "jwt", drop: compile(setJWT), steps: []step{
			lit("eyJ"), run(tok, 8, 0), lit("."), run(tok, 8, 0), lit("."), run(tok, 8, 0),
		}},
		{typ: "telegram", drop: tok, steps: []step{
			{op: opBehind, set: compile(setDigit), min: 8}, lit(":"), {op: opMark}, run(tok, 35, 0),
		}},
		// User may be empty; the password runs to the last '@' before
		// whitespace and may hold '/' and '@'; JSON-escaped ":\/\/" too
		// (ADR-096 butir 6, R-02). A fail-closed cut drops the rest of the \S run.
		userinfo(`://`), userinfo(`:\/\/`),
		auth("bearer", "bearer"), auth("basic_auth", "basic"), auth("auth_token", "token"),
	}
	for _, pre := range [...]string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"} {
		ps = append(ps, prefixed("github", pre, alnum, 36))
	}
	for _, pre := range [...]string{"AKIA", "ASIA"} {
		ps = append(ps, prefixed("aws", pre, upDig, 16))
	}
	for _, pre := range [...]string{"xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-", "xoxe-", "xapp-"} {
		ps = append(ps, prefixed("slack", pre, slack, 10))
	}
	for _, pre := range [...]string{"sk_live_", "sk_test_", "sk_prod_", "rk_live_", "rk_test_", "rk_prod_"} {
		ps = append(ps, prefixed("stripe", pre, alnum, 20))
	}
	return append(ps,
		prefixed("google", "AIza", tok, 35),
		prefixed("gitlab", "glpat-", tok, 20),
		prefixed("huggingface", "hf_", alnum, 30),
		prefixed("npm", "npm_", alnum, 36),
	)
}
