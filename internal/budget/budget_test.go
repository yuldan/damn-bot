package budget

import (
	"bufio"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want Counts
	}{
		{"", Counts{}},
		{"hello, world 42", Counts{Latin: 15}},
		{"café naïve", Counts{Latin: 10}},
		{"привет мир", Counts{Latin: 1, Mid: 9}},
		{"مرحبا", Counts{Mid: 5}},
		{"नमस्ते", Counts{Mid: 6}},
		{"日本語のテキスト。", Counts{Wide: 9}},
		{"한국어", Counts{Wide: 3}},
		{"ok 👍", Counts{Latin: 3, Wide: 1}},
		{"\xff\xfe", Counts{Wide: 2}},
	}
	for _, c := range cases {
		got := Count(c.in)
		if got.Latin+got.Mid+got.Wide != len([]rune(c.in)) && c.in != "\xff\xfe" {
			t.Errorf("Count(%q) = %+v does not cover every rune", c.in, got)
		}
		if got != c.want {
			t.Errorf("Count(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
	if got := Count("abcd").Add(Count("я")).Raw(); got != 2 { // 4/4 + 1/2 = 1.5 → 2
		t.Errorf("Raw = %d, want 2", got)
	}
}

func TestCalibrate(t *testing.T) {
	t.Parallel()
	if f := NewEstimator(nil, 0).Factor(); f != 1 {
		t.Errorf("no samples: factor %v, want 1", f)
	}
	clamp := []struct {
		s    Sample
		want float64
	}{
		{Sample{Real: 10, Est: 100}, CalibrationMin},
		{Sample{Real: 100, Est: 10}, CalibrationMax},
		{Sample{Real: 80, Est: 100}, 0.8},
	}
	for _, c := range clamp {
		if f := NewEstimator([]Sample{c.s}, 0).Factor(); math.Abs(f-c.want) > 1e-12 {
			t.Errorf("%+v: factor %v, want %v", c.s, f, c.want)
		}
	}
	// EMA: newest sample weighs 0.3; invalid rows are skipped.
	e := NewEstimator([]Sample{{Real: 100, Est: 100}, {Real: 0, Est: 50}, {Real: 50, Est: 0}, {Real: 60, Est: 100}}, 0)
	if want := 0.3*0.6 + 0.7*1.0; math.Abs(e.Factor()-want) > 1e-12 {
		t.Errorf("EMA factor %v, want %v", e.Factor(), want)
	}
	// Only the newest CalibrationWindow samples count: incremental Observe
	// equals re-deriving from the ledger window at session load.
	var all []Sample
	inc := NewEstimator(nil, 0)
	var last int64
	for i := range 3 * CalibrationWindow {
		s := Sample{Real: int64(50 + i%37), Est: 100}
		all = append(all, s)
		inc.Observe(s.Real, s.Est)
		last = s.Real
	}
	loaded := NewEstimator(all[len(all)-CalibrationWindow:], 0)
	if inc.Factor() != loaded.Factor() {
		t.Errorf("incremental %v ≠ loaded %v", inc.Factor(), loaded.Factor())
	}
	if got := inc.Estimate(Counts{Latin: 40}); got != last+int64(math.Ceil(10*inc.Factor())) {
		t.Errorf("Estimate = %d, want anchor %d + delta", got, last)
	}
}

// corpus returns the paragraphs of testdata/calibration/<lang>.txt with
// their o200k_base counts (o200k.tsv, verified by bench/budget
// TestCalibrationCorpusCounts).
func corpus(t *testing.T, lang string) (paras []string, real []int64) {
	t.Helper()
	dir := os.DirFS(filepath.Join("testdata", "calibration"))
	src, err := fs.ReadFile(dir, lang+".txt")
	if err != nil {
		t.Fatal(err)
	}
	paras = strings.Split(strings.TrimSpace(string(src)), "\n\n")
	f, err := dir.Open("o200k.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		cols := strings.Split(sc.Text(), "\t")
		if len(cols) != 3 || cols[0] != lang {
			continue
		}
		n, perr := strconv.ParseInt(cols[2], 10, 64)
		if perr != nil {
			t.Fatal(perr)
		}
		real = append(real, n)
	}
	if err := sc.Err(); err != nil || len(real) != len(paras) || len(paras) < 6 {
		t.Fatalf("%s: %d paragraphs, %d counts, %v", lang, len(paras), len(real), err)
	}
	return paras, real
}

// TestTokenEstimatorCalibration (ADR-058/067/101, acceptance F1, text
// corpus en/id/ru/ja): a session whose prompt grows by one paragraph per
// call. From the 4th call on, the COST-09 estimate (last real prompt +
// calibrated delta) is within 0.9–1.1 of the o200k_base size of the whole
// request; est_input_tokens = Raw of the whole request.
func TestTokenEstimatorCalibration(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{"en", "id", "ru", "ja"} {
		paras, real := corpus(t, lang)
		e := NewEstimator(nil, 0)
		var prompt Counts
		var promptReal int64
		for i, p := range paras {
			delta := Count(p)
			est := e.Estimate(delta)
			prompt, promptReal = prompt.Add(delta), promptReal+real[i]
			r := float64(est) / float64(promptReal)
			if i >= 3 && (r < 0.9 || r > 1.1) {
				t.Errorf("%s call %d: estimate/real %.3f (factor %.3f)", lang, i+1, r, e.Factor())
			}
			t.Logf("%s call %d: estimate/real %.3f, delta uncalibrated %.3f, factor %.3f",
				lang, i+1, r, float64(delta.Raw())/float64(real[i]), e.Factor())
			e.Observe(promptReal, prompt.Raw())
		}
	}
}

func TestCost(t *testing.T) {
	t.Parallel()
	p := &Price{InputPerMTok: 3_000_000, CachedInputPerMTok: 300_000, OutputPerMTok: 15_000_000, Version: "2026-10"}
	usd := 0.0123456
	neg, zero, nan := -1.0, 0.0, math.NaN()
	huge, maxUSD, inf := 1e14, float64(MaxProviderCostUSD), math.Inf(1)
	cases := []struct {
		u       Usage
		p       *Price
		want    int64 // -1 = nil
		src     CostSource
		wantErr bool
	}{
		{Usage{Input: 1000, CachedInput: 400, Output: 100, CostUSD: &usd}, p, 12346, CostProvider, false},
		{Usage{Input: 1000, CachedInput: 400, Output: 100}, p, 3420, CostPricingTable, false}, // 600×3 + 400×0.3 + 100×15
		{Usage{Input: 1, Output: 0}, &Price{InputPerMTok: 1}, 1, CostPricingTable, false},     // rounds up
		{Usage{Input: 10, CachedInput: 10}, &Price{InputPerMTok: 2_000_000}, 20, CostPricingTable, false},
		{Usage{Input: 10, CostUSD: &neg}, nil, -1, CostUnknown, false},
		{Usage{Input: 10}, nil, -1, CostUnknown, false},
		// SEC6-6: a priced route charges max(table, provider); a reseller
		// reporting zero/low/negative/NaN cannot undercut the contract.
		{Usage{Input: 1000, CachedInput: 400, Output: 100, CostUSD: &zero}, p, 3420, CostPricingTable, false},
		{Usage{Input: 1000, CachedInput: 400, Output: 100, CostUSD: &neg}, p, 3420, CostPricingTable, false},
		{Usage{Input: 1000, CachedInput: 400, Output: 100, CostUSD: &nan}, p, 3420, CostPricingTable, false},
		// An unpriced route keeps ADR-125: the provider cost is the charge.
		{Usage{Input: 10, CostUSD: &zero}, nil, 0, CostProvider, false},
		{Usage{Input: 1, CachedInput: 2}, p, -1, CostUnknown, true},
		{Usage{Output: -1}, p, -1, CostUnknown, true},
		// LED-2: implausible provider costs fail closed, never wrap negative.
		{Usage{Input: 1, CostUSD: &huge}, p, -1, CostUnknown, true},
		{Usage{Input: 1, CostUSD: &inf}, p, -1, CostUnknown, true},
		{Usage{Input: 1, CostUSD: &maxUSD}, p, 1_000_000_000_000, CostProvider, false},
		// LED-3: token bounds and checked arithmetic.
		{Usage{Input: MaxTokens + 1}, p, -1, CostUnknown, true},
		{Usage{Output: math.MaxInt64 / 2}, p, -1, CostUnknown, true},
		{Usage{Input: 4e12}, p, -1, CostUnknown, true},
		{Usage{Input: MaxTokens, Output: MaxTokens}, &Price{InputPerMTok: 9e6, OutputPerMTok: 9e6}, 18_000_000_000_000, CostPricingTable, false},
		{Usage{Input: MaxTokens}, &Price{InputPerMTok: 1e8}, -1, CostUnknown, true}, // 10^20 overflows uint64
		{Usage{Input: MaxTokens, Output: MaxTokens}, &Price{InputPerMTok: 9e6, OutputPerMTok: 9e6 + 1e7}, -1, CostUnknown, true},
		{Usage{Input: 1}, &Price{InputPerMTok: -1}, -1, CostUnknown, true},
	}
	for i, c := range cases {
		m, src, err := Cost(c.u, c.p)
		if (err != nil) != c.wantErr || src != c.src || (m == nil) != (c.want < 0) || m != nil && *m != c.want {
			t.Errorf("case %d: %v %s %v, want %d %s", i, m, src, err, c.want, c.src)
		}
		if c.wantErr && !errors.Is(err, ErrInvalidUsage) {
			t.Errorf("case %d: %v is not ErrInvalidUsage", i, err)
		}
	}
}

func TestAccount(t *testing.T) {
	t.Parallel()
	p := &Price{InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000, Version: "2026-10"}
	e := Entry{SessionID: "s", Purpose: "turn"}
	if err := e.Account(Usage{Input: 1000, Output: 10}, p, 900); err != nil || e.CostSource != "pricing_table" ||
		e.PricingVersion != "2026-10" || e.EstInputTokens != 900 || *e.CostMicros != 3150 {
		t.Errorf("Account = %+v, %v", e, err)
	}
	e = Entry{}
	if err := e.Account(Usage{Input: 5}, nil, 4); err != nil || e.CostMicros != nil || e.CostSource != "unknown" || e.PricingVersion != PricingNone {
		t.Errorf("unknown cost entry = %+v, %v", e, err)
	}
	// A provider-reported charge above the table wins and has no table
	// version; the table keeps its version only when it is the charge.
	high := 0.01
	e = Entry{}
	if err := e.Account(Usage{Input: 1000, Output: 10, CostUSD: &high}, p, 0); err != nil ||
		*e.CostMicros != 10_000 || e.CostSource != "provider" || e.PricingVersion != PricingNone {
		t.Errorf("provider charge entry = %+v, %v", e, err)
	}
	if err := e.Account(Usage{Input: -1}, nil, 0); !errors.Is(err, ErrInvalidUsage) {
		t.Errorf("invalid usage: %v", err)
	}
}

func BenchmarkCount(b *testing.B) {
	s := strings.Repeat("The quick brown fox. Быстрая лиса. 速い狐。", 100)
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	for b.Loop() {
		_ = Count(s)
	}
}
