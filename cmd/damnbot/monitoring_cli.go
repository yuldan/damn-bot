//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/obs"
	"github.com/damnbot/damnbot/internal/redact"
)

// defaultHealthURL is the SPEC §15 observability listener.
const defaultHealthURL = "http://" + obs.DefaultListen + "/healthz"

type monitoringOptions struct {
	json      bool
	healthURL string
}

// runMonitoring implements SPEC §10 `damnbot monitoring status` (CLI-060):
// gateway health plus a redacted diagnostic summary. It never exports a
// metric and never dials the OTLP endpoint; a disabled exporter keeps its
// zero-connection contract.
func runMonitoring(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "status" {
		monitoringUsage(stderr)
		return 2
	}
	var o monitoringOptions
	fs := flag.NewFlagSet("monitoring status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.json, "json", false, "print the status as JSON")
	fs.StringVar(&o.healthURL, "health-url", defaultHealthURL, "gateway /healthz URL (loopback)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		sessionsPrintf(stderr, "monitoring status: unexpected argument %q\n", cleanArg(fs.Arg(0)))
		return 2
	}
	cfg, err := openConfig()
	if err != nil {
		sessionsPrintf(stderr, "monitoring status: %s\n", cleanLine(err.Error()))
		return 1
	}
	return monitoringStatus(context.Background(), &cfg.cfg, cfg.file, o, stdout, stderr)
}

// monitoringStatus is the seam tests use without touching the host config.
func monitoringStatus(ctx context.Context, cfg *config.Config, configPath string, o monitoringOptions, stdout, stderr io.Writer) int {
	red := redact.New(redact.Options{})
	out := monitoringReport{
		Version: version,
		Runtime: runtime.GOOS + "/" + runtime.GOARCH,
		Config:  string(red.Redact([]byte(configPath))),
		OTLP:    otlpStatus{Enabled: cfg.Telemetry.OTLPEnabled, Endpoint: cfg.Telemetry.OTLPEndpoint},
	}
	if out.OTLP.Enabled {
		if err := obs.ValidateOTLPEndpoint(out.OTLP.Endpoint); err != nil {
			sessionsPrintf(stderr, "monitoring status: %s\n", cleanLine(err.Error()))
			return 2
		}
	} else {
		out.OTLP.Endpoint = ""
		out.OTLP.State = "disabled"
	}
	out.Health = probeHealth(ctx, string(red.Redact([]byte(o.healthURL))))
	if out.OTLP.Enabled {
		out.OTLP.State = "ready"
	}
	if o.json {
		raw, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			sessionsPrintf(stderr, "monitoring status: %s\n", cleanLine(err.Error()))
			return 1
		}
		sessionsPrintf(stdout, "%s\n", raw)
		return 0
	}
	sessionsPrintf(stdout, "version: %s (%s)\n", out.Version, out.Runtime)
	sessionsPrintf(stdout, "otlp: %s", out.OTLP.State)
	if out.OTLP.Endpoint != "" {
		sessionsPrintf(stdout, " %s", out.OTLP.Endpoint)
	}
	sessionsPrintf(stdout, "\nhealth: %s", out.Health.State)
	if out.Health.Code != 0 {
		sessionsPrintf(stdout, " (%d)", out.Health.Code)
	}
	sessionsPrintf(stdout, "\n")
	return 0
}

type monitoringReport struct {
	Version string       `json:"version"`
	Runtime string       `json:"runtime"`
	Config  string       `json:"config"`
	OTLP    otlpStatus   `json:"otlp"`
	Health  healthStatus `json:"health"`
}

type otlpStatus struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint,omitempty"`
	State    string `json:"state"`
}

type healthStatus struct {
	URL   string `json:"url"`
	State string `json:"state"`
	Code  int    `json:"code,omitempty"`
}

// probeHealth checks the local observability listener with the loopback
// client; a down gateway is reported, never fatal.
func probeHealth(ctx context.Context, raw string) healthStatus {
	out := healthStatus{URL: raw, State: "unreachable"}
	u, err := url.Parse(raw)
	if err != nil || (u.Hostname() != "localhost" && u.Hostname() != "::1" && !strings.HasPrefix(u.Hostname(), "127.")) {
		out.State = "invalid"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return out
	}
	resp, err := netx.LoopbackClient().Do(req)
	if err != nil {
		return out
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return out
	}
	if err := resp.Body.Close(); err != nil {
		return out
	}
	out.Code = resp.StatusCode
	if resp.StatusCode == http.StatusOK {
		out.State = "ok"
	} else {
		out.State = "unhealthy"
	}
	return out
}

func monitoringUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot monitoring status [--json] [--health-url URL]\n")
}
