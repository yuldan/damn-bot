package heal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func newBreakerDB(t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Fatal(cerr)
		}
	})
	return db
}

func TestBreakerTripLoad(t *testing.T) {
	db := newBreakerDB(t)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	b := NewBreaker(db, func() time.Time { return now })

	st, err := b.Load(context.Background())
	if err != nil || st.SafeMode {
		t.Fatalf("initial Load = %+v, %v", st, err)
	}
	if err = b.Trip(context.Background(), "5 crashes in 10m"); err != nil {
		t.Fatal(err)
	}
	if err = b.Trip(context.Background(), "second trip"); err != nil {
		t.Fatal(err)
	}
	st, err = b.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.SafeMode || st.Reason != "5 crashes in 10m" || !st.Since.Equal(now) {
		t.Fatalf("Load after Trip = %+v", st)
	}
}

func TestBreakerResume(t *testing.T) {
	db := newBreakerDB(t)
	b := NewBreaker(db, nil)
	ctx := context.Background()

	if _, err := b.Resume(ctx); !errors.Is(err, ErrNotSafeMode) {
		t.Fatalf("Resume before Trip = %v, want ErrNotSafeMode", err)
	}
	if err := b.Trip(ctx, "5 crashes"); err != nil {
		t.Fatal(err)
	}
	after, err := b.Resume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.SafeMode || after.Reason != "" || after.Crashes != 0 {
		t.Fatalf("Resume = %+v", after)
	}
	if _, err = b.Resume(ctx); !errors.Is(err, ErrNotSafeMode) {
		t.Fatalf("second Resume error = %v, want ErrNotSafeMode", err)
	}
	st, err := b.Load(ctx)
	if err != nil || st.SafeMode {
		t.Fatalf("Load after Resume = %+v, %v", st, err)
	}
}

func TestBreakerNilStore(t *testing.T) {
	b := NewBreaker(nil, nil)
	if st, err := b.Load(context.Background()); err != nil || st.SafeMode {
		t.Fatalf("nil store Load = %+v, %v", st, err)
	}
	if err := b.Trip(context.Background(), "x"); err == nil {
		t.Fatal("Trip with nil store succeeded")
	}
	if _, err := b.Resume(context.Background()); err == nil {
		t.Fatal("Resume with nil store succeeded")
	}
}
