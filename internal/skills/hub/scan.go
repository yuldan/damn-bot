package hub

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/skills"
)

// ThreatScan is the hub's built-in grep scanner (SPEC §14.4). It is a
// [skills.Scanner] the composition root can hand to [skills.Service.Install];
// a nil scanner is not an option for an untrusted source. It reports a
// category and the matched pattern name, never the matched text, so no secret
// can leak into scan_result or a log.
//
// Severity: shell execution and secret exfiltration are high (the service
// rejects them immediately), directory traversal is medium (quarantined and
// shown at review), and a clean skill is low.
func ThreatScan(sk *skills.Skill) (severity string, result string, err error) {
	if sk == nil {
		return "", "", fmt.Errorf("%w: nil skill", ErrIndex)
	}
	text := strings.ToLower(sk.Description + "\n" + sk.Body)
	findings := make([]skills.Finding, 0, 4)
	for _, rule := range threatRules() {
		if strings.Contains(text, rule.pattern) {
			findings = append(findings, skills.Finding{Category: rule.category, Severity: rule.severity, Detail: rule.detail})
		}
	}
	sev := "low"
	rank := map[string]int{"low": 0, "medium": 1, "high": 2}
	for _, f := range findings {
		if rank[f.Severity] > rank[sev] {
			sev = f.Severity
		}
	}
	b, err := json.Marshal(struct {
		Severity string           `json:"severity"`
		Findings []skills.Finding `json:"findings,omitempty"`
	}{Severity: sev, Findings: findings})
	if err != nil {
		return "", "", fmt.Errorf("skills hub: scan result: %w", err)
	}
	return sev, string(b), nil
}

type threatRule struct {
	pattern  string
	category string
	severity string
	detail   string
}

// threatRules is the reviewed pattern set. Details name the family, not the
// matched substring.
func threatRules() []threatRule {
	return []threatRule{
		{"os/exec", "shell_exec", "high", "spawns a process"},
		{"exec.command", "shell_exec", "high", "spawns a process"},
		{"subprocess.", "shell_exec", "high", "spawns a process"},
		{"subprocess(", "shell_exec", "high", "spawns a process"},
		{"child_process", "shell_exec", "high", "spawns a process"},
		{"bash -c", "shell_exec", "high", "shell -c"},
		{"sh -c", "shell_exec", "high", "shell -c"},
		{"system(", "shell_exec", "high", "system call"},
		{"../", "path_traversal", "medium", "parent directory reference"},
		{"..\\", "path_traversal", "medium", "parent directory reference"},
		{".ssh/", "secret_exfil", "high", "reads SSH material"},
		{".aws/credentials", "secret_exfil", "high", "reads cloud credentials"},
		{".config/gcloud", "secret_exfil", "high", "reads cloud credentials"},
		{"security find-generic-password", "secret_exfil", "high", "reads the keychain"},
		{"secrets.json", "secret_exfil", "high", "reads a secret file"},
		// GAP-GW-07: loading the whole dotenv file is a medium env_file
		// finding (quarantined, never auto-rejected).
		{"load_dotenv(", "env_file", "medium", "loads a dotenv file"},
		{"dotenv.config(", "env_file", "medium", "loads a dotenv file"},
		{"cat .env", "env_file", "medium", "reads a dotenv file"},
		{"source .env", "env_file", "medium", "reads a dotenv file"},
		{"grep .env", "env_file", "medium", "reads a dotenv file"},
	}
}
