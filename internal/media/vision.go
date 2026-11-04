//go:build !minimal

package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/media/preflight"
	"github.com/damnbot/damnbot/internal/modules/host"
)

// analysisTokens bounds one analysis reply.
const analysisTokens = 1024

type analysisArgs struct {
	Path   string `json:"path"`
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

func (a *analysisArgs) validate() error {
	a.Path, a.URL = strings.TrimSpace(a.Path), strings.TrimSpace(a.URL)
	if (a.Path == "") == (a.URL == "") {
		return errors.New("exactly one of path or url is required")
	}
	if a.Path != "" && (len(a.Path) > 512 || strings.ContainsRune(a.Path, 0)) {
		return errors.New("invalid path")
	}
	if a.URL != "" {
		u, err := url.Parse(a.URL)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("url must be an absolute http(s) URL")
		}
	}
	return nil
}

// analyze is the shared analysis entry point. The capability gate runs before
// reading a file or fetching a URL: a text-only session never reaches the
// completer and never downloads the media (EC-PROV-024).
func (m *Module) analyze(ctx context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error) {
	if m == nil || m.deps.LLM == nil || !m.deps.Vision {
		return host.Result{}, &host.Error{
			Code: "module_unavailable", Message: "no vision-capable route for " + tool,
			Hint: "configure a model with the vision capability",
		}
	}
	var in analysisArgs
	if err := json.Unmarshal(args, &in); err != nil || in.validate() != nil {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: "invalid " + tool + " arguments"}
	}
	if in.Prompt == "" {
		in.Prompt = "Describe the image in detail."
	}
	cwd := env.Cwd
	if cwd == "" {
		cwd = env.Session.Cwd()
	}
	return m.analyzeImage(ctx, in, cwd)
}

func (m *Module) analyzeImage(ctx context.Context, in analysisArgs, cwd string) (host.Result, error) {
	var data []byte
	var err error
	var name string
	if in.Path != "" {
		data, err = readWorkspace(cwd, in.Path, preflight.MaxInputBytes)
		name = filepath.Base(in.Path)
	} else {
		data, err = m.fetchMedia(ctx, in.URL, preflight.MaxInputBytes)
		name = urlName(in.URL)
	}
	if err != nil {
		return host.Result{}, err
	}
	res, err := preflight.Preflight(data, preflight.Default())
	if err != nil {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: "image failed preflight", Hint: "use JPEG or PNG under the provider preset"}
	}
	return m.complete(ctx, host.Message{
		Role: "user", Content: in.Prompt,
		Images: []host.Image{{
			Name: name, MIME: res.MIME, Data: res.Data,
			Width: res.Width, Height: res.Height, Untrusted: true,
		}},
	}, map[string]any{
		"kind": "image", "mime": res.MIME, "bytes": len(data),
		"width": res.Width, "height": res.Height, "resized": res.Resized,
	})
}

// complete performs the one provider call and labels the text as untrusted.
// The provider error is deliberately not echoed: it can carry a URL, and the
// media bytes must never reach a log or tool error.
func (m *Module) complete(ctx context.Context, msg host.Message, meta map[string]any) (host.Result, error) {
	resp, err := m.deps.LLM.Complete(ctx, host.Request{
		Model: m.deps.VisionModel, MaxOutputTokens: analysisTokens,
		Messages: []host.Message{msg},
	})
	if err != nil {
		code := "network"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = "timeout"
		}
		return host.Result{}, &host.Error{Code: code, Message: "media analysis failed", Hint: "check the vision route and retry"}
	}
	if strings.TrimSpace(resp.Content) == "" {
		return host.Result{}, &host.Error{Code: "internal", Message: "vision model returned an empty analysis"}
	}
	meta["model"] = m.deps.VisionModel
	meta["input_tokens"] = resp.Usage.InputTokens
	meta["output_tokens"] = resp.Usage.OutputTokens
	return host.Result{Content: resp.Content, Meta: meta, UntrustedSource: "media:vision"}, nil
}

func (m *Module) fetchMedia(ctx context.Context, rawURL string, max int64) (data []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &host.Error{Code: "invalid_args", Message: "invalid media URL"}
	}
	resp, err := m.doer(rawURL).Do(req)
	if err != nil {
		return nil, &host.Error{Code: "network", Message: "media download failed"}
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("media: close response: %w", cerr)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &host.Error{Code: "network", Message: fmt.Sprintf("media download returned HTTP %d", resp.StatusCode)}
	}
	if resp.ContentLength > max {
		return nil, &host.Error{Code: "too_large", Message: fmt.Sprintf("media is %d bytes (max %d)", resp.ContentLength, max)}
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, &host.Error{Code: "network", Message: "media download failed"}
	}
	if int64(len(data)) > max {
		return nil, &host.Error{Code: "too_large", Message: fmt.Sprintf("media exceeds %d bytes", max)}
	}
	return data, nil
}

func readWorkspace(cwd, name string, max int64) (data []byte, err error) {
	if cwd == "" {
		return nil, &host.Error{Code: "invalid_args", Message: "workspace is not available"}
	}
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return nil, &host.Error{Code: "invalid_args", Message: "workspace is not accessible"}
	}
	defer func() {
		if cerr := root.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("media: close workspace: %w", cerr)
		}
	}()
	st, err := root.Stat(name)
	if err != nil || st.IsDir() {
		return nil, &host.Error{Code: "invalid_args", Message: "media file is not readable"}
	}
	if st.Size() > max {
		return nil, &host.Error{Code: "too_large", Message: fmt.Sprintf("media is %d bytes (max %d)", st.Size(), max)}
	}
	data, err = root.ReadFile(name)
	if err != nil {
		return nil, &host.Error{Code: "invalid_args", Message: "media file is not readable"}
	}
	return data, nil
}

func urlName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "media"
	}
	name := filepath.Base(u.Path)
	if name == "." || name == "/" || name == "" {
		return "media"
	}
	return name
}
