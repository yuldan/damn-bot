package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

// GW-024: `damnbot gateway chat` is a local REPL over the live gateway. A line
// becomes a run/spool chat request; serve (the lease holder) admits it, runs
// the same turn loop as a platform message, and appends the answer to
// run/chat/<id>.jsonl. The CLI never opens the database or calls a model; with
// no serve lease it refuses (SPEC §10 has no one-shot mode).
const (
	chatReplyPoll = 25 * time.Millisecond
	chatLeasePoll = 500 * time.Millisecond
	chatPrompt    = "chat> "
	chatDeltaMin  = 256
)

// chatReplyLine is one line of a chat answer file. status/accepted, notice,
// delta and the terminal done/error/rejected are the whole protocol.
type chatReplyLine struct {
	Kind   string `json:"kind"`
	Text   string `json:"text,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func chatReplyPath(dataDir, id string) string {
	return filepath.Join(dataDir, "run", "chat", id+".jsonl")
}

// validChatRequestID is the only form accepted as a file name: 16 lowercase hex
// characters (8 random bytes). It is checked on both sides, so a request can
// never escape run/chat.
func validChatRequestID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func newChatRequestID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("gateway chat: request id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// serveLease probes the data-dir lease without taking it: a busy flock means a
// live serve owns the directory. The probe never creates serve.lock, and a
// failed probe or release is returned instead of ignored.
func serveLease(dataDir string) (bool, error) {
	path := filepath.Join(dataDir, "run", "serve.lock")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("gateway chat: probe %s: %w", path, err)
	}
	f, err := tryFlock(path)
	if err != nil {
		if errors.Is(err, errFlockBusy) {
			return true, nil
		}
		return false, fmt.Errorf("gateway chat: probe %s: %w", path, err)
	}
	if uerr := unlockFlock(f); uerr != nil {
		return false, fmt.Errorf("gateway chat: release probe %s: %w", path, uerr)
	}
	return false, nil
}

// configuredGatewayIdentity returns the sender the CLI speaks as. It is the
// configured owner or first allowlisted identity of the platform, so admission
// still authorizes the message exactly as it would a platform update.
func configuredGatewayIdentity(platform string) (string, error) {
	specs := append([]string{os.Getenv("DAMNBOT_GATEWAY_OWNER")},
		strings.Split(os.Getenv("DAMNBOT_GATEWAY_ALLOW"), ",")...)
	for _, spec := range specs {
		p, id, ok := strings.Cut(strings.TrimSpace(spec), ":")
		if ok && p == platform && id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("no configured %s identity; set DAMNBOT_GATEWAY_OWNER or DAMNBOT_GATEWAY_ALLOW",
		cleanArg(platform))
}

// gatewayChat implements the `gateway chat` subcommand. A non-interactive
// stdin (a pipe) reads one message per line and stops at the first rejection;
// an interactive terminal keeps the REPL open after a denial. Ctrl-D ends the
// loop, Ctrl-C cancels the pending wait and exits 130.
func gatewayChat(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway chat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	platform := fs.String("platform", "telegram", "target platform")
	chat := fs.String("chat", "", "chat id (default: the configured identity)")
	thread := fs.String("thread", "", "optional thread/topic id")
	dataDirFlag := fs.String("data-dir", "", "data directory (default: profile path)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || strings.TrimSpace(*platform) == "" {
		gatewayChatUsage(stderr)
		return 2
	}
	dataDir, err := sendDataDir(*dataDirFlag)
	if err != nil {
		sessionsPrintf(stderr, "gateway chat: %s\n", cleanLine(err.Error()))
		return 1
	}
	running, lerr := serveLease(dataDir)
	if lerr != nil {
		sessionsPrintf(stderr, "gateway chat: %s\n", cleanLine(lerr.Error()))
		return 1
	}
	if !running {
		sessionsPrintf(stderr, "gateway chat: serve is not running for %s; start `damnbot serve` first\n",
			cleanArg(dataDir))
		sessionsPrintf(stderr, "gateway chat: SPEC §10 makes this a client of the live gateway; there is no one-shot mode\n")
		return 1
	}
	sender, err := configuredGatewayIdentity(*platform)
	if err != nil {
		sessionsPrintf(stderr, "gateway chat: %s\n", cleanLine(err.Error()))
		return 1
	}
	chatID := strings.TrimSpace(*chat)
	if chatID == "" {
		chatID = sender
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	target := chatTarget{dataDir: dataDir, platform: *platform, chat: chatID, thread: *thread, sender: sender}
	return chatREPL(ctx, &target, stdinIsTerminal(), stdout, stderr)
}

// chatTarget is the conversation the REPL writes to; every line reuses it with a fresh id.
type chatTarget struct {
	dataDir, platform, chat, thread, sender string
}

func chatREPL(ctx context.Context, target *chatTarget, interactive bool, stdout, stderr io.Writer) int {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	code := 0
	for {
		if interactive {
			sessionsPrintf(stderr, chatPrompt)
		}
		if !sc.Scan() {
			break
		}
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		id, err := newChatRequestID()
		if err != nil {
			sessionsPrintf(stderr, "gateway chat: %s\n", cleanLine(err.Error()))
			return 1
		}
		req := sendSpoolRequest{
			Op: "chat", Platform: target.platform, Chat: target.chat, Thread: target.thread,
			Sender: target.sender, Text: text, ID: id,
		}
		if qerr := queueSend(target.dataDir, &req); qerr != nil {
			sessionsPrintf(stderr, "gateway chat: %s\n", cleanLine(qerr.Error()))
			return 1
		}
		code = awaitChatReply(ctx, target.dataDir, id, stdout, stderr)
		if ctx.Err() != nil {
			return 130
		}
		if code != 0 && !interactive {
			return code
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		sessionsPrintf(stderr, "gateway chat: stdin: %s\n", cleanLine(err.Error()))
		return 1
	}
	return code
}

func gatewayChatUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot gateway chat [--platform telegram] [--chat ID] [--thread ID] [--data-dir DIR]\n")
	sessionsPrintf(w, "       speaks as DAMNBOT_GATEWAY_OWNER or the first DAMNBOT_GATEWAY_ALLOW identity\n")
	sessionsPrintf(w, "       requires a running `damnbot serve` on the same data dir\n")
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// awaitChatReply tails one reply file until a terminal line, stopping with a
// clear error when the serve lease disappears so a dead daemon cannot hang the CLI.
func awaitChatReply(ctx context.Context, dataDir, id string, stdout, stderr io.Writer) int {
	path := chatReplyPath(dataDir, id)
	processed, printed, code := 0, false, 0
	lastLease := time.Now()
	for {
		if drainChatReply(path, &processed, &printed, &code, stdout, stderr) {
			if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				sessionsPrintf(stderr, "gateway chat: remove reply: %s\n", cleanLine(rerr.Error()))
			}
			return code
		}
		if time.Since(lastLease) >= chatLeasePoll {
			lastLease = time.Now()
			running, lerr := serveLease(dataDir)
			if lerr != nil {
				sessionsPrintf(stderr, "gateway chat: %s\n", cleanLine(lerr.Error()))
				return 1
			}
			if !running {
				sessionsPrintf(stderr, "gateway chat: serve stopped while waiting for a reply\n")
				return 1
			}
		}
		select {
		case <-ctx.Done():
			sessionsPrintf(stderr, "gateway chat: interrupted\n")
			return 130
		case <-time.After(chatReplyPoll):
		}
	}
}

// drainChatReply consumes complete lines after *processed and reports whether
// the request reached a terminal line.
func drainChatReply(path string, processed *int, printed *bool, code *int, stdout, stderr io.Writer) bool {
	data, err := os.ReadFile(path) // #nosec G304 -- path is <data-dir>/run/chat/<16-hex-id>.jsonl under the 0700 run dir; validChatRequestID checked the id
	if err != nil {
		return false
	}
	parts := strings.Split(string(data), "\n")
	complete := len(parts) - 1 // the tail after the last newline may be partial
	for _, raw := range parts[*processed:complete] {
		var line chatReplyLine
		if jerr := json.Unmarshal([]byte(raw), &line); jerr != nil {
			continue
		}
		switch line.Kind {
		case "status":
			sessionsPrintf(stderr, "status: %s\n", cleanLine(line.Text))
		case "notice":
			sessionsPrintf(stderr, "notice: %s\n", cleanLine(line.Text))
		case "delta":
			if _, werr := io.WriteString(stdout, line.Text); werr != nil {
				*code = 1
				return true
			}
			*printed = true
		case "done":
			if *printed {
				sessionsPrintf(stdout, "\n")
			}
			*code = 0
			return true
		case "rejected":
			sessionsPrintf(stderr, "rejected: %s\n", cleanLine(line.Reason))
			*code = 1
			return true
		case "error":
			if *printed {
				sessionsPrintf(stdout, "\n")
			}
			sessionsPrintf(stderr, "error: %s\n", cleanLine(line.Text))
			*code = 1
			return true
		}
	}
	*processed = complete
	return false
}
