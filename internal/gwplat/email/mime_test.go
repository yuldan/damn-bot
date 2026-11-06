package email

import (
	"errors"
	"strings"
	"testing"
)

// TestEmailMIMEParse covers the PLAT-006 extraction contract: multipart
// alternative prefers text/plain, transfer encodings are decoded, and HTML is
// only a stripped fallback.
func TestEmailMIMEParse(t *testing.T) {
	multipart := strings.Join([]string{
		"Message-ID: <m1@example.com>",
		"From: Alice <alice@example.com>",
		"To: bot@example.com",
		"Subject: hello",
		"In-Reply-To: <r1@example.com>",
		"List-Id: Team <team.example.com>",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=b1",
		"",
		"--b1",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"plain body",
		"--b1",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>html body</p>",
		"--b1--",
		"",
	}, "\r\n")
	msg, err := parseMIME([]byte(multipart))
	if err != nil {
		t.Fatal(err)
	}
	if msg.From != "alice@example.com" || msg.MessageID != "<m1@example.com>" || msg.ListID != "team.example.com" {
		t.Fatalf("headers = %+v", msg)
	}
	if strings.TrimSpace(msg.Body) != "plain body" {
		t.Fatalf("body = %q, want plain part", msg.Body)
	}

	encoded := strings.Join([]string{
		"From: alice@example.com",
		"Content-Type: text/plain",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"caf=C3=A9",
		"",
	}, "\r\n")
	qp, err := parseMIME([]byte(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(qp.Body) != "café" {
		t.Fatalf("quoted-printable body = %q", qp.Body)
	}

	html := strings.Join([]string{
		"From: alice@example.com",
		"Content-Type: text/html",
		"",
		"<p>hello <b>world</b></p>",
		"",
	}, "\r\n")
	h, err := parseMIME([]byte(html))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.Body, "<") || !strings.Contains(h.Body, "hello world") {
		t.Fatalf("html fallback = %q", h.Body)
	}

	if _, err := parseMIME([]byte("not a message")); !errors.Is(err, ErrMIME) {
		t.Fatalf("malformed = %v, want ErrMIME", err)
	}
}
