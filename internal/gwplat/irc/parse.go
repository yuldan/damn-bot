package irc

import "strings"

// message is one parsed IRC line (RFC 1459 + IRCv3 tags).
type message struct {
	tags    map[string]string
	nick    string
	command string
	params  []string
}

// parseLine parses one line without its CRLF. It reports false for an empty
// line.
func parseLine(line string) (message, bool) {
	line = strings.TrimRight(line, "\r\n")
	msg := message{tags: map[string]string{}}
	if line == "" {
		return msg, false
	}
	if strings.HasPrefix(line, "@") {
		end := strings.IndexByte(line, ' ')
		if end < 0 {
			return msg, false
		}
		msg.tags = parseTags(line[1:end])
		line = strings.TrimLeft(line[end:], " ")
	}
	if strings.HasPrefix(line, ":") {
		end := strings.IndexByte(line, ' ')
		if end < 0 {
			return msg, false
		}
		msg.nick = prefixNick(line[1:end])
		line = strings.TrimLeft(line[end:], " ")
	}
	for len(line) > 0 {
		if line[0] == ':' {
			msg.params = append(msg.params, line[1:])
			break
		}
		end := strings.IndexByte(line, ' ')
		if end < 0 {
			msg.params = append(msg.params, line)
			break
		}
		msg.params = append(msg.params, line[:end])
		line = strings.TrimLeft(line[end:], " ")
	}
	if len(msg.params) == 0 {
		return msg, false
	}
	msg.command = strings.ToUpper(msg.params[0])
	msg.params = msg.params[1:]
	return msg, true
}

func prefixNick(prefix string) string {
	if i := strings.IndexAny(prefix, "!@"); i >= 0 {
		return prefix[:i]
	}
	return prefix
}

// parseTags decodes the IRCv3 tag escape set; a key without '=' has an empty
// value.
func parseTags(raw string) map[string]string {
	tags := make(map[string]string)
	for part := range strings.SplitSeq(raw, ";") {
		if part == "" {
			continue
		}
		key, value, found := strings.Cut(part, "=")
		if !found {
			tags[key] = ""
			continue
		}
		tags[key] = unescapeTag(value)
	}
	return tags
}

func unescapeTag(value string) string {
	if !strings.ContainsRune(value, '\\') {
		return value
	}
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' || i+1 >= len(value) {
			b.WriteByte(value[i])
			continue
		}
		i++
		switch value[i] {
		case ':':
			b.WriteByte(';')
		case 's':
			b.WriteByte(' ')
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte('\\')
			b.WriteByte(value[i])
		}
	}
	return b.String()
}

func lastParam(m message) string {
	if len(m.params) == 0 {
		return ""
	}
	return m.params[len(m.params)-1]
}
