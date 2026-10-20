package classify

import (
	"strings"
	"testing"
)

// TestRuleLabels covers the label tables, option rules, wrappers and
// interpreter recursion (EXEC-AND-CODING §A.8, GAP-SEC-12/15).
func TestRuleLabels(t *testing.T) {
	t.Parallel()
	checkLabelCases(t, []labelCase{
		{"git status", Mutating, 0},
		{"git checkout feature", Mutating, 0},
		{"git add . && git commit -m 'msg'", Mutating, 0},
		{"sed -i s/a/b/ main.go", Mutating, 0},
		{"bash ./scripts/build.sh", Mutating, 0},
		{"python3 tools/gen.py", Mutating, 0},
		{"bash -c 'go build ./...'", Mutating, 0},
		{"timeout 10 go test ./...", Mutating, 0},
		{"env FOO=1 make", Mutating, 0},
		{"find . -name '*.go' -exec gofmt -l {} +", Mutating, 0},
		{"eval 'ls -la'", Mutating, 0},
		{"claude -p 'review this'", Mutating, 0},
		{"npm install", Mutating, Network},
		{"go mod download", Mutating, Network},
		{"git clone https://github.com/a/b", Mutating, Network},
		{"npx prettier --check .", Mutating, Network},
		{"apt search jq", Mutating, Network},
		{"sort --files0-from=- <<< ~/.ssh/id_rsa", Unknown, 0},
		{"bash", Unknown, 0},
		{"python -c 'print(1)'", Unknown, 0},
		{"cat a | python3", Unknown, 0},
		{"bash -c \"$CMD\"", Unknown, 0},
		{"eval \"$X\"", Unknown, 0},
		{"source .venv/bin/activate", Unknown, 0},
		{"xargs rm < list.txt", Unknown, 0},
		{"git -c core.pager=less log", Unknown, 0},
		{"sudo -i", Unknown, Privileged},
		{"env -C /tmp ls", Unknown, 0},
		{"git reset --hard HEAD~1", Destructive, 0},
		{"git clean -fdx", Destructive, 0},
		{"git checkout -- .", Destructive, 0},
		{"git branch -D old", Destructive, 0},
		{"git stash drop", Destructive, 0},
		{"git config --global user.name x", Destructive, 0},
		{"dd if=/dev/zero of=/dev/sda", Destructive, 0},
		{"mkfs.ext4 /dev/sdb1", Destructive, OutsideRead},
		{"kill -9 -1", Destructive, 0},
		{"crontab -r", Destructive, 0},
	})
}

// TestRuleLabelsMore covers the remaining tables, flags and launchers.
func TestRuleLabelsMore(t *testing.T) {
	t.Parallel()
	checkLabelCases(t, []labelCase{
		{"docker system prune -a", Destructive, 0},
		{"kubectl delete pod x", Destructive, Network},
		{"find /tmp -delete", Destructive, 0},
		{"find . -exec rm -rf / \\;", Destructive, 0},
		{"bash -c 'rm -rf ~'", Destructive, 0},
		{"nohup rm -rf / &", Destructive, 0},
		{"git push --force origin main", Irreversible, 0},
		{"git push origin +main", Irreversible, 0},
		{"npm publish", Irreversible, 0},
		{"npm i -g typescript", Irreversible, Network},
		{"gh pr create --fill", Irreversible, Network},
		{"sudo apt install jq", Irreversible, Privileged | Network},
		{"pip install requests", Irreversible, Network},
		{"stripe charges create --amount 100", Monetary, 0},
		{"gh sponsor x", Monetary, Network},
		{"claude --dangerously-skip-permissions -p x", DelegatedAgentBypass, 0},
		{"gemini --approval-mode yolo", DelegatedAgentBypass, 0},
		{"npx -y claude --yolo", DelegatedAgentBypass, Network},
		{"git add ~/.ssh/id_rsa", Mutating, DeniedPath},
		{"sudo ls /root", Mutating, Privileged | OutsideRead},
		// CL5-2: standard launchers recurse into the wrapped command.
		{"setsid sh -c 'rm -rf ~'", Destructive, 0},
		{"busybox rm -rf ~", Destructive, 0},
		{"taskset -c 0-3 sh -c 'rm -rf ~'", Destructive, 0},
		{"taskset 0x3 rm -rf ~", Destructive, 0},
		{"unshare -m sh -c 'rm -rf ~'", Destructive, 0},
		{"systemd-run rm -rf ~", Destructive, 0},
		{"systemd-run -p X rm -rf ~", Unknown, 0},
		{"setsid --unknown rm -rf /", Unknown, 0},
		{"unshare --unknown rm -rf /", Unknown, 0},
		{"taskset -p 1234", Unknown, 0},
		// CL5-10: launcher values and shell applets.
		{"unshare --propagation private sh -c 'rm -rf ~'", Destructive, 0},
		{"busybox ash -c 'rm -rf ~'", Destructive, 0},
		{"busybox hush -c 'rm -rf ~'", Destructive, 0},
		{"toybox sh -c 'rm -rf ~'", Destructive, 0},
		{"toybox rm -rf ~", Destructive, 0},
		{"busybox --unknown rm -rf /", Unknown, 0},
	})
}

func checkLabelCases(t *testing.T, cases []labelCase) {
	t.Helper()
	for _, c := range cases {
		v := Classify(c.cmd, testEnv())
		if v.Class != c.class || v.Flags != c.flags {
			t.Errorf("Classify(%q) = %s %v, want %s %v; %q", c.cmd, v.Class, v.Flags.Names(), c.class, c.flags.Names(), v.Reasons)
		}
	}
}

func TestRuleNonMutating(t *testing.T) {
	t.Parallel()
	for cmd, want := range map[string]bool{
		"ls -la": true, "git status": true, "git diff HEAD~1": true, "cat a.go | wc -l": true, "go list ./...": true,
		"find . -name x": true, "git diff --output=x": false, "ls > out": false, "find . -delete": false,
		"go test ./...": false, "cat /etc/passwd": false, "echo $(touch x)": false, "x=1": false,
	} {
		if got := Classify(cmd, testEnv()).NonMutating; got != want {
			t.Errorf("NonMutating(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestRuleNesting(t *testing.T) {
	t.Parallel()
	cmd := "ls"
	for range maxDepth + 1 {
		cmd = "eval '" + strings.ReplaceAll(cmd, "'", `'\''`) + "'"
	}
	if v := Classify(cmd, testEnv()); v.Class != Unknown {
		t.Fatalf("deep nesting = %s, want unknown", v.Class)
	}
	if v := Classify(strings.Repeat("sudo ", maxDepth+2)+"ls", testEnv()); v.Class != Unknown {
		t.Fatalf("deep wrappers = %s, want unknown", v.Class)
	}
}

// TestWindowsShellRecursion (CL5-1): cmd /c|/k parses the rest of the line
// with the Cmd dialect, so rd/del follow the rm rules; an interactive cmd
// is Unknown.
func TestWindowsShellRecursion(t *testing.T) {
	t.Parallel()
	env := testEnv()
	env.Roots = []string{`C:\proj`}
	env.Home = `C:\Users\u`
	for _, c := range []labelCase{
		{`cmd /c rd /s /q C:\proj`, Destructive, 0},
		{`cmd /k del /s /q C:\proj`, Destructive, 0},
		{`cmd /c echo hi`, Mutating, 0},
		{`cmd /c`, Unknown, 0},
		{`cmd`, Unknown, 0},
	} {
		v := Classify(c.cmd, env)
		if v.Class != c.class || v.Flags != c.flags {
			t.Errorf("Classify(%q) = %s %v, want %s %v; %q", c.cmd, v.Class, v.Flags.Names(), c.class, c.flags.Names(), v.Reasons)
		}
	}
}
