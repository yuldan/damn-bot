package secret

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBackupKeyCreatesOnce(t *testing.T) {
	kr := newFakeKeyring()
	kr.unsupported = true
	s := tempStore(t, kr)
	ctx := context.Background()
	first, err := s.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Backup(ctx)
	if err != nil || second.ID != first.ID {
		t.Fatalf("second Backup = %+v, %v", second, err)
	}
	if len(first.Private) != KeySize || len(first.Public) != KeySize {
		t.Fatalf("key sizes = %d/%d", len(first.Private), len(first.Public))
	}
	fi, err := os.Stat(filepath.Join(s.dir, backupFile))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup file mode = %04o", fi.Mode().Perm())
	}
	ok, err := s.BackupAvailable(ctx, first.ID)
	if err != nil || !ok {
		t.Fatalf("available = %v, %v", ok, err)
	}
	ok, err = s.BackupAvailable(ctx, "00000000000000000000000000000001")
	if err != nil || ok {
		t.Fatalf("unknown id available = %v, %v", ok, err)
	}
}

func TestBackupKeychain(t *testing.T) {
	kr := newFakeKeyring()
	s := tempStore(t, kr)
	ctx := context.Background()
	k, err := s.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if k.ID == "" {
		t.Fatal("empty id")
	}
	kr.setDown()
	if _, err := s.BackupByID(ctx, k.ID); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("BackupByID with keychain down = %v", err)
	}
}

func TestBackupRotateKeepsOld(t *testing.T) {
	kr := newFakeKeyring()
	kr.unsupported = true
	s := tempStore(t, kr)
	ctx := context.Background()
	old, err := s.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.RotateBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == old.ID {
		t.Fatal("rotation kept the key id")
	}
	cur, err := s.Backup(ctx)
	if err != nil || cur.ID != next.ID {
		t.Fatalf("current = %+v, %v", cur, err)
	}
	back, err := s.BackupByID(ctx, old.ID)
	if err != nil || back.ID != old.ID {
		t.Fatalf("old key = %+v, %v", back, err)
	}
}

func TestBackupFileCorrupt(t *testing.T) {
	kr := newFakeKeyring()
	kr.unsupported = true
	s := tempStore(t, kr)
	if _, err := s.Backup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, backupFile), []byte(`{"v":1,"backend":"file","current":"x","keys":{"x":"!!"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Backup(context.Background()); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("corrupt backup file = %v", err)
	}
}

func TestBackupPlantedKeychainMetaFailsClosed(t *testing.T) {
	kr := newFakeKeyring()
	kr.unsupported = true
	s := tempStore(t, kr)
	attacker := make([]byte, KeySize)
	if _, err := rand.Read(attacker); err != nil {
		t.Fatal(err)
	}
	ak, err := backupKey(attacker)
	if err != nil {
		t.Fatal(err)
	}
	meta := `{"v":1,"backend":"keychain","current":"` + ak.ID + `"}`
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, backupFile), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Backup(context.Background()); err == nil {
		t.Fatal("planted keychain metadata was accepted without a keychain item")
	}
}

func TestBackupKeyRedaction(t *testing.T) {
	kr := newFakeKeyring()
	kr.unsupported = true
	s := tempStore(t, kr)
	k, err := s.Backup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hexKey := hex.EncodeToString(k.Private)
	for _, got := range []string{fmt.Sprintf("%v", k), fmt.Sprintf("%+v", k), k.String(), fmt.Sprintf("%#v", k)} {
		if strings.Contains(got, hexKey) || !strings.Contains(got, "[redacted]") {
			t.Fatalf("private key leaked in %q", got)
		}
	}
	k.Zero()
	if len(k.Private) != 0 {
		t.Fatal("Zero kept the private key")
	}
}
