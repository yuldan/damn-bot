package classify

import (
	"slices"
	"strings"
)

// HTTP client options (curl/wget/httpie/Invoke-WebRequest).
const (
	fetchers       = " curl wget iwr irm invoke-webrequest invoke-restmethod "
	httpClients    = fetchers + " http https xh "
	remoteCmds     = " ssh scp sftp rsync nc ncat socat telnet "
	httpMethodOpts = " -X --request --method -method "
	httpBodyOpts   = " -d --data --data-binary --data-raw --data-urlencode --data-ascii -F --form --form-string" +
		" --json --post-data --body-data -body "
	httpFileOpts = " -T --upload-file --post-file --body-file -infile "
	httpOutOpts  = " -o -O --output --output-document -outfile "
	httpValued   = httpMethodOpts + httpBodyOpts + httpFileOpts + httpOutOpts + " -H --header -A --user-agent -u" +
		" --user -e --referer -b --cookie -c --cookie-jar -x --proxy -w --write-out -m --max-time -K --config -headers "
)

// http labels HTTP clients: GET to a public host is network; a mutating
// method or body, or a metadata host, is irreversible (monetary for
// security.monetary_hosts).
func (s *state) http(args []arg) {
	method, body := "", false
	var hosts []string
	for i := 1; i < len(args); i++ {
		if a := args[i]; !strings.HasPrefix(a.s, "-") {
			hosts = append(hosts, hostOf(a.s))
			body = body || (len(hosts) > 1 && strings.ContainsAny(a.s, "=@") && !strings.Contains(a.s, "=="))
			if len(hosts) == 1 && a.s == strings.ToUpper(a.s) && isLetter(a.s[0]) {
				method = a.s
			}
			continue
		}
		name, val := s.optSplit(args, &i)
		switch {
		case inList(httpMethodOpts, name):
			method = strings.ToUpper(val.s)
		case inList(httpBodyOpts, name):
			body = true
			if _, f, ok := strings.Cut(val.s, "@"); ok {
				s.operand(arg{s: f, lit: val.lit}, false)
			}
		case inList(httpFileOpts, name):
			body = true
			s.operand(val, false)
		case inList(httpOutOpts, name):
			s.operand(val, true)
		}
	}
	s.httpLabel(method, body, hosts)
}

// optSplit splits --opt=value, -Xvalue or "-X value" (consuming it).
func (s *state) optSplit(args []arg, i *int) (string, arg) {
	a := args[*i]
	if k, v, ok := strings.Cut(a.s, "="); ok && strings.HasPrefix(k, "--") {
		return k, arg{s: v, lit: a.lit}
	}
	if !s.fold && len(a.s) > 2 && a.s[1] != '-' && strings.IndexByte("XdFTo", a.s[1]) >= 0 {
		return a.s[:2], arg{s: a.s[2:], lit: a.lit}
	}
	name := a.s
	if s.fold {
		name = strings.ToLower(name)
	}
	if *i+1 < len(args) && inList(httpValued, name) {
		*i++
		return name, args[*i]
	}
	return name, arg{}
}

func (s *state) httpLabel(method string, body bool, hosts []string) {
	network := len(hosts) == 0
	for _, h := range hosts {
		if strings.HasPrefix(h, "169.254.") || h == "fd00:ec2::254" || h == "metadata.google.internal" {
			s.raise(Irreversible, "HTTP to metadata host "+h)
		}
		network = network || !loopback(h)
	}
	if network {
		s.flag(Network, "network: HTTP request")
	}
	if !body && (method == "" || method == "GET" || method == "HEAD") {
		return
	}
	for _, h := range hosts {
		if slices.Contains(s.env.MonetaryHosts, h) {
			s.raise(Monetary, "mutating HTTP to monetary host "+h)
		}
	}
	s.raise(Irreversible, "mutating HTTP request "+method)
}

func loopback(h string) bool {
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasPrefix(h, "127.") || h == "::1"
}

// hostOf extracts the lower-cased host of a URL or bare host[:port]/path.
func hostOf(u string) string {
	if _, rest, ok := strings.Cut(u, "://"); ok {
		u = rest
	}
	u, _, _ = strings.Cut(u, "/")
	u, _, _ = strings.Cut(u, "?")
	u = u[strings.LastIndexByte(u, '@')+1:]
	if strings.HasPrefix(u, "[") {
		u, _, _ = strings.Cut(u[1:], "]")
	} else if strings.Count(u, ":") == 1 {
		u, _, _ = strings.Cut(u, ":")
	}
	return strings.ToLower(u)
}

// remoteCmd labels ssh/scp/rsync/nc as irreversible; the ssh command
// string is still scanned for destructive patterns (paths not checked).
func (s *state) remoteCmd(name string, args []arg) {
	remote := inList(" ssh sftp nc ncat socat telnet ", name) || slices.ContainsFunc(args[1:], func(a arg) bool {
		k, _, ok := strings.Cut(a.s, ":")
		return ok && !strings.HasPrefix(a.s, "-") && len(k) > 1 && !strings.Contains(k, "/")
	})
	if !remote {
		s.operands("cp", args[1:])
		return
	}
	s.flag(Network, "network: "+name)
	s.raise(Irreversible, "remote: "+name)
	if rest := skipOpts(name, args[1:]); name == "ssh" && len(rest) > 1 {
		saved := s.remote
		s.remote = true
		s.inline(rest[1:], Bash)
		s.remote = saved
	}
}
