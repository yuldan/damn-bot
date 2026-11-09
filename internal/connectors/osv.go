package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
)

// osvMaxVulns and osvMaxResult bound one OSV lookup result.
const (
	osvMaxVulns  = 20
	osvMaxResult = 128 << 10
)

// osvHandler queries OSV.dev for one package version. The endpoint needs no
// credential; the query is a read-only POST and therefore retryable.
type osvHandler struct {
	doer *Doer
	base string
}

// OSVSection is [modules.connectors.osv].
type OSVSection struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
}

func (m *Module) newOSV() (Handler, error) {
	cfg, err := decodeSub[OSVSection](m.cfg.OSV, "osv")
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	doer, err := m.newDoer(m.network.Client("osv", netx.Tool))
	if err != nil {
		return nil, err
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://api.osv.dev"
	}
	return &osvHandler{doer: doer, base: strings.TrimRight(base, "/")}, nil
}

func (o *osvHandler) Tools() []string { return []string{"osv_check"} }

func (o *osvHandler) Invoke(ctx context.Context, tool string, raw json.RawMessage, _ host.Env) (host.Result, error) {
	if tool != "osv_check" {
		return host.Result{}, &Error{Code: codeModuleUnavailable, Message: "unknown osv tool " + tool}
	}
	var a struct {
		Package   string `json:"package"`
		Version   string `json:"version"`
		Ecosystem string `json:"ecosystem"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	if strings.TrimSpace(a.Package) == "" {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "package must not be empty"}
	}
	pkg := map[string]string{"name": a.Package}
	if a.Ecosystem != "" {
		pkg["ecosystem"] = a.Ecosystem
	}
	query := map[string]any{"package": pkg}
	if a.Version != "" {
		query["version"] = a.Version
	}
	body, err := json.Marshal(query)
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode osv query", err: err}
	}
	var out struct {
		Vulns []struct {
			ID      string `json:"id"`
			Summary string `json:"summary"`
		} `json:"vulns"`
	}
	if _, perr := o.doer.JSON(ctx, &Request{
		Method: http.MethodPost, URL: joinURL(o.base, "/v1/query"),
		Header: headerJSON(), Body: body, Accept: "application/json", Retry: true,
	}, &out); perr != nil {
		return host.Result{}, perr
	}
	vulns, truncated := dedupeVulns(out.Vulns)
	rendered, err := json.Marshal(struct {
		Vulns     []osvVuln `json:"vulns"`
		Count     int       `json:"count"`
		Truncated bool      `json:"truncated"`
	}{vulns, len(vulns), truncated})
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode osv result", err: err}
	}
	return host.Result{Content: string(rendered), UntrustedSource: "connector:osv"}, nil
}

type osvVuln struct {
	ID      string `json:"id"`
	Summary string `json:"summary,omitempty"`
}

// dedupeVulns removes repeated advisory ids, caps the list at 20 entries and
// stops growing the rendered payload at 128 KiB.
func dedupeVulns(in []struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
},
) ([]osvVuln, bool) {
	seen := map[string]bool{}
	out := make([]osvVuln, 0, min(len(in), osvMaxVulns))
	size := 0
	for _, v := range in {
		if v.ID == "" || seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		out = append(out, osvVuln{ID: v.ID, Summary: v.Summary})
		size += len(v.ID) + len(v.Summary)
		if len(out) >= osvMaxVulns || size >= osvMaxResult {
			return out, true
		}
	}
	return out, false
}
