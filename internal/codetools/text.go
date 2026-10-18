package codetools

import (
	"fmt"
	"unicode/utf8"
)

// escapeInvalid renders a line holding invalid UTF-8 injectively (CODE-TOOLS
// §4.3): invalid bytes as \xNN and literal backslashes as \\. Valid lines
// are returned unchanged with n = 0; U+FFFD is never produced.
func escapeInvalid(line []byte) (out []byte, n int) {
	if utf8.Valid(line) {
		return line, 0
	}
	out = make([]byte, 0, len(line)+16)
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRune(line[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			out = fmt.Appendf(out, `\x%02X`, line[i])
			n++
		case r == '\\':
			out = append(out, '\\', '\\')
		default:
			out = append(out, line[i:i+size]...)
		}
		i += size
	}
	return out, n
}
