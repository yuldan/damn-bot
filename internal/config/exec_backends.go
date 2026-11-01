//go:build !minimal

package config

import "strings"

// ExecBackends is the per-backend configuration of [exec.backends]
// (EXEC-AND-CODING §A.7, ADR-277). Selecting docker without its required
// fields is a validation error, not a silent fallback to local.
type ExecBackends struct {
	Docker DockerExec `toml:"docker"`
}

// DockerExec configures the docker CLI backend.
type DockerExec struct {
	Binary         string   `toml:"binary"`
	Image          string   `toml:"image"`
	User           string   `toml:"user"`
	CPUMilli       int      `toml:"cpu_milli"`
	MemoryMiB      int      `toml:"memory_mib"`
	PIDs           int      `toml:"pids"`
	Network        string   `toml:"network"`      // none (default) | bridge | allowlist
	NetworkName    string   `toml:"network_name"` // isolated docker network used by allowlist
	EgressAllow    []string `toml:"egress_allow"`
	ProxyURL       string   `toml:"proxy_url"`
	AllowUnlimited bool     `toml:"allow_unlimited"`
}

// defaultExecBackends is the SPEC §12 [exec.backends] block (ADR-277).
func defaultExecBackends() ExecBackends {
	return ExecBackends{
		Docker: DockerExec{
			Binary: "docker", User: "65534:65534", CPUMilli: 1000,
			MemoryMiB: 512, PIDs: 128, Network: "none",
		},
	}
}

// validateExecBackends rejects a selected backend with missing or unsafe
// configuration; a backend that cannot run is never replaced by local.
func (c *Config) validateExecBackends() error {
	switch c.Exec.Backend {
	case "", "local":
		return nil
	case "docker":
		return c.validateDocker()
	default:
		return invalid("exec.backend", "must be local or docker in this slice")
	}
}

//nolint:gocyclo // one branch per validated key keeps the config error names readable
func (c *Config) validateDocker() error {
	d := c.Exec.Backends.Docker
	switch {
	case d.Image == "":
		return invalid("exec.backends.docker.image", "required for the docker backend")
	case d.Binary == "" || strings.ContainsAny(d.Binary, " \t\x00"):
		return invalid("exec.backends.docker.binary", "must be a program name")
	case d.User == "" || strings.ContainsAny(d.User, " \t\x00"):
		return invalid("exec.backends.docker.user", "must be uid[:gid] (non-root)")
	case d.CPUMilli < 1 || d.CPUMilli > 64000:
		return invalid("exec.backends.docker.cpu_milli", "must be in 1..64000")
	case d.MemoryMiB < 32 || d.MemoryMiB > 65536:
		return invalid("exec.backends.docker.memory_mib", "must be in 32..65536")
	case d.PIDs < 1 || d.PIDs > 65535:
		return invalid("exec.backends.docker.pids", "must be in 1..65535")
	}
	switch d.Network {
	case "none":
		if len(d.EgressAllow) > 0 || d.ProxyURL != "" {
			return invalid("exec.backends.docker.network", "allowlist/proxy configured while network = none")
		}
	case "bridge":
	case "allowlist":
		if len(d.EgressAllow) == 0 {
			return invalid("exec.backends.docker.egress_allow", "allowlist requires at least one host")
		}
		if d.ProxyURL == "" {
			return invalid("exec.backends.docker.proxy_url", "allowlist egress requires the filtering proxy")
		}
		if d.NetworkName == "" || strings.ContainsAny(d.NetworkName, " \t\x00") {
			return invalid("exec.backends.docker.network_name", "allowlist requires the isolated docker network name")
		}
	default:
		return invalid("exec.backends.docker.network", "must be none, bridge or allowlist")
	}
	for _, host := range d.EgressAllow {
		if !validEgressHost(host) {
			return invalid("exec.backends.docker.egress_allow", "%q is not a host[:port]", host)
		}
	}
	return nil
}

// validEgressHost rejects scheme/prefix/paths: the list is host[:port].
func validEgressHost(host string) bool {
	if host == "" || strings.ContainsAny(host, " \t/\x00") {
		return false
	}
	return !strings.Contains(host, "://") && !strings.HasPrefix(host, "*")
}
