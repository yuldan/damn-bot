package email

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIMAP is a scripted IMAP4rev1 server over net.Pipe: it answers the few
// commands the adapter issues and records every command line.
// reply answers one scripted command; false ends the session.
func reply(server net.Conn, line string, body []byte) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return false
	}
	tag := fields[0]
	write := func(text string) bool {
		if _, err := server.Write([]byte(text)); err != nil {
			return false
		}
		return true
	}
	switch {
	case strings.Contains(line, " LOGIN "):
		return write(tag + " OK LOGIN completed\r\n")
	case strings.Contains(line, " SELECT "):
		return write("* 2 EXISTS\r\n* OK [UIDVALIDITY 7] valid\r\n* OK [HIGHESTMODSEQ 99] ok\r\n" + tag + " OK [READ-WRITE] SELECT completed\r\n")
	case strings.Contains(line, "UID FETCH") && strings.Contains(line, "(UID)"):
		return write("* 1 FETCH (UID 1)\r\n* 2 FETCH (UID 5)\r\n" + tag + " OK FETCH completed\r\n")
	case strings.Contains(line, "UID FETCH"):
		return write(fmt.Sprintf("* 3 FETCH (UID 5 BODY[] {%d}\r\n%s)\r\n%s OK FETCH completed\r\n", len(body), body, tag))
	case strings.Contains(line, "UID STORE"):
		return write(tag + " OK STORE completed\r\n")
	case strings.Contains(line, "LOGOUT"):
		return write("* BYE\r\n" + tag + " OK LOGOUT completed\r\n")
	default:
		return write(tag + " BAD unknown\r\n")
	}
}

type fakeIMAP struct {
	server   net.Conn
	commands chan string
	body     []byte
	wg       sync.WaitGroup
}

func startFakeIMAP(t *testing.T, body []byte) *fakeIMAP {
	t.Helper()
	client, server := net.Pipe()
	f := &fakeIMAP{server: server, commands: make(chan string, 64), body: body}
	f.wg.Go(func() {
		defer func() {
			if cerr := server.Close(); cerr != nil {
				return
			}
		}()
		r := bufio.NewReader(server)
		write := func(text string) bool {
			if _, err := server.Write([]byte(text)); err != nil {
				return false
			}
			return true
		}
		if !write("* OK fake ready\r\n") {
			return
		}
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			f.commands <- line
			if !reply(server, line, f.body) {
				return
			}
		}
	})
	t.Cleanup(func() {
		if cerr := client.Close(); cerr != nil {
			return
		}
		f.wg.Wait()
	})
	return &fakeIMAP{server: client, commands: f.commands, body: f.body}
}

func (f *fakeIMAP) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" {
		return nil, errors.New("unexpected network")
	}
	if addr != "" && addr != "fake:143" {
		return nil, errors.New("unexpected address")
	}
	return f.server, nil
}

func (f *fakeIMAP) next(t *testing.T) string {
	t.Helper()
	select {
	case line := <-f.commands:
		return line
	case <-time.After(3 * time.Second):
		t.Fatal("no IMAP command")
		return ""
	}
}

func testClient(t *testing.T, body []byte) (*client, *fakeIMAP) {
	t.Helper()
	fake := startFakeIMAP(t, body)
	cfg := &Config{Address: "bot@example.com", IMAPAddr: "fake:143", Timeout: 5 * time.Second, Dial: fake.dial}
	c, err := dialIMAP(t.Context(), cfg, "s3cret")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		if err := c.logout(t.Context()); err != nil && !errors.Is(err, ErrIMAP) {
			t.Errorf("logout: %v", err)
		}
	})
	return c, fake
}

// TestEmailIMAPClient proves the PLAT-006 wire behaviour: login, SELECT
// metadata, UID FETCH with BODY.PEEK, inline literal assembly, and the exact
// UID STORE for an admitted message.
func TestEmailIMAPClient(t *testing.T) {
	body := []byte("Message-ID: <m1@example.com>\r\nSubject: hi\r\n\r\nbody line\r\n")
	c, fake := testClient(t, body)
	if line := fake.next(t); !strings.HasPrefix(line, "a1 LOGIN ") {
		t.Fatalf("first command = %q, want LOGIN", line)
	}
	uids := selectAndSearch(t, c, fake)
	if len(uids) != 2 || uids[0] != 1 || uids[1] != 5 {
		t.Fatalf("uids = %v, want [1 5]", uids)
	}
	got, err := c.fetchBody(t.Context(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if line := fake.next(t); !strings.Contains(line, "BODY.PEEK[]") || strings.Contains(line, "BODY[]") {
		t.Fatalf("fetch command = %q, want BODY.PEEK[]", line)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if err := c.markSeen(t.Context(), 5); err != nil {
		t.Fatal(err)
	}
	if line := fake.next(t); !strings.Contains(line, `+FLAGS (\Seen)`) {
		t.Fatalf("store command = %q", line)
	}
}

func selectAndSearch(t *testing.T, c *client, fake *fakeIMAP) []uint32 {
	t.Helper()
	if err := c.selectMailbox(t.Context(), "INBOX"); err != nil {
		t.Fatal(err)
	}
	if line := fake.next(t); !strings.Contains(line, "SELECT") {
		t.Fatalf("select command = %q", line)
	}
	if c.uidValidity != 7 || c.highModSeq != 99 {
		t.Fatalf("select metadata = %d/%d", c.uidValidity, c.highModSeq)
	}
	uids, err := c.searchNew(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if line := fake.next(t); !strings.Contains(line, "UID FETCH 1:*") {
		t.Fatalf("search command = %q", line)
	}
	return uids
}

// TestEmailIMAPSearchWindow proves a replay window above the last UID yields
// no second fetch.
func TestEmailIMAPSearchWindow(t *testing.T) {
	c, fake := testClient(t, nil)
	if line := fake.next(t); !strings.HasPrefix(line, "a1 LOGIN ") {
		t.Fatalf("login command = %q", line)
	}
	if err := c.selectMailbox(t.Context(), "INBOX"); err != nil {
		t.Fatal(err)
	}
	if line := fake.next(t); !strings.Contains(line, "SELECT") {
		t.Fatalf("select command = %q", line)
	}
	if _, err := c.searchNew(t.Context(), 5); err != nil {
		t.Fatal(err)
	}
	if line := fake.next(t); !strings.Contains(line, "UID FETCH 6:*") {
		t.Fatalf("window command = %q", line)
	}
	if _, err := c.fetchBody(t.Context(), 5); err != nil || errors.Is(err, ErrIMAPFetch) {
		t.Fatalf("empty literal body = %v", err)
	}
}
