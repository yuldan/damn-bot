package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"io"
	"strings"
)

// runSend is CLI-018/GW-010: a durable outbox request with no turn or model.
func runSend(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	platform := fs.String("platform", "telegram", "target platform (telegram)")
	chat := fs.String("chat", "", "target chat id")
	thread := fs.String("thread", "", "optional thread/topic id")
	text := fs.String("text", "", "message text")
	dataDirFlag := fs.String("data-dir", "", "data directory (default: profile path)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || strings.TrimSpace(*chat) == "" || strings.TrimSpace(*text) == "" {
		sendUsage(stderr)
		return 2
	}
	dataDir, err := dataDirFor(*dataDirFlag)
	if err != nil {
		sessionsPrintf(stderr, "send: %s\n", cleanLine(err.Error()))
		return 1
	}
	req := spoolRequest{Op: "send", Platform: *platform, Chat: *chat, Thread: *thread, Text: *text}
	req.Key = sendKey(req.Platform, req.Chat, req.Thread, req.Text)
	if err := queueRequest(dataDir, req); err != nil {
		sessionsPrintf(stderr, "send: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "queued %s:%s key=%s\n", cleanArg(req.Platform), cleanArg(req.Chat), req.Key)
	return 0
}

func sendUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot send --platform telegram --chat <id> [--thread <id>] --text <text> [--data-dir DIR]\n")
	sessionsPrintf(w, "       identical text reuses one idempotency key\n")
}

func sendKey(platform, chat, thread, text string) string {
	sum := sha256.Sum256([]byte(platform + "\x00" + chat + "\x00" + thread + "\x00" + text))
	return "cli:" + hex.EncodeToString(sum[:12])
}
