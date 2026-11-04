package compaction

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"testing"
)

// TestF6MEM037ImageEvictionReplay: a 30-screenshot session retires in one
// batch (≤2 full cache misses), every stub names a blob that replays
// byte-identically, and the live turn is never touched.
func TestF6MEM037ImageEvictionReplay(t *testing.T) {
	t.Parallel()
	const size = 1200 << 10 // 1.2 MiB per screenshot
	blobs := map[string][]byte{}
	b := transcript{t: t}
	batches, evictedSoFar := 0, 0
	for i := range 30 {
		data := []byte(fmt.Sprintf("screenshot-%02d", i))
		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])
		blobs[sha] = data
		b.userSized(fmt.Sprintf("shot %d", i), []int64{size}, sha)
		b.assistant("ok")
		if _, st := Prune(b.events); st.Images > evictedSoFar {
			batches++
			evictedSoFar = st.Images
		}
	}
	if batches > 2 {
		t.Fatalf("full cache misses = %d, want ≤ 2", batches)
	}
	msgs, st := Prune(b.events)
	if st.Images == 0 {
		t.Fatal("30 screenshots at 1.2 MiB did not trigger eviction")
	}
	evicted := EvictedImages(msgs)
	if len(evicted) != st.Images {
		t.Fatalf("evicted stubs = %d, stats = %d", len(evicted), st.Images)
	}
	for _, sha := range evicted {
		want, ok := blobs[sha]
		if !ok {
			t.Fatalf("stub names unknown blob %s", sha)
		}
		got, err := ReplayImage(sha, func(s string) ([]byte, error) { return blobs[s], nil })
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("replay %s = %q, %v; want %q", sha, got, err, want)
		}
	}
	var live Message
	for _, m := range slices.Backward(msgs) {
		if m.Role == "user" {
			live = m
			break
		}
	}
	if len(live.Images) != 1 {
		t.Fatalf("live turn images = %v, want one attachment", live.Images)
	}
	if _, ok := blobs[live.Images[0]]; !ok {
		t.Fatalf("live turn lost its image: %v", live.Images)
	}
}

// TestF6GAPCOST05EvictionThreshold pins the 80%→50% batch policy and the
// negative controls: exactly 80% does not evict, and a live turn is never
// cut even when it alone crosses the high watermark.
func TestF6GAPCOST05EvictionThreshold(t *testing.T) {
	t.Parallel()
	t.Run("count-high-water", countHighWater)
	t.Run("exactly-eighty-percent", exactlyEighty)
	t.Run("bytes-high-water", bytesHighWater)
	t.Run("live-turn-protected", liveTurnProtected)
}

func countHighWater(t *testing.T) {
	b := transcript{t: t}
	for range ImageHighWater*ImageLimitCount/100 + 1 {
		b.userSized("shot", []int64{1}, fmt.Sprintf("%064d", len(b.events)))
		b.assistant("ok")
	}
	msgs, st := Prune(b.events)
	if st.Images == 0 {
		t.Fatal("81 images did not trigger the count batch")
	}
	left := 0
	for _, m := range msgs {
		left += len(m.Images)
	}
	if left*100 > ImageLimitCount*ImageLowWater {
		t.Fatalf("images left = %d, want ≤ %d", left, ImageLimitCount*ImageLowWater/100)
	}
}

func exactlyEighty(t *testing.T) {
	b := transcript{t: t}
	for range ImageHighWater * ImageLimitCount / 100 {
		b.userSized("shot", []int64{1}, fmt.Sprintf("%064d", len(b.events)))
		b.assistant("ok")
	}
	if _, st := Prune(b.events); st.Images != 0 {
		t.Fatalf("80 images evicted %d, want 0", st.Images)
	}
}

func bytesHighWater(t *testing.T) {
	const each = int64(1100 << 10)
	b := transcript{t: t}
	for range 24 {
		b.userSized("shot", []int64{each}, fmt.Sprintf("%064d", len(b.events)))
		b.assistant("ok")
	}
	msgs, st := Prune(b.events)
	if st.Images == 0 {
		t.Fatal("24 × 1.1 MiB did not trigger the byte batch")
	}
	left := 0
	for _, m := range msgs {
		left += len(m.Images)
	}
	if int64(left)*each > ImageLimitBytes*ImageLowWater/100 {
		t.Fatalf("kept bytes = %d, want ≤ %d", int64(left)*each, ImageLimitBytes*ImageLowWater/100)
	}
}

func liveTurnProtected(t *testing.T) {
	b := transcript{t: t}
	b.userSized("one burst",
		[]int64{ImageLimitBytes / 2, ImageLimitBytes / 2, ImageLimitBytes / 2},
		fmt.Sprintf("%064d", 0), fmt.Sprintf("%064d", 1), fmt.Sprintf("%064d", 2))
	b.assistant("ok")
	msgs, st := Prune(b.events)
	if st.Images != 0 || len(msgs[0].Images) != 3 {
		t.Fatalf("live turn images = %v, stats = %+v", msgs[0].Images, st)
	}
}

// TestF6MEM037ReplayNegativeControls: a missing blob and a hash mismatch are
// distinct errors and never return another image.
func TestF6MEM037ReplayNegativeControls(t *testing.T) {
	t.Parallel()
	sha := fmt.Sprintf("%064d", 7)
	if data, err := ReplayImage(sha, func(string) ([]byte, error) { return nil, errors.New("not found") }); !errors.Is(err, ErrImageBlobMissing) || data != nil {
		t.Fatalf("missing = %q, %v", data, err)
	}
	if data, err := ReplayImage(sha, func(string) ([]byte, error) { return []byte("other image"), nil }); !errors.Is(err, ErrImageBlobMismatch) || data != nil {
		t.Fatalf("mismatch = %q, %v", data, err)
	}
}

// TestF6MEM037ImageEviction is the ledger name; the replay variant adds the
// round-trip proof.
func TestF6MEM037ImageEviction(t *testing.T) {
	TestF6MEM037ImageEvictionReplay(t)
}
