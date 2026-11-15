package archtest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestTestShardPartition pins the CI shard split (ADR-324 amendment
// 2026-11-15): every shard of a fixed inventory must be non-empty, the union
// must be exactly the inventory with no duplicate, and the same shard must be
// stable across invocations. A regression here would silently drop tests from
// the `test` matrix while ci-required still reports green.
func TestTestShardPartition(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shard script is exercised on Linux/macOS runners")
	}
	dir := t.TempDir()
	inventory := strings.Join([]string{
		"pkg/a\tTestA1",
		"pkg/a\tTestA2",
		"pkg/b\tTestB1",
		"pkg/c\tTestC1",
		"pkg/d\tTestD1",
		"pkg/d\tTestD2",
	}, "\n") + "\n"
	weights := "pkg/a\t100\npkg/b\t50\npkg/c\t25\npkg/d\t25\n"
	invPath := filepath.Join(dir, "inventory")
	weightPath := filepath.Join(dir, "weights")
	if err := os.WriteFile(invPath, []byte(inventory), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(weightPath, []byte(weights), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(shard string) string {
		t.Helper()
		cmd := exec.Command("bash", "scripts/test-shard.sh", "test", shard) // #nosec G204 -- fixed script and generated fixture
		cmd.Dir = filepath.Join("..", "..")
		cmd.Env = append(os.Environ(),
			"SHARD_INVENTORY="+invPath,
			"SHARD_WEIGHTS="+weightPath,
			"SHARD_PLAN_ONLY=1",
		)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("shard %s: %v\n%s", shard, err, stderr.String())
		}
		return stderr.String()
	}

	var got []string
	for i := 1; i <= 3; i++ {
		out := run(fmt.Sprintf("%d/3", i))
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) == 0 || lines[0] == "" {
			t.Fatalf("shard %d empty:\n%s", i, out)
		}
		got = append(got, lines...)
	}
	want := strings.Split(strings.TrimSpace(inventory), "\n")
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("shard union mismatch\n got: %v\nwant: %v", got, want)
	}

	if first, second := run("1/3"), run("1/3"); first != second {
		t.Fatalf("shard 1/3 is not deterministic:\n--- first\n%s\n--- second\n%s", first, second)
	}
}
