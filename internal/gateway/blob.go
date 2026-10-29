package gateway

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrNoBlobStore rejects a blob guard without an ownership source.
var ErrNoBlobStore = errors.New("gateway: blob store is required")

// BlobStore answers whether a content-addressed blob belongs to a gateway
// principal (ADR-084). internal/ctxdb/gwstore implements it from blob_refs,
// session_labels and gateway_inbound.
type BlobStore interface {
	BlobOwned(ctx context.Context, sha, platform, identity string) (bool, error)
}

// BlobGuard is the role user read exception: only blobs referenced by that
// identity's own session or inbound update are readable, never another
// identity's (SPEC §9.7, TRUST-MODEL §2).
type BlobGuard struct{ store BlobStore }

// NewBlobGuard wires a guard to its ownership store.
func NewBlobGuard(store BlobStore) (*BlobGuard, error) {
	if store == nil {
		return nil, ErrNoBlobStore
	}
	return &BlobGuard{store: store}, nil
}

// Allow reports whether sc may read sha. Unknown, foreign, malformed or empty
// shas are denied; a store error must also be treated as a denial.
func (g *BlobGuard) Allow(ctx context.Context, sc *Scope, sha string) (bool, error) {
	if sc == nil || !validBlobSHA(sha) {
		return false, nil
	}
	owned, err := g.store.BlobOwned(ctx, sha, sc.Platform, sc.UserID)
	if err != nil {
		return false, fmt.Errorf("gateway: blob %s: %w", sha, err)
	}
	return owned, nil
}

func validBlobSHA(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	_, err := hex.DecodeString(sha)
	return err == nil
}
