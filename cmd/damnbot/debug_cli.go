//go:build !minimal

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/damnbot/damnbot/internal/secret"
)

const f8DebugKeySize = 32

func runDebug(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("debug", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory (default: profile data dir)")
	if err := fs.Parse(args); err != nil {
		return f8ExitUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		debugUsage(stderr)
		return f8ExitUsage
	}
	dir, err := f8Dir(*dataDir)
	if err != nil {
		return f8CmdFail(stderr, "debug", err)
	}
	switch rest[0] {
	case "share":
		code, err := debugShare(rest[1:], dir, stdout, stderr)
		if err != nil {
			if errors.Is(err, errF8Usage) {
				return code
			}
			return f8CmdFail(stderr, "debug share", err)
		}
		return code
	case "delete":
		if len(rest) != 1 {
			say(stderr, "usage: damnbot debug delete [--data-dir DIR]\n")
			return f8ExitUsage
		}
		return debugDelete(dir, stdout, stderr)
	case "trace":
		return debugTrace(rest[1:], stderr)
	default:
		say(stderr, "debug: unknown subcommand %q\n", cleanArg(rest[0]))
		debugUsage(stderr)
		return f8ExitUsage
	}
}

func debugUsage(w io.Writer) {
	say(w, "usage: damnbot debug share --out FILE --key-file KEY [--data-dir DIR]\n")
	say(w, "       damnbot debug delete [--data-dir DIR]\n")
	say(w, "       damnbot debug trace --file TRACE --upload [--data-dir DIR]\n")
	say(w, "share encrypts the redacted setup summary with the 32-byte key; no network upload.\n")
}

func debugShare(args []string, dataDir string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("debug share", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "artifact path (required)")
	keyPath := fs.String("key-file", "", "32-byte key file (required)")
	if err := fs.Parse(args); err != nil {
		return f8ExitUsage, errF8Usage
	}
	if fs.NArg() != 0 || *out == "" || *keyPath == "" {
		debugUsage(stderr)
		return f8ExitUsage, errF8Usage
	}
	key, err := readDebugKey(*keyPath)
	if err != nil {
		return f8ExitFail, err
	}
	codec, err := secret.NewCodec(key)
	if err != nil {
		return f8ExitFail, fmt.Errorf("codec: %w", err)
	}
	cfg, paths, _, err := f8LoadSetup("", dataDir)
	if err != nil {
		return f8ExitFail, err
	}
	report := f8DumpReport(&cfg, paths, dataDir)
	payload := f8Redact(f8DumpText(&report))
	sealed, err := codec.Seal("debug-share", []byte(payload))
	if err != nil {
		return f8ExitFail, fmt.Errorf("encrypt: %w", err)
	}
	path := *out
	if filepath.Ext(path) == "" {
		path += ".enc"
	}
	if err := f8WriteFile(path, sealed); err != nil {
		return f8ExitFail, err
	}
	sum := sha256.Sum256(sealed)
	say(stdout, "shared %s bytes=%d key=%s sha256=%s\n", path, len(sealed), codec.ID(), hex.EncodeToString(sum[:]))
	return f8ExitOK, nil
}

// debugTrace is the opt-in Hugging Face trace upload of OPS-022. No upload
// lane (and no netx egress entry) exists in this build, so an explicit
// --upload refuses with the typed module_unavailable line and sends nothing.
// The trace file is never opened here: 0 transcript bytes leave the process.
func debugTrace(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("debug trace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "trace file (required with --upload)")
	upload := fs.Bool("upload", false, "upload to Hugging Face (opt-in)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *file == "" || !*upload {
		say(stderr, "usage: damnbot debug trace --file TRACE --upload [--data-dir DIR]\n")
		return f8ExitUsage
	}
	return f8CmdFail(stderr, "debug trace", f8Unavail("trace-upload",
		"no Hugging Face trace upload lane is configured; nothing was sent"))
}

func readDebugKey(path string) ([]byte, error) {
	fi, err := os.Lstat(path) // #nosec G304 -- operator-supplied key file
	if err != nil {
		return nil, fmt.Errorf("key file: %w", err)
	}
	if !fi.Mode().IsRegular() || (runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0) {
		return nil, fmt.Errorf("key file must be a regular file only readable by its owner")
	}
	b, err := os.ReadFile(path) // #nosec G304 -- operator-supplied key file
	if err != nil {
		return nil, fmt.Errorf("key file: %w", err)
	}
	if len(b) != f8DebugKeySize {
		return nil, fmt.Errorf("key file must be exactly %d bytes", f8DebugKeySize)
	}
	return b, nil
}

func debugDelete(dataDir string, stdout, stderr io.Writer) int {
	dir := filepath.Join(dataDir, "debug")
	if err := os.RemoveAll(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			say(stdout, "nothing to delete\n")
			return f8ExitOK
		}
		return f8CmdFail(stderr, "debug delete", err)
	}
	say(stdout, "deleted %s\n", dir)
	return f8ExitOK
}
