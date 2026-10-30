package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func localFixture(t *testing.T, active string) (data, bin string) {
	t.Helper()
	dir := t.TempDir()
	data = filepath.Join(dir, "data")
	bin = filepath.Join(dir, "app", "damnbot")
	for _, d := range []string{filepath.Dir(bin), filepath.Join(data, "update")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(bin, []byte(active), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".prev", []byte("prev-binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	return data, bin
}

func writeLocalPending(t *testing.T, data, bin, target string) {
	t.Helper()
	sum := sha256.Sum256([]byte(target))
	p := pending{
		PrevBinary: bin + ".prev", PrevVersion: "1.0.0", TargetVersion: "1.1.0",
		TargetSHA256: hex.EncodeToString(sum[:]), CreatedAt: "now",
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "update", pendingFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRepairLocalChecksum: a matching target verifies; a wrong target restores
// .prev without touching the database.
func TestRepairLocalChecksum(t *testing.T) {
	data, bin := localFixture(t, "good-binary")
	writeLocalPending(t, data, bin, "good-binary")
	o := &InstallOptions{DataDir: data, BinaryPath: bin, HealthCheck: func(context.Context, string) error { return nil }}
	status, err := RepairLocal(context.Background(), o)
	if err != nil || status != "verified 1.1.0" {
		t.Fatalf("verify = %q %v", status, err)
	}
	if _, serr := os.Stat(filepath.Join(data, "update", pendingFile)); !os.IsNotExist(serr) {
		t.Fatal("pending kept after verify")
	}

	data, bin = localFixture(t, "bad-binary")
	writeLocalPending(t, data, bin, "good-binary")
	o = &InstallOptions{DataDir: data, BinaryPath: bin, HealthCheck: func(context.Context, string) error { return nil }}
	status, err = RepairLocal(context.Background(), o)
	if err != nil || status != "rolled back: installed binary failed checksum" {
		t.Fatalf("rollback = %q %v", status, err)
	}
	got, err := os.ReadFile(bin) // #nosec G304
	if err != nil || string(got) != "prev-binary" {
		t.Fatalf("active = %q %v, want prev", got, err)
	}
	if _, serr := os.Stat(filepath.Join(data, "update", pendingFile)); !os.IsNotExist(serr) {
		t.Fatal("pending kept after rollback")
	}

	data, bin = localFixture(t, "good-binary")
	if _, err := RepairLocal(context.Background(), &InstallOptions{DataDir: data, BinaryPath: bin}); !errors.Is(err, ErrNoPending) {
		t.Fatalf("no pending = %v", err)
	}
}
