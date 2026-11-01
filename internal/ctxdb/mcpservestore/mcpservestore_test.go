package mcpservestore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func open(t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	return db
}

func TestTokenCreateAuthenticateRevoke(t *testing.T) {
	t.Parallel()
	store := Store{DB: open(t)}
	ctx := context.Background()
	raw := "tok_abcdefghijklmnop"
	row := Token{IDHash: Hash(raw), Profile: "default", Identity: "owner", Scope: ScopeApprove, CreatedAt: 10}
	if err := store.Create(ctx, &row); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.Authenticate(ctx, raw, "default")
	if err != nil || got.Identity != "owner" || got.Scope != ScopeApprove {
		t.Fatalf("Authenticate = %+v, %v", got, err)
	}
	for _, tc := range []struct{ name, token, profile string }{
		{"wrong_token", "tok_other", "default"},
		{"other_profile", raw, "work"},
		{"empty", "", "default"},
	} {
		if _, err := store.Authenticate(ctx, tc.token, tc.profile); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s = %v, want ErrInvalidToken", tc.name, err)
		}
	}
	if ok, err := store.Revoke(ctx, row.IDHash, 20); err != nil || !ok {
		t.Fatalf("Revoke = %v, %v", ok, err)
	}
	if _, err := store.Authenticate(ctx, raw, "default"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("revoked authenticate = %v", err)
	}
	if ok, err := store.Revoke(ctx, row.IDHash, 30); err != nil || ok {
		t.Fatalf("second Revoke = %v, %v; want false, nil", ok, err)
	}
}

func TestTokenDefaultProfile(t *testing.T) {
	t.Parallel()
	store := Store{DB: open(t)}
	ctx := context.Background()
	raw := "tok_default_profile"
	if err := store.Create(ctx, &Token{IDHash: Hash(raw), Identity: "owner", Scope: ScopeRead, CreatedAt: 10}); err != nil {
		t.Fatalf("Create default profile: %v", err)
	}
	if _, err := store.Authenticate(ctx, raw, ""); err != nil {
		t.Fatalf("Authenticate default profile: %v", err)
	}
}

func TestTokenListAndValidation(t *testing.T) {
	t.Parallel()
	store := Store{DB: open(t)}
	ctx := context.Background()
	if err := store.Create(ctx, &Token{IDHash: Hash("a"), Profile: "p", Identity: "i", Scope: "root", CreatedAt: 1}); err == nil {
		t.Fatal("invalid scope accepted")
	}
	if err := store.Create(ctx, &Token{IDHash: "short", Profile: "p", Identity: "i", Scope: ScopeRead, CreatedAt: 1}); err == nil {
		t.Fatal("short hash accepted")
	}
	for i, scope := range []string{ScopeRead, ScopeSend, ScopeApprove} {
		raw := "token-" + scope
		if err := store.Create(ctx, &Token{IDHash: Hash(raw), Profile: "p", Identity: "id", Scope: scope, CreatedAt: int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.List(ctx, "p")
	if err != nil || len(rows) != 3 {
		t.Fatalf("List = %d, %v", len(rows), err)
	}
	if strings.Contains(rows[0].IDHash, "token-") {
		t.Fatal("List leaked a raw token")
	}
}

// seedApproval creates a session and one awaiting_approval journal row.
func seedApproval(t *testing.T, db *ctxdb.Store, requestID, payload string) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		if _, err := tx.CreateSession(ctxdb.Session{ID: "s1", Cwd: "/work", Model: "p/m", ConfigSHA: "sha", ContextTier: "core"}); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO turn_journal(turn_id, session_id, state, approval_request, started_at, updated_at)
			VALUES ('t1', 's1', 'awaiting_approval', ?1, 1, 2)`, payload); err != nil {
			return fmt.Errorf("insert journal: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = requestID
}

func payload(requestID, origin, class string, expires int64) string {
	return `{"request_id":"` + requestID + `","tool":"shell","class":"` + class + `","origin":"` + origin +
		`","preview_sha":"` + strings.Repeat("a", 64) + `","channels":["tui"],"expires_at":` + itoa(expires) + `}`
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestRespondAndOpenApprovals(t *testing.T) {
	t.Parallel()
	db := open(t)
	store := Store{DB: db}
	seedApproval(t, db, "r1", payload("r1", "tui", "shell", 0))
	open, err := store.OpenApprovals(context.Background(), 10)
	if err != nil || len(open) != 1 || open[0].RequestID != "r1" || open[0].SessionID != "s1" || open[0].TurnID != "t1" {
		t.Fatalf("OpenApprovals = %+v, %v", open, err)
	}
	sha := strings.Repeat("a", 64)
	ok, err := store.Respond(context.Background(), &RespondRequest{
		RequestID: "r1", Decision: "allow_once", PreviewSHA: sha,
		Responder: "owner", ResponderTokenHash: sha, Now: 100,
	})
	if err != nil || ok.TurnID != "t1" {
		t.Fatalf("Respond = %+v, %v", ok, err)
	}
	if _, err := store.Respond(context.Background(), &RespondRequest{
		RequestID: "r1", Decision: "deny", PreviewSHA: sha,
		Responder: "owner", ResponderTokenHash: sha, Now: 101,
	}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second respond = %v, want ErrNotPending", err)
	}
}

func TestRespondRefusesSelfApprovalAndP26(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 64)
	cases := []struct {
		name    string
		origin  string
		class   string
		expires int64
		preview string
		want    error
	}{
		{name: "self_origin", origin: "mcp_serve", class: "shell", preview: sha, want: ErrForbidden},
		{name: "empty_origin", origin: "", class: "shell", preview: sha, want: ErrForbidden},
		{name: "p26_irreversible", origin: "tui", class: "irreversible", preview: sha, want: ErrForbidden},
		{name: "p26_monetary", origin: "web", class: "monetary", preview: sha, want: ErrForbidden},
		{name: "preview_mismatch", origin: "tui", class: "shell", preview: strings.Repeat("b", 64), want: ErrForbidden},
		{name: "expired", origin: "tui", class: "shell", preview: sha, expires: 50, want: ErrNotPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := open(t)
			store := Store{DB: db}
			seedApproval(t, db, "r-"+tc.name, payload("r-"+tc.name, tc.origin, tc.class, tc.expires))
			_, err := store.Respond(context.Background(), &RespondRequest{
				RequestID: "r-" + tc.name, Decision: "allow_once", PreviewSHA: tc.preview,
				Responder: "owner", ResponderTokenHash: sha, Now: 100,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Respond = %v, want %v", err, tc.want)
			}
			var n int64
			if xerr := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
				var x int64
				if err := tx.Get(`SELECT count(*) FROM approval_responses`, nil, &x); err != nil {
					return fmt.Errorf("count responses: %w", err)
				}
				n = x
				return nil
			}); xerr != nil {
				t.Fatal(xerr)
			}
			if n != 0 {
				t.Fatalf("approval_responses rows = %d, want 0", n)
			}
		})
	}
}

func TestRespondRejectsEdit(t *testing.T) {
	t.Parallel()
	db := open(t)
	store := Store{DB: db}
	seedApproval(t, db, "r9", payload("r9", "tui", "shell", 0))
	_, err := store.Respond(context.Background(), &RespondRequest{
		RequestID: "r9", Decision: "edit", PreviewSHA: strings.Repeat("a", 64),
		Responder: "owner", ResponderTokenHash: strings.Repeat("a", 64), Now: 100,
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("edit = %v, want ErrForbidden", err)
	}
}
