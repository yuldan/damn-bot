package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

func seedCronJob(t *testing.T, e *Env, args func(...string) []string) string {
	t.Helper()
	add := e.Exec(args("add", "--schedule", "*/5 * * * *", "--prompt", "say hi", "--name", "report")...)
	if add.Code != 0 {
		t.Fatalf("cron add: code %d\nstdout: %s\nstderr: %s", add.Code, add.Stdout, add.Stderr)
	}
	fields := strings.Fields(add.Stdout)
	if len(fields) != 3 || fields[0] != "created" || fields[2] != "report" {
		t.Fatalf("cron add output = %q", add.Stdout)
	}
	return fields[1]
}

// TestCronCLI covers the CLI-025 subset: add, list, pause, resume, run and rm
// through the real binary and the cronstore tables.
func TestCronCLI(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("job done"))
	cronArgs := func(sub ...string) []string {
		return append(append([]string{"cron"}, sub...), "--data-dir", e.DataDir, "--config", e.cfg)
	}

	id := seedCronJob(t, e, cronArgs)

	list := e.Exec(cronArgs("list")...)
	if list.Code != 0 || !strings.Contains(list.Stdout, id) || !strings.Contains(list.Stdout, "active") {
		t.Fatalf("cron list: code %d\n%s%s", list.Code, list.Stdout, list.Stderr)
	}
	if pause := e.Exec(cronArgs("pause", id)...); pause.Code != 0 {
		t.Fatalf("cron pause: %s%s", pause.Stdout, pause.Stderr)
	}
	list = e.Exec(cronArgs("list")...)
	if !strings.Contains(list.Stdout, "paused") {
		t.Fatalf("paused job still active:\n%s", list.Stdout)
	}
	if resume := e.Exec(cronArgs("resume", id)...); resume.Code != 0 {
		t.Fatalf("cron resume: %s%s", resume.Stdout, resume.Stderr)
	}

	run := e.Exec(cronArgs("run", id, "--model", modelRef, "--base-url", e.Server.URL, "--api-key", "e2e")...)
	if run.Code != 0 {
		t.Fatalf("cron run: code %d\nstdout: %s\nstderr: %s", run.Code, run.Stdout, run.Stderr)
	}
	if !strings.Contains(run.Stdout, "ran "+id) {
		t.Fatalf("cron run output = %q", run.Stdout)
	}

	if rm := e.Exec(cronArgs("rm", id)...); rm.Code != 0 {
		t.Fatalf("cron rm: %s%s", rm.Stdout, rm.Stderr)
	}
	list = e.Exec(cronArgs("list")...)
	if list.Code != 0 || strings.TrimSpace(list.Stdout) != "" {
		t.Fatalf("after rm list = %q (code %d, %s)", list.Stdout, list.Code, list.Stderr)
	}
}
