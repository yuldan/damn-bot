package irc

import "testing"

// TestParseLine locks the IRCv3 line parser the SASL adapter depends on: tags
// with the escape set, a nick prefix that is not an identity, the command, and
// trailing parameters.
func TestParseLine(t *testing.T) {
	t.Run("privmsg", checkPrivmsg)
	t.Run("escapes", checkEscapes)
	t.Run("rejects", checkRejects)
}

func checkPrivmsg(t *testing.T) {
	t.Helper()
	m, ok := parseLine("@account=alice;msgid=a1;flag :mallory!u@h PRIVMSG #chan :hello world\r\n")
	if !ok {
		t.Fatal("line rejected")
	}
	if m.nick != "mallory" || m.command != "PRIVMSG" || len(m.params) != 2 {
		t.Fatalf("message = %+v", m)
	}
	if m.tags["account"] != "alice" || m.tags["msgid"] != "a1" || m.tags["flag"] != "" {
		t.Fatalf("tags = %+v", m.tags)
	}
	if m.params[0] != "#chan" || m.params[1] != "hello world" {
		t.Fatalf("params = %v", m.params)
	}
	if nick := prefixNick("nick!user@host"); nick != "nick" {
		t.Fatalf("prefix nick = %q", nick)
	}
	if nick := prefixNick("server.example"); nick != "server.example" {
		t.Fatalf("server prefix = %q", nick)
	}
}

func checkEscapes(t *testing.T) {
	t.Helper()
	escaped, ok := parseLine("@note=one\\stwo\\:three\\\\four\\r\\n :srv 001 bot :welcome")
	if !ok {
		t.Fatal("escaped line rejected")
	}
	if got := escaped.tags["note"]; got != "one two;three\\four\r\n" {
		t.Fatalf("unescaped tag = %q", got)
	}
	if escaped.command != "001" || lastParam(escaped) != "welcome" {
		t.Fatalf("server line = %+v", escaped)
	}
}

func checkRejects(t *testing.T) {
	t.Helper()
	if _, ok := parseLine(""); ok {
		t.Fatal("empty line accepted")
	}
	if _, ok := parseLine("@only-tags"); ok {
		t.Fatal("tag-only line accepted")
	}
}
