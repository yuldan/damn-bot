package email

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
)

// ErrMIME rejects a message that cannot be parsed.
var ErrMIME = errors.New("email: cannot parse message")

const (
	maxTextBytes = 1 << 20
	maxParts     = 64
	maxDepth     = 6
)

// message is the authenticated subset of one email the adapter routes on.
type message struct {
	MessageID string
	From      string
	InReplyTo string
	ListID    string
	Subject   string
	Body      string
}

// parseMIME extracts headers and the best text/plain body of a raw message.
// A malformed part is skipped; only an unreadable header is an error.
func parseMIME(raw []byte) (*message, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMIME, err)
	}
	from, err := mail.ParseAddress(msg.Header.Get("From"))
	if err != nil {
		from = &mail.Address{Address: strings.TrimSpace(msg.Header.Get("From"))}
	}
	body, err := extractText(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body, 0)
	if err != nil {
		return nil, err
	}
	return &message{
		MessageID: msg.Header.Get("Message-Id"),
		From:      from.Address,
		InReplyTo: msg.Header.Get("In-Reply-To"),
		ListID:    firstToken(msg.Header.Get("List-Id")),
		Subject:   msg.Header.Get("Subject"),
		Body:      body,
	}, nil
}

func firstToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if open := strings.IndexByte(raw, '<'); open >= 0 {
		if close := strings.IndexByte(raw[open:], '>'); close > 0 {
			return raw[open+1 : open+close]
		}
	}
	if i := strings.IndexAny(raw, " \t"); i > 0 {
		return raw[:i]
	}
	return raw
}

// extractText walks a MIME tree, preferring text/plain and falling back to a
// tag-stripped text/html part. Non-text parts are ignored.
func extractText(contentType, encoding string, r io.Reader, depth int) (string, error) {
	if depth > maxDepth {
		return "", fmt.Errorf("%w: nesting too deep", ErrMIME)
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		return extractMultipart(params["boundary"], r, depth)
	}
	if mediaType != "text/plain" && mediaType != "text/html" {
		return "", nil
	}
	text, err := readText(encoding, r)
	if err != nil {
		return "", err
	}
	if mediaType == "text/html" {
		return stripHTML(text), nil
	}
	return text, nil
}

func extractMultipart(boundary string, r io.Reader, depth int) (string, error) {
	if boundary == "" {
		return "", fmt.Errorf("%w: multipart without boundary", ErrMIME)
	}
	reader := multipart.NewReader(r, boundary)
	fallback := ""
	for range maxParts {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrMIME, err)
		}
		text, err := extractText(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part, depth+1)
		if err != nil || text == "" {
			continue
		}
		if strings.HasPrefix(part.Header.Get("Content-Type"), "text/plain") {
			return text, nil
		}
		if fallback == "" {
			fallback = text
		}
	}
	return fallback, nil
}

func readText(encoding string, r io.Reader) (string, error) {
	limited := io.LimitReader(r, maxTextBytes)
	reader := io.Reader(limited)
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, limited)
	case "quoted-printable":
		reader = quotedprintable.NewReader(limited)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrMIME, err)
	}
	return string(data), nil
}

// stripHTML drops tags and collapses whitespace; it is a fallback body, not a
// renderer.
func stripHTML(text string) string {
	var out strings.Builder
	depth := 0
	for _, r := range text {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				out.WriteRune(r)
			}
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}
