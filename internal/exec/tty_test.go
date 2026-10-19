package exec

import (
	"strings"
	"testing"
)

func TestNeedsTTY(t *testing.T) {
	t.Parallel()
	for cmd, want := range map[string]bool{
		"vim a.go": true, "less log": true, "top": true, "/usr/bin/htop": true, "man ls": true,
		"ssh host": true, "ssh host uptime": false, "ssh -p 22 host": false,
		"python3": true, "python3 x.py": false, "python -c print(1)": false, "node": true, "node -e 1": false,
		"irb": true, "docker run -it alpine sh": true, "docker run alpine true": false, "docker exec -t c sh": true,
		"tmux": true, "git status": false, "go test ./...": false, "cat file": false,
	} {
		if got := NeedsTTY([][]string{strings.Fields(cmd)}); got != want {
			t.Errorf("%q: %v", cmd, got)
		}
	}
	if !NeedsTTY([][]string{{"echo", "x"}, {"less"}}) || NeedsTTY(nil) || NeedsTTY([][]string{{}}) {
		t.Error("pipeline handling")
	}
}
