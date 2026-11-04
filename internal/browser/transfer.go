package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// maxWorkspaceBlob bounds upload and download payloads (ADR-321).
const maxWorkspaceBlob = 8 << 20

// workspaceRoot opens the session workspace; every upload/download path is
// resolved through it, so an absolute or escaping path cannot leave the tree.
func workspaceRoot(env host.Env) (*os.Root, error) {
	cwd := env.Cwd
	if cwd == "" {
		cwd = env.Session.Cwd()
	}
	if cwd == "" {
		return nil, &host.Error{Code: "invalid_args", Message: "workspace is not accessible"}
	}
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return nil, &host.Error{Code: "invalid_args", Message: "workspace is not accessible"}
	}
	return root, nil
}

func readWorkspaceFile(env host.Env, path string) (out FileRef, err error) {
	root, err := workspaceRoot(env)
	if err != nil {
		return FileRef{}, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if path == "" || filepath.IsAbs(path) {
		return FileRef{}, &host.Error{Code: "invalid_args", Message: "upload path must be relative"}
	}
	data, rerr := root.ReadFile(path)
	if rerr != nil {
		return FileRef{}, &host.Error{Code: "invalid_args", Message: "upload path is not readable"}
	}
	if len(data) > maxWorkspaceBlob {
		return FileRef{}, &host.Error{Code: "too_large", Message: "upload exceeds 8 MiB"}
	}
	return FileRef{Name: filepath.Base(path), Data: data}, nil
}

func writeWorkspaceFile(env host.Env, path string, blob []byte) (err error) {
	if len(blob) > maxWorkspaceBlob {
		return &host.Error{Code: "too_large", Message: "download exceeds 8 MiB"}
	}
	if path == "" || filepath.IsAbs(path) {
		return &host.Error{Code: "invalid_args", Message: "download path must be relative"}
	}
	root, err := workspaceRoot(env)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if werr := root.WriteFile(path, blob, 0o600); werr != nil {
		return &host.Error{Code: "invalid_args", Message: "download path is not writable"}
	}
	return nil
}

func (m *Module) upload(ctx context.Context, raw json.RawMessage, env host.Env) (host.Result, error) {
	var a struct {
		Selector, Path string
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	file, err := readWorkspaceFile(env, a.Path)
	if err != nil {
		return host.Result{}, err
	}
	if aerr := m.approve(ctx, "browser_upload", "browser:upload", fmt.Sprintf("browser_upload %s (%d bytes)", a.Path, len(file.Data)), env, raw); aerr != nil {
		return host.Result{}, aerr
	}
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		if err := p.Upload(ctx, a.Selector, file, true); err != nil {
			return host.Result{}, err
		}
		return host.Result{Content: fmt.Sprintf("uploaded %s (%d bytes)", a.Path, len(file.Data))}, nil
	})
}

func (m *Module) download(ctx context.Context, raw json.RawMessage, env host.Env) (host.Result, error) {
	var a struct {
		URL, Path string
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	if err := m.approve(ctx, "browser_download", "browser:download", "browser_download "+a.Path, env, raw); err != nil {
		return host.Result{}, err
	}
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		u, err := p.checkURL(a.URL)
		if err != nil {
			return host.Result{}, err
		}
		reply, err := p.read(ctx, ReadOp{Kind: "download", URL: u.String(), Limit: p.cfg.MaxBlob, Guard: p.guard})
		if err != nil {
			return host.Result{}, pageError(err)
		}
		blob := reply.Blob
		if len(blob) > p.cfg.MaxBlob {
			return host.Result{}, fmt.Errorf("%w: download %d > %d bytes", errTooLarge, len(blob), p.cfg.MaxBlob)
		}
		if werr := writeWorkspaceFile(env, a.Path, blob); werr != nil {
			return host.Result{}, werr
		}
		return host.Result{
			Content: fmt.Sprintf("downloaded %d bytes to %s", len(blob), a.Path),
			Meta:    map[string]any{"bytes": len(blob), "name": reply.Name},
		}, nil
	})
}
