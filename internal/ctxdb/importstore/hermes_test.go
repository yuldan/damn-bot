package importstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const currentSchema = `
CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT, model TEXT, cwd TEXT, title TEXT, started_at REAL NOT NULL);
CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL,
  role TEXT NOT NULL, content TEXT, tool_name TEXT, timestamp NOT NULL);`

const oldSchema = `
CREATE TABLE sessions (id TEXT PRIMARY KEY, created_at REAL NOT NULL);
CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL,
  role TEXT NOT NULL, content TEXT, created_at REAL NOT NULL);`

func makeDB(t *testing.T, schema string) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	for stmt := range strings.SplitSeq(schema, ";\n") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db, path
}

func TestReadHermesSessions(t *testing.T) {
	t.Parallel()
	db, path := makeDB(t, currentSchema)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	exec(`INSERT INTO sessions(id, source, model, cwd, title, started_at) VALUES ('s1','cli','openai/gpt','/work','First',100)`)
	exec(`INSERT INTO sessions(id, source, model, cwd, title, started_at) VALUES ('s2','cli',NULL,NULL,NULL,200)`)
	exec(`INSERT INTO messages(session_id, role, content, tool_name, timestamp) VALUES ('s1','user','hello',NULL,100.0)`)
	exec(`INSERT INTO messages(session_id, role, content, tool_name, timestamp) VALUES ('s1','assistant','hi',NULL,100.3)`)
	exec(`INSERT INTO messages(session_id, role, content, tool_name, timestamp) VALUES ('s1','user','middle',NULL,100.1)`)
	exec(`INSERT INTO messages(session_id, role, content, tool_name, timestamp) VALUES ('s2','tool','out','shell',200.0)`)
	exec(`INSERT INTO messages(session_id, role, content, tool_name, timestamp) VALUES ('s2','user','question',NULL,200)`)
	sessions, err := ReadHermes(t.Context(), path)
	if err != nil {
		t.Fatalf("ReadHermes: %v", err)
	}
	assertSessions(t, sessions)
}

func assertSessions(t *testing.T, sessions []HermesSession) {
	t.Helper()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d", len(sessions))
	}
	s1 := sessions[0]
	if s1.SourceID != "s1" || s1.Title != "First" || s1.Cwd != "/work" || len(s1.Messages) != 3 {
		t.Errorf("s1 = %+v", s1)
	}
	got := []string{s1.Messages[0].Content, s1.Messages[1].Content, s1.Messages[2].Content}
	want := []string{"hello", "middle", "hi"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
	if s1.Messages[2].Timestamp != 100300 || s1.Messages[0].Timestamp != 100000 {
		t.Errorf("timestamps = %d/%d", s1.Messages[0].Timestamp, s1.Messages[2].Timestamp)
	}
	s2 := sessions[1]
	if s2.Model != "" || s2.Title != "" || s2.Cwd != "" {
		t.Errorf("s2 = %+v", s2)
	}
	if s2.Messages[0].ToolName != "shell" || s2.Messages[1].Timestamp != 200000 {
		t.Errorf("s2 messages = %+v", s2.Messages)
	}
}

func TestReadHermesOldSchema(t *testing.T) {
	t.Parallel()
	db, path := makeDB(t, oldSchema)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO sessions(id, created_at) VALUES ('old', 10)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO messages(session_id, role, content, created_at) VALUES ('old','user','text',10.5)`); err != nil {
		t.Fatal(err)
	}
	sessions, err := ReadHermes(t.Context(), path)
	if err != nil {
		t.Fatalf("ReadHermes: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Messages[0].Timestamp != 10500 {
		t.Errorf("sessions = %+v", sessions)
	}
}

func TestReadHermesMillisecondIntegers(t *testing.T) {
	t.Parallel()
	db, path := makeDB(t, currentSchema)
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO sessions(id, source, model, cwd, title, started_at) VALUES ('s','cli','','','',1700000000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO messages(session_id, role, content, tool_name, timestamp) VALUES ('s','user','x','',1700000000123)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO messages(session_id, role, content, tool_name, timestamp) VALUES ('s','assistant','y','',1700000000456)`); err != nil {
		t.Fatal(err)
	}
	sessions, err := ReadHermes(t.Context(), path)
	if err != nil {
		t.Fatalf("ReadHermes: %v", err)
	}
	if got := sessions[0].Messages[0].Timestamp; got != 1700000000123 {
		t.Errorf("ms timestamp = %d", got)
	}
}

func TestReadHermesEmptyDatabase(t *testing.T) {
	t.Parallel()
	_, path := makeDB(t, currentSchema)
	sessions, err := ReadHermes(t.Context(), path)
	if err != nil || len(sessions) != 0 {
		t.Errorf("empty = %v, %v", sessions, err)
	}
}

func TestReadHermesInvalidDatabases(t *testing.T) {
	t.Parallel()
	notSQLite := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(notSQLite, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	noMessages := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", noMessages)
	if err != nil {
		t.Fatal(err)
	}
	if _, eerr := db.ExecContext(t.Context(), `CREATE TABLE sessions (id TEXT PRIMARY KEY)`); eerr != nil {
		t.Fatal(eerr)
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	missingContent := filepath.Join(t.TempDir(), "state.db")
	db, err = sql.Open("sqlite", missingContent)
	if err != nil {
		t.Fatal(err)
	}
	if _, eerr := db.ExecContext(t.Context(), `CREATE TABLE sessions (id TEXT PRIMARY KEY);
		CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT)`); eerr != nil {
		t.Fatal(eerr)
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	for _, path := range []string{notSQLite, noMessages, missingContent} {
		if _, err := ReadHermes(t.Context(), path); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", filepath.Base(filepath.Dir(path)), err)
		}
	}
}

func TestReadHermesMessageCap(t *testing.T) {
	t.Parallel()
	db, path := makeDB(t, currentSchema)
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO sessions(id, source, model, cwd, title, started_at) VALUES ('s','cli','','','',1)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxMessages + 2 {
		if _, eerr := tx.ExecContext(t.Context(),
			`INSERT INTO messages(session_id, role, content, tool_name, timestamp) VALUES ('s','user','m','',?)`,
			float64(i)); eerr != nil {
			t.Fatal(eerr)
		}
	}
	if cerr := tx.Commit(); cerr != nil {
		t.Fatal(cerr)
	}
	sessions, err := ReadHermes(t.Context(), path)
	if err != nil {
		t.Fatalf("ReadHermes: %v", err)
	}
	if len(sessions[0].Messages) != MaxMessages || sessions[0].Skipped != 2 {
		t.Errorf("messages = %d, skipped = %d", len(sessions[0].Messages), sessions[0].Skipped)
	}
}

func TestReadHermesContextCanceled(t *testing.T) {
	t.Parallel()
	_, path := makeDB(t, currentSchema)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadHermes(ctx, path); err == nil {
		t.Errorf("canceled context returned no error")
	}
}
