package compaction

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Provider image envelope (GAP-COST-05, SPEC §9.5). The hard limits are the
// documented request caps; the two watermarks make eviction one batch instead
// of one cache-breaking shift per turn.
const (
	// ImageLimitBytes is the hard encoded-image budget of one request.
	ImageLimitBytes int64 = 32 << 20
	// ImageLimitCount is the hard attachment-count budget of one request.
	ImageLimitCount = 100
	// ImageHighWater triggers a retirement batch at 80% of either budget.
	ImageHighWater = 80
	// ImageLowWater is the target after a batch: 50% of both budgets.
	ImageLowWater = 50
)

// ErrImageBlobMissing is a replay whose stub names a blob the store no longer
// has. It is never replaced by a different attachment.
var ErrImageBlobMissing = errors.New("compaction: evicted image blob is missing")

// ErrImageBlobMismatch is a replay whose bytes do not hash to the stubbed
// blob_sha; the caller must not send them.
var ErrImageBlobMismatch = errors.New("compaction: evicted image blob does not match its sha")

// ImageStub is the model-visible placeholder for one evicted attachment. The
// full sha256 is kept so the image can be replayed from the blob store.
func ImageStub(sha string) string {
	return "[image blob_sha=" + sha + " omitted]"
}

// evictedPrefix is the stable part every stub starts with.
const evictedPrefix = "[image blob_sha="

// EvictedImages returns the blob shas named by image stubs, in message order.
// It is the recovery list for ReplayImage; a message without stubs yields nil.
func EvictedImages(msgs []Message) []string {
	var out []string
	for i := range msgs {
		for line := range strings.SplitSeq(msgs[i].Content, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, evictedPrefix) {
				continue
			}
			rest := strings.TrimPrefix(line, evictedPrefix)
			sha, _, ok := strings.Cut(rest, " ")
			if !ok || len(sha) != 64 {
				continue
			}
			out = append(out, sha)
		}
	}
	return out
}

// ImageLoader resolves one blob by sha (ctxdb's Blob method satisfies it).
type ImageLoader func(sha string) ([]byte, error)

// ReplayImage reads an evicted attachment back from the blob store and
// verifies the bytes against the stubbed sha. A missing blob and a hash
// mismatch are distinct typed errors; neither returns a different image.
func ReplayImage(sha string, load ImageLoader) ([]byte, error) {
	if load == nil {
		return nil, fmt.Errorf("%w: %s (no loader)", ErrImageBlobMissing, sha)
	}
	data, err := load(sha)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrImageBlobMissing, sha, err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != sha {
		return nil, fmt.Errorf("%w: %s", ErrImageBlobMismatch, sha)
	}
	return data, nil
}

// evictImages keeps a sliding window of image attachments: images join in
// seq order and, once the window crosses 80% of either budget, the oldest
// non-live ones leave together until it is back at 50%. Because the window is
// rebuilt from the same event order, the kept prefix is stable between
// batches: a new screenshot only appends, so the provider cache stays warm.
// The live turn's attachments are never evicted, and only seq order is used
// (the package has no clock).
func evictImages(items []item) int {
	live := liveTurn(items)
	var window []imageRef
	var totalBytes int64
	var totalCount, head int
	for i := range items {
		window, totalBytes, totalCount, head = addImages(items, i, live, window, totalBytes, totalCount, head)
	}
	if head == 0 {
		return 0
	}
	return markKept(items, window[head:])
}

type imageRef struct{ item, index int }

// addImages appends one user turn to the sliding window and retires the oldest
// non-live attachments until the window is back at 50%.
func addImages(items []item, i int, live string, window []imageRef, totalBytes int64, totalCount, head int) ([]imageRef, int64, int, int) {
	it := &items[i]
	if it.Role != "user" || len(it.Images) == 0 {
		return window, totalBytes, totalCount, head
	}
	for j := range it.Images {
		window = append(window, imageRef{i, j})
		totalCount++
		totalBytes += imageSize(it, j)
	}
	if !imageHighWater(totalBytes, totalCount) {
		return window, totalBytes, totalCount, head
	}
	for head < len(window) && !imageLowWater(totalBytes, totalCount) {
		r := window[head]
		if live != "" && items[r.item].turn == live {
			break // the running turn is in use; never evict it
		}
		totalCount--
		totalBytes -= imageSize(&items[r.item], r.index)
		head++
	}
	return window, totalBytes, totalCount, head
}

// markKept rewrites every user message with the stubs of the retired prefix and
// the still-sent suffix.
func markKept(items []item, keptRefs []imageRef) int {
	kept := make(map[imageRef]bool, len(keptRefs))
	for _, r := range keptRefs {
		kept[r] = true
	}
	evicted := 0
	for i := range items {
		it := &items[i]
		if it.Role != "user" || len(it.Images) == 0 {
			continue
		}
		images := make([]string, 0, len(it.Images))
		bytes := make([]int64, 0, len(it.Images))
		for j, sha := range it.Images {
			if kept[imageRef{i, j}] {
				images = append(images, sha)
				bytes = append(bytes, imageSize(it, j))
				continue
			}
			if it.Content != "" {
				it.Content += "\n"
			}
			it.Content += ImageStub(sha)
			evicted++
		}
		if len(images) == 0 {
			images, bytes = nil, nil
		}
		it.Images, it.imageBytes = images, bytes
	}
	return evicted
}

// imageHighWater reports whether either budget is above its high watermark.
func imageHighWater(bytes int64, count int) bool {
	return bytes*100 > ImageLimitBytes*ImageHighWater ||
		int64(count)*100 > int64(ImageLimitCount)*ImageHighWater
}

// imageLowWater reports whether both budgets are at or below the target.
func imageLowWater(bytes int64, count int) bool {
	return bytes*100 <= ImageLimitBytes*ImageLowWater &&
		int64(count)*100 <= int64(ImageLimitCount)*ImageLowWater
}

// liveTurn is the turn of the newest event; its attachments are in use by the
// running turn and are never evicted.
func liveTurn(items []item) string {
	if len(items) == 0 {
		return ""
	}
	return items[len(items)-1].turn
}

func imageSize(it *item, j int) int64 {
	if j < len(it.imageBytes) && it.imageBytes[j] > 0 {
		return it.imageBytes[j]
	}
	return 0
}
