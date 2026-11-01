//go:build !minimal

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestGAPF5_SEC18 (SPEC §22.1): RFC 7009 revocation kills the refresh token
// (the next refresh is invalid_grant), and an offline logout fails closed
// instead of reporting success.
func TestGAPF5_SEC18(t *testing.T) {
	t.Parallel()
	f := newFakeAS(t, true)
	store := fileStore(t)
	o := oauthFor(t, f, store, false, &strings.Builder{})
	if _, err := o.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	tok, err := store.Load(context.Background(), "fake")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Revoke(context.Background(), &tok); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !f.revoked["refresh-SECRET"] || !f.revoked["access-SECRET"] {
		t.Fatalf("revoked = %v, want both refresh and access", f.revoked)
	}
	if err := o.Refresh(context.Background(), &tok); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("refresh after revoke = %v, want ErrInvalidGrant", err)
	}

	offline := oauthFor(t, f, store, true, &strings.Builder{})
	offTok := sampleToken("fake")
	if err := offline.Revoke(context.Background(), &offTok); !errors.Is(err, ErrOffline) {
		t.Fatalf("offline revoke = %v, want ErrOffline", err)
	}
}
