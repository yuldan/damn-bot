//go:build unix

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestServeShutdownDuringCronHealth seeds a stalled job so the first cron tick
// writes health markers, then stops the daemon immediately. Shutdown must not
// close the DB while that write runs (exit 0, no "table is locked").
func TestServeShutdownDuringCronHealth(t *testing.T) {
	for i := range 10 {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			e := New(t)
			db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: e.DataDir, Kind: "run"})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
				_, xerr := tx.Exec(`INSERT INTO cron_jobs(id, name, owner_identity, schedule, prompt, prompt_sha, cwd, enabled, created_at, updated_at)
					VALUES ('stalled', 'stalled', 'local', '* * * * *', 'p', 'sha', '/tmp', 1, ?1, ?1)`,
					time.Now().Add(-2*time.Hour).UnixMilli())
				if xerr != nil {
					return fmt.Errorf("seed job: %w", xerr)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			p := startServe(t, e, binPaths.main, nil)
			p.terminate()
			if code := p.waitExit(30 * time.Second); code != 0 {
				t.Fatalf("exit = %d\nstderr:\n%s", code, p.stderr.String())
			}
			if strings.Contains(p.stderr.String(), "table is locked") {
				t.Fatalf("locked during shutdown:\n%s", p.stderr.String())
			}
		})
	}
}
