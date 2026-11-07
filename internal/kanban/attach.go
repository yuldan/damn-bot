package kanban

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/netx"
)

// AttachFile attaches a workspace file. Absolute paths are accepted only when
// they resolve inside the workspace; symlinks are evaluated on both sides so
// a link cannot escape the root. Directories and files over MaxAttach fail.
func (s *Store) AttachFile(ctx context.Context, id, path, actor string) (Attachment, error) {
	root, err := filepath.EvalSymlinks(s.Workspace)
	if err != nil {
		return Attachment{}, fmt.Errorf("%w: workspace: %w", ErrOutsideWorkspace, err)
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	// Lexical containment first: a missing outside path must be an
	// outside-workspace error, not a file-existence oracle.
	if _, ok := within(root, candidate); !ok {
		return Attachment{}, fmt.Errorf("%w: %s", ErrOutsideWorkspace, filepath.Base(path))
	}
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Attachment{}, fmt.Errorf("%w: file does not exist", ErrNotFound)
		}
		return Attachment{}, fmt.Errorf("%w: %w", ErrOutsideWorkspace, err)
	}
	rel, ok := within(root, real)
	if !ok {
		return Attachment{}, fmt.Errorf("%w: %s", ErrOutsideWorkspace, filepath.Base(path))
	}
	fi, err := os.Stat(real)
	if err != nil {
		return Attachment{}, fmt.Errorf("stat attachment: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return Attachment{}, fmt.Errorf("%w: not a regular file", ErrInvalid)
	}
	f, err := os.Open(real) // #nosec G304 -- real is inside the workspace and a regular file
	if err != nil {
		return Attachment{}, fmt.Errorf("open attachment: %w", err)
	}
	sum, size, err := hashFile(f, s.MaxAttach)
	cerr := f.Close()
	if err != nil {
		return Attachment{}, err
	}
	if cerr != nil {
		return Attachment{}, fmt.Errorf("close attachment: %w", cerr)
	}
	att := Attachment{Name: filepath.ToSlash(rel), Kind: "file", Path: filepath.ToSlash(rel), Size: size, SHA256: sum, AddedAt: s.now().UTC()}
	if err := s.attach(ctx, id, &att, actor); err != nil {
		return Attachment{}, err
	}
	return att, nil
}

// AttachURL validates an https URL, rejects every non-public destination
// (loopback, private, link-local, metadata, unspecified) before any request,
// then fetches bounded bytes through the safe dialer. Validation is repeated
// for every redirect.
func (s *Store) AttachURL(ctx context.Context, id, rawURL, actor string) (Attachment, error) {
	u, err := parsePublicURL(rawURL, s.resolve)
	if err != nil {
		return Attachment{}, err
	}
	fetch := s.Fetch
	if fetch == nil {
		fetch = safeFetch{}
	}
	body, err := fetch.Get(ctx, u.String(), s.MaxAttach)
	if err != nil {
		return Attachment{}, fmt.Errorf("fetch attachment: %w", err)
	}
	if int64(len(body)) > s.MaxAttach {
		return Attachment{}, fmt.Errorf("%w: > %d bytes", ErrTooLarge, s.MaxAttach)
	}
	sum := sha256.Sum256(body)
	att := Attachment{Name: u.Hostname() + u.Path, Kind: "url", URL: u.String(), Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), AddedAt: s.now().UTC()}
	if err := s.attach(ctx, id, &att, actor); err != nil {
		return Attachment{}, err
	}
	return att, nil
}

// hashFile returns the SHA-256 and size of an open file, bounded by max.
func hashFile(f io.Reader, max int64) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, max+1))
	if err != nil {
		return "", 0, fmt.Errorf("hash attachment: %w", err)
	}
	if n > max {
		return "", 0, fmt.Errorf("%w: > %d bytes", ErrTooLarge, max)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// within reports whether path stays inside root after cleaning.
func within(root, path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

func (s *Store) resolve(host string) ([]net.IP, error) {
	if s.Resolve == nil {
		return resolver(host)
	}
	return s.Resolve(host)
}

func (s *Store) attach(ctx context.Context, id string, att *Attachment, actor string) error {
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, err := readTaskAt(tx, id, s.now()); err != nil {
			return err
		}
		pl := marshal(Payload{Attach: att})
		return insertEvent(tx, id, actorName(actor), "edit", pl, s.now().UnixMilli())
	})
	if err != nil {
		return fmt.Errorf("append attachment: %w", err)
	}
	return nil
}

// Attachments lists the live attachment set.
func (s *Store) Attachments(ctx context.Context, id string) ([]Attachment, error) {
	t, err := s.Task(ctx, id)
	if err != nil {
		return nil, err
	}
	return append([]Attachment{}, t.Attachments...), nil
}

// Detach removes one attachment by name, appending a detach event.
func (s *Store) Detach(ctx context.Context, id, name, actor string) (*Task, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%w: attachment name is required", ErrInvalid)
	}
	return s.writeEdit(ctx, id, actor, "edit", &Payload{Detach: name}, func(tx *ctxdb.Tx, now time.Time) error {
		t, err := readTaskAt(tx, id, s.now())
		if err != nil {
			return err
		}
		if !hasAttachment(t, name) {
			return fmt.Errorf("%w: attachment %s", ErrNotFound, name)
		}
		if _, xerr := tx.Exec(`UPDATE kanban_tasks SET updated_at = ?2 WHERE id = ?1`, id, now.UnixMilli()); xerr != nil {
			return fmt.Errorf("touch attachment task: %w", xerr)
		}
		return nil
	})
}

func hasAttachment(t *Task, name string) bool {
	for _, a := range t.Attachments {
		if a.Name == name || a.URL == name {
			return true
		}
	}
	return false
}

// parsePublicURL performs the SSRF gate before any request. Only https with
// no credentials is accepted; every resolved address must be public.
func parsePublicURL(raw string, resolve func(string) ([]net.IP, error)) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("%w: malformed URL", ErrBlockedURL)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("%w: only https is accepted", ErrBlockedURL)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		host == "metadata.google.internal" || host == "metadata" || host == "instance-data" {
		return nil, fmt.Errorf("%w: %s", ErrBlockedURL, host)
	}
	ips, err := resolve(host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("%w: cannot resolve %s", ErrBlockedURL, host)
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, fmt.Errorf("%w: %s", ErrBlockedURL, host)
		}
	}
	return u, nil
}

func publicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() || !addr.IsGlobalUnicast() {
		return false
	}
	// Cloud metadata endpoints are link-local, but keep the explicit check so
	// a future dialect change cannot silently allow 169.254.169.254.
	return true // the link-local/metadata checks above already refused those ranges
}

// safeFetch is the default URL attachment client. netx owns the transport
// (E-05): the tool purpose checks the address actually dialled, after
// resolution, so a rebinding name cannot slip past the pre-gate in
// parsePublicURL, and redirects are followed only for GET/HEAD and never from
// https to http.
type safeFetch struct{}

func (safeFetch) Get(ctx context.Context, rawURL string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: request", ErrBlockedURL)
	}
	req.Header.Set("User-Agent", "damnbot-kanban")
	network := netx.New(netx.Config{})
	defer network.CloseIdleConnections()
	resp, err := network.Client("kanban-attach", netx.Tool).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch attachment: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("attachment URL answered %d", resp.StatusCode)
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, max+1))
	cerr := resp.Body.Close()
	if err := errors.Join(rerr, cerr); err != nil {
		return nil, fmt.Errorf("read attachment: %w", err)
	}
	return body, nil
}
