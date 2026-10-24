package compaction

import (
	"crypto/sha256"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Stage-1 windows (SPEC §4.7). "Older than the last N" keeps the newest N.
const (
	keepResults    = 6   // rule 3: newest tool results kept whole
	keepIterations = 10  // rule 4: newest assistant messages kept whole
	keepImageTurns = 2   // rule 5: newest user turns whose images are sent
	trimRunes      = 200 // rule 4 length
	shortSHA       = 12
)

// PruneStats counts stage-1 replacements per rule (SPEC §4.7 1–5) and the
// orphan tool results history dropped.
type PruneStats struct {
	Dups, Stale, Old, Trimmed, Images, Orphans int
}

// Prune applies the five deterministic stage-1 rules to the events after the
// summary boundary and returns the request view; the database is not touched.
// Only contents change: a message is never added, removed or reordered (apart
// from orphan tool results, see PruneStats.Orphans), so every assistant tool
// call keeps its result. A stub replaces a whole content, so an <untrusted>
// wrapper is never cut open, and a stub is applied only when it is shorter.
func Prune(events []Event) ([]Message, PruneStats) {
	items, orphans := history(events)
	st := prune(items, nil)
	st.Orphans = orphans
	return messages(items), st
}

func prune(items []item, pins []int64) PruneStats {
	pending := pendingFrom(items)
	saved := keepPinned(items, pins)
	st := PruneStats{
		Dups:    dedupe(items, pending),
		Stale:   stale(items, pending),
		Old:     old(items, pending),
		Trimmed: trim(items),
		Images:  images(items),
	}
	restorePinned(items, pins, saved)
	return st
}

// keepPinned snapshots the model-visible form of every pinned message before a
// stage-1 rule can stub it; restorePinned puts it back (MEM-025).
func keepPinned(items []item, pins []int64) map[int64]Message {
	if len(pins) == 0 {
		return nil
	}
	set := pinSet(pins)
	out := make(map[int64]Message, len(pins))
	for i := range items {
		if set[items[i].seq] {
			out[items[i].seq] = items[i].Message
		}
	}
	return out
}

func restorePinned(items []item, pins []int64, saved map[int64]Message) {
	if len(saved) == 0 {
		return
	}
	for i := range items {
		if m, ok := saved[items[i].seq]; ok {
			items[i].Message, items[i].stubbed = m, false
		}
	}
}

// unpinned drops the pinned messages from a slice of items (MEM-025).
func unpinned(items []item, pins []int64) []item {
	if len(pins) == 0 {
		return items
	}
	set := pinSet(pins)
	out := make([]item, 0, len(items))
	for i := range items {
		if !set[items[i].seq] {
			out = append(out, items[i])
		}
	}
	return out
}

// pinSet indexes the pinned sequences.
func pinSet(pins []int64) map[int64]bool {
	set := make(map[int64]bool, len(pins))
	for _, p := range pins {
		set[p] = true
	}
	return set
}

// pendingFrom is the index of the first result of the latest tool round when
// the history ends with it: the model has not read those results yet, so no
// rule touches them (EC-CTX-019). Without such a round it is len(items).
func pendingFrom(items []item) int {
	i := len(items)
	for i > 0 && items[i-1].Role == "tool" {
		i--
	}
	if i < len(items) && i > 0 && len(items[i-1].ToolCalls) > 0 {
		return i
	}
	return len(items)
}

// stub replaces it.Content with s when that saves bytes.
func stub(it *item, s string) bool {
	if it.stubbed || len(s) >= len(it.Content) {
		return false
	}
	it.Content, it.stubbed = s, true
	return true
}

// dedupe is rule 1: of identical (tool, args, result) only the newest stays.
func dedupe(items []item, pending int) int {
	newest := map[[32]byte]string{}
	n := 0
	for i := len(items) - 1; i >= 0; i-- {
		it := &items[i]
		if it.Role != "tool" {
			continue
		}
		k := sha256.Sum256([]byte(it.tool + "\x00" + it.args + "\x00" + it.Content))
		id, seen := newest[k]
		if !seen {
			newest[k] = it.ToolCallID
			continue
		}
		if i < pending && stub(it, "[dup of call "+id+"]") {
			n++
		}
	}
	return n
}

// stale is rule 2: a read_file result followed by a successful write_file or
// edit_file of the same path names the seq of the nearest later mutation.
func stale(items []item, pending int) int {
	next := map[string]int64{}
	n := 0
	for i := len(items) - 1; i >= 0; i-- {
		it := &items[i]
		if it.Role != "tool" || it.path == "" || it.failed {
			continue
		}
		switch it.tool {
		case "write_file", "edit_file":
			next[it.path] = it.seq
		case "read_file":
			seq, ok := next[it.path]
			if ok && i < pending && stub(it, "[stale: file modified at seq "+strconv.FormatInt(seq, 10)+"]") {
				n++
			}
		}
	}
	return n
}

// old is rule 3: tool results beyond the newest keepResults become a pointer
// to the blob and the re-read path. tool_search results carry the pinned
// schemas and are never pruned here.
func old(items []item, pending int) int {
	rank, n := 0, 0
	for i := len(items) - 1; i >= 0; i-- {
		it := &items[i]
		if it.Role != "tool" {
			continue
		}
		rank++
		if rank > keepResults && i < pending && it.tool != "tool_search" && stub(it, prunedStub(it)) {
			n++
		}
	}
	return n
}

func prunedStub(it *item) string {
	var b strings.Builder
	if it.tool == "skill_view" {
		b.WriteString("[SKILL_PRUNED] ")
	}
	b.WriteString("[pruned: " + strconv.Itoa(utf8.RuneCountInString(it.Content)) + " chars")
	if len(it.blob) >= shortSHA {
		b.WriteString(", blob " + it.blob[:shortSHA])
	}
	b.WriteString("; ")
	if it.tool == "read_file" && it.path != "" {
		b.WriteString("read_file " + it.path + " or ")
	}
	b.WriteString("recall]")
	return b.String()
}

// trim is rule 4: assistant text between tool calls older than the newest
// keepIterations assistant messages is cut to trimRunes runes. A final
// answer (no tool call) is never cut.
func trim(items []item) int {
	rank, n := 0, 0
	for i := len(items) - 1; i >= 0; i-- {
		it := &items[i]
		if it.Role != "assistant" {
			continue
		}
		rank++
		if rank <= keepIterations || len(it.ToolCalls) == 0 || utf8.RuneCountInString(it.Content) <= trimRunes {
			continue
		}
		r := []rune(it.Content)
		it.Content = string(r[:trimRunes-1]) + "…"
		n++
	}
	return n
}

// images is rule 5: images of user turns older than the newest
// keepImageTurns are replaced by a text stub naming the blob.
func images(items []item) int {
	turn, n := 0, 0
	for i := len(items) - 1; i >= 0; i-- {
		it := &items[i]
		if it.Role != "user" {
			continue
		}
		turn++
		if turn <= keepImageTurns {
			continue
		}
		for _, sha := range it.Images {
			if it.Content != "" {
				it.Content += "\n"
			}
			it.Content += "[image " + sha[:min(len(sha), shortSHA)] + " omitted]"
			n++
		}
		it.Images = nil
	}
	return n
}
