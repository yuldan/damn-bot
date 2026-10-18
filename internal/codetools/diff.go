package codetools

import (
	"fmt"
	"strings"
)

// diff limits (CODE-TOOLS §5.3 butir 6, §5.3 butir 2, CT-33).
const (
	diffContext  = 2
	diffMaxLines = 60
	diffMaxD     = 1000 // Myers edit distance bound; beyond: summary only
	staleRanges  = 10
)

// lineOp is one diff line: ' ' equal, '-' old only, '+' new only.
type lineOp struct {
	kind     byte
	old, new int // 0-based line numbers (old for ' '/'-', new for '+')
}

// lineDiff is a Myers O(ND) diff over lines after trimming the common
// prefix and suffix; ok=false when the distance exceeds diffMaxD.
func lineDiff(a, b []string) (ops []lineOp, ok bool) {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	for i := range pre {
		ops = append(ops, lineOp{' ', i, i})
	}
	mid, ok := myers(a[pre:len(a)-suf], b[pre:len(b)-suf])
	if !ok {
		return nil, false
	}
	for _, o := range mid {
		o.old += pre
		o.new += pre
		ops = append(ops, o)
	}
	for i := range suf {
		ops = append(ops, lineOp{' ', len(a) - suf + i, len(b) - suf + i})
	}
	return ops, true
}

func myers(a, b []string) ([]lineOp, bool) {
	n, m := len(a), len(b)
	maxD := min(n+m, diffMaxD)
	off := maxD + 1
	v := make([]int, 2*off+1)
	var trace [][]int
	for d := 0; d <= maxD; d++ {
		// trace[d] keeps v[off-d-1 : off+d+2], the only cells backtrack reads.
		trace = append(trace, append([]int(nil), v[off-d-1:off+d+2]...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || k != d && v[off+k-1] < v[off+k+1] {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x, y = x+1, y+1
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(trace, n, m, d), true
			}
		}
	}
	return nil, false
}

func backtrack(trace [][]int, x, y, d int) []lineOp {
	var rev []lineOp
	for ; d > 0; d-- {
		v := trace[d]
		at := func(k int) int { return v[k+d+1] }
		k := x - y
		var pk int
		if k == -d || k != d && at(k-1) < at(k+1) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := at(pk)
		py := px - pk
		for x > px && y > py {
			x, y = x-1, y-1
			rev = append(rev, lineOp{' ', x, y})
		}
		if x == px {
			y--
			rev = append(rev, lineOp{'+', x, y})
		} else {
			x--
			rev = append(rev, lineOp{'-', x, y})
		}
	}
	for x > 0 && y > 0 {
		x, y = x-1, y-1
		rev = append(rev, lineOp{' ', x, y})
	}
	ops := make([]lineOp, len(rev))
	for i, o := range rev {
		ops[len(rev)-1-i] = o
	}
	return ops
}

// diffStat counts added and removed lines.
func diffStat(ops []lineOp) (add, del int) {
	for _, o := range ops {
		switch o.kind {
		case '+':
			add++
		case '-':
			del++
		}
	}
	return add, del
}

// unified renders hunks with diffContext lines of context, cut at
// diffMaxLines rows (CODE-TOOLS §9.4).
func unified(ops []lineOp, a, b []string) string {
	var out []string
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		from := max(0, i-diffContext)
		to := i
		for to < len(ops) {
			if ops[to].kind != ' ' {
				to++
				continue
			}
			run := to
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-to > 2*diffContext {
				to = min(run, to+diffContext)
				break
			}
			to = run
		}
		out = append(out, hunk(ops[from:to], a, b)...)
		i = to
	}
	if len(out) > diffMaxLines {
		out = append(out[:diffMaxLines], fmt.Sprintf("… (%d more diff lines)", len(out)-diffMaxLines))
	}
	return strings.Join(out, "\n")
}

func hunk(ops []lineOp, a, b []string) []string {
	var oldStart, newStart, oldN, newN int
	oldStart, newStart = -1, -1
	rows := make([]string, 0, len(ops)+1)
	for _, o := range ops {
		switch o.kind {
		case ' ':
			rows = append(rows, " "+a[o.old])
			oldN++
			newN++
		case '-':
			rows = append(rows, "-"+a[o.old])
			oldN++
		case '+':
			rows = append(rows, "+"+b[o.new])
			newN++
		}
		if oldStart < 0 {
			oldStart, newStart = o.old, o.new
		}
	}
	return append([]string{fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart+1, oldN, newStart+1, newN)}, rows...)
}

// changedRanges summarizes which current lines differ from a snapshot:
// "L40-52, L118 (+6 -2)" (CODE-TOOLS §5.3 butir 2), or "changes: unknown".
func changedRanges(snap, cur []string) (summary, firstRange string) {
	ops, ok := lineDiff(snap, cur)
	if !ok {
		return "changes: unknown (too many)", ""
	}
	add, del := diffStat(ops)
	var rs []string
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		j := i
		lo, hi := -1, -1
		for j < len(ops) && ops[j].kind != ' ' {
			ln := max(1, min(ops[j].new+1, len(cur))) // deletions anchor at the next line
			if lo < 0 {
				lo = ln
			}
			hi = max(hi, ln)
			j++
		}
		if hi > lo {
			rs = append(rs, fmt.Sprintf("L%d-%d", lo, hi))
		} else {
			rs = append(rs, fmt.Sprintf("L%d", lo))
		}
		i = j
	}
	first := ""
	if len(rs) > 0 {
		first = rs[0]
	}
	if len(rs) > staleRanges {
		rs = append(rs[:staleRanges], "…")
	}
	return fmt.Sprintf("%s (+%d -%d)", strings.Join(rs, ", "), add, del), first
}
