package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

type fakeInstaller struct {
	rows   []InstallRow
	called int
}

func (f *fakeInstaller) Install(_ context.Context, rows []InstallRow) ([]string, error) {
	f.called++
	f.rows = rows
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out, nil
}

func manageEnv(t *testing.T, origin string) host.Env {
	t.Helper()
	view, err := host.NewSessionView("s1", "", origin, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return host.Env{Session: view}
}

func manageModule(t *testing.T, deps Deps) *Module {
	t.Helper()
	m := New(&Config{Enabled: true}, deps)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return m
}

func manageCall(t *testing.T, m *Module, env host.Env, tool, args string) (string, *host.Error) {
	t.Helper()
	res, err := m.Invoke(context.Background(), tool, json.RawMessage(args), env)
	if err == nil {
		return res.Content, nil
	}
	var he *host.Error
	if !errors.As(err, &he) {
		t.Fatalf("error %v is not a host error", err)
	}
	return "", he
}

// TestF8TOOL020ManageCatalog proves search is read-only, the tool refuses a
// non-GUI session, install reaches an installer only after the call, and a
// high-risk row needs the typed acknowledgement first.
func TestF8TOOL020ManageCatalog(t *testing.T) {
	t.Parallel()
	fake := &fakeInstaller{}
	m := manageModule(t, Deps{Manage: &ManageDeps{Installer: fake}})
	env := manageEnv(t, "desktop")
	catalogSearchChecks(t, m, env, fake)
	catalogInstallChecks(t, m, env, fake)
}

func catalogSearchChecks(t *testing.T, m *Module, env host.Env, fake *fakeInstaller) {
	t.Helper()
	out, he := manageCall(t, m, env, "manage_catalog", `{"action":"search","kind":"plugin","query":"adaption"}`)
	if he != nil || !strings.Contains(out, "adaption\tmcp\ttools\ttos_risk=low") {
		t.Fatalf("search = (%q, %+v)", out, he)
	}
	if fake.called != 0 {
		t.Fatal("search reached the installer")
	}
	if _, he = manageCall(t, m, manageEnv(t, "cli"), "manage_catalog", `{"action":"search","query":"adaption"}`); he == nil || he.Code != codeDenied {
		t.Fatalf("non-GUI search = %+v", he)
	}
}

func catalogInstallChecks(t *testing.T, m *Module, env host.Env, fake *fakeInstaller) {
	t.Helper()
	out, he := manageCall(t, m, env, "manage_catalog", `{"action":"install","items":[{"kind":"plugin","id":"adaption"}]}`)
	if he != nil || !strings.Contains(out, "requested adaption") || fake.called != 1 {
		t.Fatalf("install = (%q, %+v), calls=%d", out, he, fake.called)
	}
	if len(fake.rows) != 1 || fake.rows[0].ID != "adaption" || fake.rows[0].Risk != "low" {
		t.Fatalf("rows = %+v", fake.rows)
	}
	if _, he = manageCall(t, m, env, "manage_catalog", `{"action":"install","items":[{"kind":"skill","id":"adaption"}]}`); he == nil || he.Code != codeInvalidArgs {
		t.Fatalf("kind mismatch = %+v", he)
	}
	if _, he = manageCall(t, m, env, "manage_catalog", `{"action":"install","items":[{"kind":"plugin","id":"nope"}]}`); he == nil || he.Code != codeNotFound {
		t.Fatalf("unknown id = %+v", he)
	}
	catalogHighRiskChecks(t, m, env, fake)
}

func catalogHighRiskChecks(t *testing.T, m *Module, env host.Env, fake *fakeInstaller) {
	t.Helper()
	fake.called = 0
	if _, he := manageCall(t, m, env, "manage_catalog", `{"action":"install","items":[{"kind":"plugin","id":"hermes-talk"}]}`); he == nil || he.Code != codeDenied {
		t.Fatalf("high risk without ack = %+v", he)
	}
	if fake.called != 0 {
		t.Fatal("unacknowledged high-risk row reached the installer")
	}
	out, he := manageCall(t, m, env, "manage_catalog", `{"action":"install","ack":"hermes-talk","items":[{"kind":"plugin","id":"hermes-talk"}]}`)
	if he != nil || !strings.Contains(out, "requested hermes-talk") || fake.called != 1 {
		t.Fatalf("high risk with ack = (%q, %+v), calls=%d", out, he, fake.called)
	}
}

// TestF8TOOL020ManageCatalogUnavailable checks the fail-closed seam: no
// installer means install reports module_unavailable and nothing is started.
func TestF8TOOL020ManageCatalogUnavailable(t *testing.T) {
	t.Parallel()
	m := manageModule(t, Deps{})
	env := manageEnv(t, "web")
	if _, he := manageCall(t, m, env, "manage_catalog", `{"action":"install","items":[{"kind":"plugin","id":"adaption"}]}`); he == nil || he.Code != codeModuleUnavailable {
		t.Fatalf("unwired installer = %+v", he)
	}
}

// TestF8TOOL024ManageConnections covers hosted account lifecycle, the vault
// reference rule, and the fail-closed store.
func TestF8TOOL024ManageConnections(t *testing.T) {
	t.Parallel()
	store := NewConnections()
	m := manageModule(t, Deps{Manage: &ManageDeps{Accounts: store}})
	connectionsLifecycle(t, m, store)
	unwired := manageModule(t, Deps{})
	if _, he := manageCall(t, unwired, manageEnv(t, "web"), "manage_connections", `{"action":"status"}`); he == nil || he.Code != codeModuleUnavailable {
		t.Fatalf("unwired store = %+v", he)
	}
}

func connectionsLifecycle(t *testing.T, m *Module, store *Connections) {
	t.Helper()
	env := manageEnv(t, "web")
	connectionsConnectChecks(t, m, env, store)
	connectionsMutationChecks(t, m, env, store)
}

func connectionsConnectChecks(t *testing.T, m *Module, env host.Env, store *Connections) {
	t.Helper()
	if out, he := manageCall(t, m, env, "manage_connections", `{"action":"status"}`); he != nil || out != "" {
		t.Fatalf("empty status = (%q, %+v)", out, he)
	}
	out, he := manageCall(t, m, env, "manage_connections", `{"action":"connect","connector":"graph","account":"work","label":"Work mail"}`)
	if he != nil || !strings.Contains(out, "connected graph/work token=keychain") {
		t.Fatalf("connect = (%q, %+v)", out, he)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("connect printed a token: %q", out)
	}
	list := store.List("graph")
	if len(list) != 1 || !strings.HasPrefix(list[0].TokenRef, "keychain:") || list[0].Label != "Work mail" {
		t.Fatalf("stored account = %+v", list)
	}
	out, he = manageCall(t, m, env, "manage_connections", `{"action":"status","connector":"graph"}`)
	if he != nil || !strings.Contains(out, "graph/work label=Work mail token=keychain") {
		t.Fatalf("status = (%q, %+v)", out, he)
	}
}

func connectionsMutationChecks(t *testing.T, m *Module, env host.Env, store *Connections) {
	t.Helper()
	if _, he := manageCall(t, m, env, "manage_connections", `{"action":"connect","connector":"graph","account":"raw","token_ref":"secret-value"}`); he == nil || he.Code != codeInvalidArgs {
		t.Fatalf("raw token = %+v", he)
	}
	if _, he := manageCall(t, m, env, "manage_connections", `{"action":"connect","connector":"foreign","account":"x"}`); he == nil || he.Code != codeInvalidArgs {
		t.Fatalf("foreign connector = %+v", he)
	}
	if _, he := manageCall(t, m, env, "manage_connections", `{"action":"rename","connector":"graph","account":"work","label":"Renamed"}`); he != nil {
		t.Fatalf("rename = %+v", he)
	}
	if _, he := manageCall(t, m, env, "manage_connections", `{"action":"link","connector":"graph"}`); he == nil || he.Code != codeModuleUnavailable {
		t.Fatalf("link = %+v", he)
	}
	if _, he := manageCall(t, m, env, "manage_connections", `{"action":"disconnect","connector":"graph","account":"work"}`); he != nil {
		t.Fatalf("disconnect = %+v", he)
	}
	if len(store.List("")) != 0 {
		t.Fatal("disconnect left the account")
	}
}

// TestF8HostedStorePersists checks the shared 0600 store and that a reload
// returns references only.
func TestF8HostedStorePersists(t *testing.T) {
	t.Parallel()
	path := t.TempDir() + "/hosted.json"
	store, err := OpenConnections(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, addErr := store.Add("graph", "work", "Work", "keychain:connectors/graph/work"); addErr != nil {
		t.Fatal(addErr)
	}
	reopened, err := OpenConnections(path)
	if err != nil {
		t.Fatal(err)
	}
	list := reopened.List("")
	if len(list) != 1 || list[0].ID != "work" || !strings.HasPrefix(list[0].TokenRef, "keychain:") {
		t.Fatalf("reloaded = %+v", list)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}
