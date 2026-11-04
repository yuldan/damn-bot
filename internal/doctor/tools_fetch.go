//go:build !minimal

package doctor

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Fetch errors. Like the lock errors they never carry a URL.
var (
	ErrToolLicense  = errors.New("doctor tools: license not confirmed")
	ErrToolChecksum = errors.New("doctor tools: checksum mismatch")
	ErrToolDownload = errors.New("doctor tools: download failed")
	ErrToolExtract  = errors.New("doctor tools: archive does not contain the pinned binary")
)

// Downloader fetches one artifact into an open temp file. The HTTP
// implementation resumes an existing partial download with a Range request.
type Downloader interface {
	Download(ctx context.Context, a *ToolArtifact, dst *os.File) error
}

// Doer is the one-method HTTP seam (netx.Client or http.Client).
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// HTTPDownloader downloads pinned release artifacts.
type HTTPDownloader struct{ Client Doer }

// Download implements Downloader with HTTP Range resume.
func (d HTTPDownloader) Download(ctx context.Context, a *ToolArtifact, dst *os.File) (err error) {
	if d.Client == nil {
		return fmt.Errorf("%w: no HTTP client", ErrToolDownload)
	}
	off, err := resumeFrom(dst, a.Size)
	if err != nil {
		return err
	}
	resp, err := d.get(ctx, a, off)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	off, err = rangeStart(resp, dst, off)
	if errors.Is(err, errRangeComplete) {
		return nil
	}
	if err != nil {
		return err
	}
	return copyRange(dst, resp.Body, off, a.Size)
}

func resumeFrom(dst *os.File, size int64) (int64, error) {
	off, err := dst.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrToolDownload, err)
	}
	if off < size {
		return off, nil
	}
	if err := dst.Truncate(0); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrToolDownload, err)
	}
	return 0, nil
}

func (d HTTPDownloader) get(ctx context.Context, a *ToolArtifact, off int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: bad URL", ErrToolDownload)
	}
	if off > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrToolDownload, urlFree(err))
	}
	return resp, nil
}

// rangeStart returns the offset the body starts at, restarting from zero when
// the server ignored the Range request.
func rangeStart(resp *http.Response, dst *os.File, off int64) (int64, error) {
	switch {
	case resp.StatusCode == http.StatusPartialContent:
		return off, nil
	case resp.StatusCode == http.StatusOK && off > 0:
		if err := dst.Truncate(0); err != nil {
			return 0, fmt.Errorf("%w: %w", ErrToolDownload, err)
		}
		return 0, nil
	case resp.StatusCode == http.StatusOK:
		return off, nil
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && off > 0:
		return off, errRangeComplete // size/hash decide over the existing file
	default:
		return 0, fmt.Errorf("%w: HTTP %d", ErrToolDownload, resp.StatusCode)
	}
}

func copyRange(dst *os.File, body io.Reader, off, size int64) error {
	if _, err := dst.Seek(off, io.SeekStart); err != nil {
		return fmt.Errorf("%w: %w", ErrToolDownload, err)
	}
	n, err := io.Copy(dst, io.LimitReader(body, size+1))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrToolDownload, urlFree(err))
	}
	if off+n > size {
		return fmt.Errorf("%w: artifact larger than pinned size", ErrToolDownload)
	}
	return nil
}

// errRangeComplete says the temp file already covers the artifact.
var errRangeComplete = errors.New("doctor tools: range already complete")

// urlFree strips a *url.Error (it embeds the URL, which may carry a token).
func urlFree(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err
	}
	return err
}

// FetchOptions is one `doctor tools fetch <name>`.
type FetchOptions struct {
	Root          string // data dir
	Name          string
	GOOS, GOARCH  string
	AcceptLicense bool
	Downloader    Downloader
}

// FetchTool installs one pinned executable idempotently:
//
//	<data_dir>/tools/<name>/<version>/<binary>
//
// The checksum of the downloaded artifact and of the extracted binary are
// verified before anything is renamed into place; a mismatch removes every
// partial file. The function never writes outside Root.
func FetchTool(ctx context.Context, lock *ToolsLock, o *FetchOptions) (ToolStatus, error) {
	_, a, st, done, err := fetchPlan(lock, o)
	if err != nil || done {
		return st, err
	}
	return installArtifact(ctx, o.Root, a, &st, o.Downloader)
}

// fetchPlan resolves the lock row and applies idempotency and license rules.
func fetchPlan(lock *ToolsLock, o *FetchOptions) (*ToolPackage, *ToolArtifact, ToolStatus, bool, error) {
	if o.Root == "" || o.GOOS == "" || o.GOARCH == "" {
		return nil, nil, ToolStatus{}, false, fmt.Errorf("%w: missing root/platform", ErrToolLock)
	}
	p, ok := lock.Lookup(o.Name)
	if !ok {
		return nil, nil, ToolStatus{}, false,
			fmt.Errorf("%w: %q (lock lists %s)", ErrToolUnknown, o.Name, strings.Join(lock.Names(), ", "))
	}
	a, ok := p.Artifact(o.GOOS, o.GOARCH)
	if !ok {
		detail := "no pinned artifact for " + o.GOOS + "/" + o.GOARCH
		if p.Note != "" {
			detail += ": " + p.Note
		}
		return nil, nil, ToolStatus{Name: p.Name, Version: p.Version, License: p.License, Detail: detail},
			false, fmt.Errorf("%w: %s: %s", ErrToolNoArtifact, p.Name, detail)
	}
	st := ToolStatus{
		Name: p.Name, Version: p.Version, License: p.License, Pinned: true,
		Platform: o.GOOS + "/" + o.GOARCH, Path: InstallPath(o.Root, p, &a), Detail: "not installed",
	}
	if sum, err := fileSHA256(st.Path); err == nil && sum == a.BinarySHA256 {
		st.Installed, st.OK, st.Detail = true, true, "already installed"
		return p, &a, st, true, nil
	}
	if !o.AcceptLicense {
		return p, &a, st, false, fmt.Errorf("%w: %s is %s (review %s, then pass --yes)", ErrToolLicense, p.Name, p.License, p.LicenseURL)
	}
	if o.Downloader == nil {
		return p, &a, st, false, fmt.Errorf("%w: no downloader", ErrToolDownload)
	}
	return p, &a, st, false, nil
}

func installArtifact(ctx context.Context, root string, a *ToolArtifact, st *ToolStatus, dl Downloader) (ToolStatus, error) {
	tmpDir := filepath.Join(root, "tools", ".tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return *st, fmt.Errorf("%w: %w", ErrToolDownload, err)
	}
	archive, cerr := os.CreateTemp(tmpDir, "artifact-*")
	if cerr != nil {
		return *st, fmt.Errorf("%w: %w", ErrToolDownload, cerr)
	}
	cleanupArchive := func() error { return closeAndRemove(archive) }
	if err := dl.Download(ctx, a, archive); err != nil {
		return *st, errors.Join(fmt.Errorf("doctor tools fetch: %w", err), cleanupArchive())
	}
	if err := archive.Sync(); err != nil {
		return *st, errors.Join(fmt.Errorf("%w: %w", ErrToolDownload, err), cleanupArchive())
	}
	if err := verifyFile(archive.Name(), a.Size, a.SHA256); err != nil {
		return *st, errors.Join(err, cleanupArchive())
	}
	bin, berr := os.CreateTemp(tmpDir, "binary-*")
	if berr != nil {
		return *st, errors.Join(fmt.Errorf("%w: %w", ErrToolExtract, berr), cleanupArchive())
	}
	cleanupBin := func() error { return closeAndRemove(bin) }
	if err := extractBinary(archive, a, bin); err != nil {
		return *st, errors.Join(err, cleanupBin(), cleanupArchive())
	}
	if err := placeBinary(bin, a.BinarySHA256, st.Path); err != nil {
		return *st, errors.Join(err, cleanupBin(), cleanupArchive())
	}
	if err := errors.Join(cleanupBin(), cleanupArchive()); err != nil {
		return *st, err
	}
	st.Installed, st.OK, st.Detail = true, true, "installed"
	return *st, nil
}

// closeAndRemove cleans a temp file; an already-closed/removed file is not
// an error.
func closeAndRemove(f *os.File) error {
	cerr := f.Close()
	if errors.Is(cerr, os.ErrClosed) {
		cerr = nil
	}
	rerr := os.Remove(f.Name())
	if errors.Is(rerr, os.ErrNotExist) {
		rerr = nil
	}
	return errors.Join(cerr, rerr)
}

// placeBinary syncs, verifies and atomically renames the extracted binary.
func placeBinary(bin *os.File, wantSHA, finalPath string) error {
	if err := bin.Chmod(0o700); err != nil {
		return fmt.Errorf("%w: %w", ErrToolExtract, err)
	}
	if err := bin.Sync(); err != nil {
		return fmt.Errorf("%w: %w", ErrToolExtract, err)
	}
	if err := bin.Close(); err != nil {
		return fmt.Errorf("%w: %w", ErrToolExtract, err)
	}
	if err := verifyFile(bin.Name(), 0, wantSHA); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return fmt.Errorf("%w: %w", ErrToolExtract, err)
	}
	// Windows cannot rename over an existing file: remove the modified copy
	// that failed verification before installing the verified one.
	if err := os.Remove(finalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %w", ErrToolExtract, err)
	}
	if err := os.Rename(bin.Name(), finalPath); err != nil {
		return fmt.Errorf("%w: %w", ErrToolExtract, err)
	}
	return nil
}

// verifyFile checks size (>0) and sha256. size 0 skips the size check.
func verifyFile(path string, size int64, want string) (err error) {
	f, err := os.DirFS(filepath.Dir(path)).Open(filepath.Base(path))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrToolChecksum, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxToolBinary+1))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrToolChecksum, err)
	}
	if n > MaxToolBinary {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrToolChecksum, filepath.Base(path), MaxToolBinary)
	}
	if size > 0 && n != size {
		return fmt.Errorf("%w: %d bytes, want %d", ErrToolChecksum, n, size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%w: sha256 %s", ErrToolChecksum, got)
	}
	return nil
}

// extractBinary writes the pinned member (or the raw artifact) into dst.
func extractBinary(src *os.File, a *ToolArtifact, dst *os.File) error {
	switch a.Archive {
	case "":
		if a.Binary != filepath.Base(a.Binary) || !utf8.ValidString(a.Binary) {
			return fmt.Errorf("%w: binary name", ErrToolExtract)
		}
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("%w: %w", ErrToolExtract, err)
		}
		return copyMember(dst, src)
	case "tar.gz":
		return extractTarGz(src, a.Binary, dst)
	case "zip":
		return extractZip(src, a.Binary, dst)
	}
	return fmt.Errorf("%w: archive %q", ErrToolExtract, a.Archive)
}

func copyMember(dst io.Writer, src io.Reader) error {
	if _, err := io.Copy(dst, io.LimitReader(src, MaxToolBinary+1)); err != nil {
		return fmt.Errorf("%w: %w", ErrToolExtract, err)
	}
	return nil
}

func extractTarGz(src *os.File, member string, dst *os.File) (err error) {
	if _, err = src.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("%w: %w", ErrToolExtract, err)
	}
	gz, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrToolExtract, err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: %s", ErrToolExtract, member)
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrToolExtract, err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.ToSlash(filepath.Clean(hdr.Name)) != member {
			continue
		}
		return copyMember(dst, tr)
	}
}

func extractZip(src *os.File, member string, dst *os.File) error {
	fi, err := src.Stat()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrToolExtract, err)
	}
	zr, err := zip.NewReader(src, fi.Size())
	if err != nil {
		return fmt.Errorf("%w: %w", ErrToolExtract, err)
	}
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() || filepath.ToSlash(filepath.Clean(zf.Name)) != member {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("%w: %w", ErrToolExtract, err)
		}
		if zf.UncompressedSize64 > MaxToolBinary {
			return errors.Join(fmt.Errorf("%w: %s exceeds %d bytes", ErrToolExtract, member, MaxToolBinary), rc.Close())
		}
		return errors.Join(copyMember(dst, rc), rc.Close())
	}
	return fmt.Errorf("%w: %s", ErrToolExtract, member)
}
