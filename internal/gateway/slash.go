package gateway

import (
	"strings"
	"unicode"
)

// maxCommandName bounds a command word. Telegram bot commands are 1..32
// characters, so a longer word is never a command addressed to a bot.
const maxCommandName = 32

// Command is one parsed chat slash command (SPEC §9.7 "Perintah chat"). Parse
// only splits text: it never resolves a command, checks a role, touches an
// approval, or runs anything.
type Command struct {
	// Name is the command name without the leading '/', lower-case ASCII.
	Name string
	// Mention is the text after '@' in the command token ("/status@bot"), ""
	// when the token carries no mention.
	Mention string
	// Args are the non-empty whitespace-separated fields after the command
	// token; repeated spaces therefore never produce empty arguments.
	Args []string
	// Rest is the text after the command token, trimmed at both ends but with
	// its inner spacing and newlines intact.
	Rest string
}

// Parse splits one chat message of the form "/name[@bot] args…". ok is false
// when text is not a slash command at all, and the caller must treat the
// message as plain text: no leading '/', an empty/over-long name, or a name
// that is not a plain command word (ASCII letters, digits, '_' and '-'). That
// last rule is why "/etc/hosts" is text and is never mistaken for a command;
// Parse does not shell out to anything, and a command word never contains '/',
// ';', '$' or other shell syntax.
//
// Leading spaces/tabs are tolerated, and a mention is split out rather than
// discarded so the dispatcher can ignore commands addressed to another bot.
func Parse(text string) (Command, bool) {
	text = strings.TrimLeft(text, " \t")
	if !strings.HasPrefix(text, "/") {
		return Command{}, false
	}
	body := text[1:]
	token, rest := body, ""
	if i := strings.IndexFunc(body, unicode.IsSpace); i >= 0 {
		token, rest = body[:i], strings.TrimSpace(body[i:])
	}
	name, mention, hasMention := strings.Cut(token, "@")
	if !commandWord(name) {
		return Command{}, false
	}
	if hasMention && !botMention(mention) {
		return Command{}, false
	}
	var args []string
	if rest != "" {
		args = strings.Fields(rest)
	}
	return Command{
		Name:    strings.ToLower(name),
		Mention: mention,
		Args:    args,
		Rest:    rest,
	}, true
}

// commandWord reports whether s is a plain command name: it starts with an
// ASCII letter and continues with ASCII letters, digits, '_' or '-'.
func commandWord(s string) bool {
	if s == "" || len(s) > maxCommandName {
		return false
	}
	for i := range s {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// botMention reports whether s is a plausible platform bot username
// (letters, digits, '_').
func botMention(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := range s {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}
