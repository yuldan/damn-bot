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

	"github.com/damnbot/damnbot/internal/media/cache"
	"github.com/damnbot/damnbot/internal/media/preflight"
	"github.com/damnbot/damnbot/internal/media/video"
	"github.com/damnbot/damnbot/internal/modules/host"
)

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

// analyze gates on vision, then reads/fetches the media (EC-PROV-024).
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
		kind := "image"
		if tool == ToolVideoAnalyze {
			kind = "video"
		}
		in.Prompt = "Describe the " + kind + " in detail."
	}
	cwd := env.Cwd
	if cwd == "" {
		cwd = env.Session.Cwd()
	}
	if tool == ToolVisionAnalyze {
		return m.analyzeImage(ctx, in, cwd)
	}
	return m.analyzeVideo(ctx, in, cwd, env, args)
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
	meta := map[string]any{
		"kind": "image", "mime": res.MIME, "bytes": len(data),
		"width": res.Width, "height": res.Height, "resized": res.Resized,
	}
	if entry := m.cacheInbound(ctx, "image", res.MIME, data); entry != nil {
		meta["cache_sha"] = entry.SHA
	}
	return m.complete(ctx, ToolVisionAnalyze, in.Prompt, &host.Message{
		Role: "user", Content: in.Prompt,
		Images: []host.Image{{
			Name: name, MIME: res.MIME, Data: res.Data,
			Width: res.Width, Height: res.Height, Untrusted: true,
		}},
	}, meta)
}

func (m *Module) analyzeVideo(ctx context.Context, in analysisArgs, cwd string, env host.Env, args json.RawMessage) (host.Result, error) {
	var data []byte
	var err error
	var name string
	if in.Path != "" {
		data, err = m.readVideoFile(ctx, in.Path, cwd, env, args)
		name = filepath.Base(in.Path)
	} else {
		data, err = m.fetchVideo(ctx, in.URL)
		name = urlName(in.URL)
	}
	if err != nil {
		return host.Result{}, err
	}
	info, err := video.Probe(data)
	if err != nil {
		return host.Result{}, videoError(err)
	}
	if err := video.Check(info); err != nil {
		return host.Result{}, videoError(err)
	}
	meta := map[string]any{
		"kind": "video", "mime": info.MIME, "bytes": len(data), "duration_s": info.DurationS,
	}
	if entry := m.cacheInbound(ctx, "video", info.MIME, data); entry != nil {
		meta["cache_sha"] = entry.SHA
	}
	return m.complete(ctx, ToolVideoAnalyze, in.Prompt, &host.Message{
		Role: "user", Content: in.Prompt,
		Videos: []host.Video{{
			Name: name, MIME: info.MIME, Data: data, DurationS: info.DurationS, Untrusted: true,
		}},
	}, meta)
}

// complete performs the one provider call and labels the text untrusted; it
// never echoes the provider error (which can carry a body fragment).
func (m *Module) complete(ctx context.Context, tool, _ string, msg *host.Message, meta map[string]any) (host.Result, error) {
	resp, err := m.deps.LLM.Complete(ctx, host.Request{
		Model: m.deps.VisionModel, MaxOutputTokens: analysisTokens,
		Messages: []host.Message{*msg},
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
	return host.Result{Content: resp.Content, Meta: meta, UntrustedSource: "media:" + strings.TrimSuffix(tool, "_analyze")}, nil
}

// readVideoFile bounds the read and approves a path outside the workspace.
func (m *Module) readVideoFile(ctx context.Context, path, cwd string, env host.Env, args json.RawMessage) ([]byte, error) {
	abs, inside := resolvePath(cwd, path)
	if !inside {
		st, err := os.Stat(abs)
		if err != nil || st.IsDir() {
			return nil, &host.Error{Code: "invalid_args", Message: "video is not a readable file"}
		}
		if st.Size() > video.MaxBytes {
			return nil, &host.Error{Code: "too_large", Message: fmt.Sprintf("video is %d bytes (max %d)", st.Size(), video.MaxBytes)}
		}
		preview := fmt.Sprintf("video_analyze %s (%d bytes)", filepath.Base(abs), st.Size())
		if err := m.approve(ctx, ToolVideoAnalyze, "media:video", preview, env, args); err != nil {
			return nil, err
		}
		return readFileCapped(abs, video.MaxBytes)
	}
	return readWorkspace(cwd, path, video.MaxBytes)
}

// fetchVideo checks Content-Length before the body, then caps the stream.
func (m *Module) fetchVideo(ctx context.Context, rawURL string) ([]byte, error) {
	if size, ok := m.headSize(ctx, rawURL); ok && size > video.MaxBytes {
		return nil, &host.Error{Code: "too_large", Message: fmt.Sprintf("video is %d bytes (max %d)", size, video.MaxBytes)}
	}
	data, err := m.fetchMedia(ctx, rawURL, video.MaxBytes)
	if err != nil {
		var he *host.Error
		if errors.As(err, &he) && he.Code == "too_large" {
			he.Message = fmt.Sprintf("video exceeds %d bytes", video.MaxBytes)
		}
		return nil, err
	}
	return data, nil
}

func (m *Module) headSize(ctx context.Context, rawURL string) (int64, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return 0, false
	}
	resp, err := m.doer(rawURL).Do(req)
	if err != nil {
		return 0, false
	}
	if cerr := resp.Body.Close(); cerr != nil {
		return 0, false
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || resp.ContentLength <= 0 {
		return 0, false
	}
	return resp.ContentLength, true
}

// cacheInbound records the inbound bytes in the media cache. A cache failure
// never fails the analysis; the entry's SHA is exposed for observability.
func (m *Module) cacheInbound(ctx context.Context, kind, mime string, data []byte) *cache.Entry {
	if m.cache == nil {
		return nil
	}
	entry, err := m.cache.Put(ctx, cache.PutRequest{
		Kind: kind, MIME: mime, Data: data, Params: map[string]string{"usage": "inbound"},
	})
	if err != nil {
		return nil
	}
	return &entry
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

func readFileCapped(name string, max int64) (data []byte, err error) {
	dir, base := filepath.Split(name)
	if base == "" || base == "." {
		return nil, &host.Error{Code: "invalid_args", Message: "media file is not readable"}
	}
	root, err := os.OpenRoot(filepath.Clean(dir))
	if err != nil {
		return nil, &host.Error{Code: "invalid_args", Message: "media file is not readable"}
	}
	defer func() {
		if cerr := root.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("media: close dir: %w", cerr)
		}
	}()
	st, err := root.Stat(base)
	if err != nil || st.IsDir() {
		return nil, &host.Error{Code: "invalid_args", Message: "media file is not readable"}
	}
	if st.Size() > max {
		return nil, &host.Error{Code: "too_large", Message: fmt.Sprintf("media is %d bytes (max %d)", st.Size(), max)}
	}
	data, err = root.ReadFile(base)
	if err != nil {
		return nil, &host.Error{Code: "invalid_args", Message: "media file is not readable"}
	}
	return data, nil
}

func resolvePath(cwd, name string) (string, bool) {
	if cwd == "" {
		return name, false
	}
	abs := name
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(cwd, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return abs, false
	}
	return abs, true
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

func videoError(err error) error {
	switch {
	case errors.Is(err, video.ErrTooLarge), errors.Is(err, video.ErrDuration):
		return &host.Error{Code: "too_large", Message: err.Error(), Hint: fmt.Sprintf("video must be at most %d bytes and %.0f seconds", video.MaxBytes, video.MaxDuration)}
	case errors.Is(err, video.ErrUnsupported):
		return &host.Error{Code: "invalid_args", Message: err.Error(), Hint: "supported containers: MP4/MOV"}
	default:
		return &host.Error{Code: "invalid_args", Message: "video header is not readable"}
	}
}
