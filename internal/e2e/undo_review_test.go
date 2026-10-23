package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// fileSHA is the sha256 of one workspace file: the byte-identity evidence of
// a restore, logged so a failure shows both hashes.
func fileSHA(t *testing.T, e *Env, rel string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(e.ReadFile(rel)))
	got := hex.EncodeToString(sum[:])
	t.Logf("sha256(%s) = %s", rel, got)
	return got
}

// TestE2EUndoRestoresTheEditedFileAndListsTheAction (scenario 5 of PR #239):
// a real turn edits a workspace file through the journal, the change is listed
// by the listing the binary has for it (`undo ls`), and `undo` restores the
// exact bytes — while a file the turn never touched is byte-identical and
// appears in no listing. `review` is the memory quarantine queue (P10), so it
// must not claim a file change either.
func TestE2EUndoRestoresTheEditedFileAndListsTheAction(t *testing.T) {
	t.Parallel()
	const orig = "alpha\nbeta\n"
	sum := sha256.Sum256([]byte(orig))
	tag := hex.EncodeToString(sum[:4]) // codetools' read tag (sumTag)
	e := New(t,
		ToolCallReply("c1", "read_file", `{"path":"target.txt"}`),
		ToolCallReply("c2", "edit_file", fmt.Sprintf(`{"path":"target.txt","tag":%q,"edits":[{"old_string":"beta","new_string":"gamma"}]}`, tag)),
		providertest.Text("done"),
	)
	e.WriteFile("target.txt", orig)
	e.WriteFile("bystander.txt", "untouched\n")
	before := fileSHA(t, e, "target.txt")
	bystander := fileSHA(t, e, "bystander.txt")

	res := e.Run("edit the target", "--approval", "auto")
	decodeRun(t, "edit turn", res)
	if res.Code != 0 {
		t.Fatalf("run exit = %d", res.Code)
	}
	if edited := fileSHA(t, e, "target.txt"); edited == before {
		t.Fatalf("the turn did not edit target.txt")
	}
	if got := e.ReadFile("target.txt"); got != "alpha\ngamma\n" {
		t.Fatalf("edited content = %q, want alpha/gamma", got)
	}
	if fileSHA(t, e, "bystander.txt") != bystander {
		t.Fatalf("the turn touched bystander.txt")
	}

	ls := e.Exec("undo", "ls", "--data-dir", e.DataDir)
	ls.RequireOK(t)
	t.Logf("undo ls ->\n%s", ls.Stdout)
	if !strings.Contains(ls.Stdout, "file_edit") || !strings.Contains(ls.Stdout, "target.txt") {
		t.Errorf("undo ls does not list the change:\n%s", ls.Stdout)
	}
	if strings.Contains(ls.Stdout, "bystander") {
		t.Errorf("undo ls lists a file the turn never wrote:\n%s", ls.Stdout)
	}

	rev := e.Exec("review", "--data-dir", e.DataDir)
	t.Logf("review -> exit=%d stdout=%q stderr=%q", rev.Code, rev.Stdout, rev.Stderr)
	if rev.Code != 0 || strings.Contains(rev.Stdout, "target.txt") {
		t.Errorf("review exit %d claims the file change: %q", rev.Code, rev.Stdout)
	}

	un := e.Exec("undo", "--data-dir", e.DataDir)
	un.RequireOK(t)
	t.Logf("undo ->\n%s", un.Stdout)
	if got := e.ReadFile("target.txt"); got != orig {
		t.Fatalf("undo restored %q, want %q", got, orig)
	}
	if after := fileSHA(t, e, "target.txt"); after != before {
		t.Errorf("hash after undo = %s, want the pre-turn %s", after, before)
	}
	if fileSHA(t, e, "bystander.txt") != bystander {
		t.Errorf("undo touched bystander.txt")
	}
}
