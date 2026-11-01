package credpool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func baseTime() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) }

func pool(t *testing.T, ids ...string) *Pool {
	t.Helper()
	creds := make([]Credential, len(ids))
	for i, id := range ids {
		creds[i] = NewCredential(id, "sk-"+id+"-secret")
	}
	p, err := New(creds)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewRejectsInvalidPools(t *testing.T) {
	t.Parallel()
	cases := map[string][]Credential{
		"empty":       nil,
		"too many":    make([]Credential, MaxCredentials+1),
		"empty id":    {NewCredential("", "x")},
		"empty value": {NewCredential("a", "")},
		"duplicate":   {NewCredential("a", "x"), NewCredential("A", "y")},
		"bad id":      {NewCredential("a b", "x")},
	}
	for name, creds := range cases {
		if _, err := New(creds); !errors.Is(err, ErrPool) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := New([]Credential{NewCredential("a", "x")}); err != nil {
		t.Fatal(err)
	}
}

func TestRoundRobinAndLeastRecentFailure(t *testing.T) {
	t.Parallel()
	p := pool(t, "a", "b", "c")
	for i, want := range []string{"a", "b", "c", "a"} {
		got, ok := p.Acquire(baseTime())
		if !ok || got.ID != want {
			t.Fatalf("acquire %d = %q/%t, want %q", i, got.ID, ok, want)
		}
	}
	// b failed early; once its cooldown ends it is the least recently failed
	// and is preferred over a credential used a moment ago.
	two := pool(t, "a", "b")
	if d := two.Fail("b", ReasonAuth, 0, baseTime()); d != authCooldownBase {
		t.Fatalf("cooldown = %s", d)
	}
	later := baseTime().Add(31 * time.Second)
	for i, want := range []string{"a", "b", "a"} {
		got, ok := two.Acquire(later)
		if !ok || got.ID != want {
			t.Fatalf("acquire %d = %q/%t, want %q", i, got.ID, ok, want)
		}
	}
}

func TestCooldownAndExhausted(t *testing.T) {
	t.Parallel()
	p := pool(t, "a", "b")
	if d := p.Fail("a", ReasonAuth, 0, baseTime()); d != authCooldownBase {
		t.Fatalf("auth cooldown = %s", d)
	}
	if d := p.Fail("b", ReasonRateLimited, 0, baseTime()); d != DefaultRateCooldown {
		t.Fatalf("rate cooldown = %s", d)
	}
	if c, ok := p.Acquire(baseTime()); ok {
		t.Fatalf("acquired %q while every credential cools down", c.ID)
	}
	if got := p.NextAvailable(baseTime()); got != authCooldownBase {
		t.Fatalf("next available = %s", got)
	}
	err := &ExhaustedError{Provider: "fake", Count: p.Len(), RetryAfter: p.NextAvailable(baseTime())}
	if !errors.Is(err, ErrExhausted) || !strings.Contains(err.Error(), "credential_exhausted") ||
		!strings.Contains(err.Error(), "provider fake") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("exhausted error = %q", err)
	}
	if c, ok := p.Acquire(baseTime().Add(authCooldownBase)); !ok || c.ID != "a" {
		t.Fatalf("after cooldown: %q/%t", c.ID, ok)
	}
}

func TestCooldownBounds(t *testing.T) {
	t.Parallel()
	p := pool(t, "a")
	now := baseTime()
	want := []time.Duration{
		30 * time.Second, 60 * time.Second, 120 * time.Second, 240 * time.Second,
		480 * time.Second, MaxCooldown, MaxCooldown,
	}
	for i, w := range want {
		got := p.Fail("a", ReasonAuth, 0, now)
		if got != w {
			t.Fatalf("failure %d = %s, want %s", i+1, got, w)
		}
		now = now.Add(got)
	}
	rate := pool(t, "a")
	for _, tc := range []struct {
		hint, want time.Duration
	}{
		{0, DefaultRateCooldown},
		{5 * time.Second, 5 * time.Second},
		{time.Millisecond, MinCooldown},
		{time.Hour, MaxCooldown},
	} {
		if got := rate.Fail("a", ReasonRateLimited, tc.hint, baseTime()); got != tc.want {
			t.Errorf("retry-after %s = %s, want %s", tc.hint, got, tc.want)
		}
	}
	if got := rate.Fail("a", Reason("other"), 0, baseTime()); got != 0 {
		t.Errorf("unknown reason cooldown = %s", got)
	}
	if got := rate.Fail("nope", ReasonAuth, 0, baseTime()); got != 0 {
		t.Errorf("unknown id cooldown = %s", got)
	}
}

func TestSuccessClearsCooldown(t *testing.T) {
	t.Parallel()
	p := pool(t, "a")
	p.Fail("a", ReasonAuth, 0, baseTime())
	p.Success("a", baseTime())
	st := p.Status(baseTime())[0]
	if !st.Available || st.Cooldown != 0 || st.Failures != 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestNoValueInRenderedForms(t *testing.T) {
	t.Parallel()
	p := pool(t, "a")
	c, _ := p.Acquire(baseTime())
	forms := []string{
		fmt.Sprint(c),
		fmt.Sprintf("%v", c),
		fmt.Sprintf("%+v", c),
		fmt.Sprintf("%#v", c),
		c.String(),
		fmt.Sprintf("%q", c),
		fmt.Sprint(p),
		fmt.Sprintf("%+v", p),
		c.GoString(),
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	forms = append(forms, string(b))
	for _, form := range forms {
		if strings.Contains(form, c.Secret()) {
			t.Fatalf("credential value leaked in %q", form)
		}
	}
	if !strings.Contains(forms[0], "credential:a") {
		t.Fatalf("identity missing from %q", forms[0])
	}
}

func TestConcurrentRotation(t *testing.T) {
	t.Parallel()
	p := pool(t, "a", "b", "c")
	allowed := map[string]bool{"a": true, "b": true, "c": true}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 200 {
				c, ok := p.Acquire(baseTime().Add(time.Duration(i) * time.Millisecond))
				if !ok {
					continue
				}
				if !allowed[c.ID] || c.Secret() != "sk-"+c.ID+"-secret" {
					t.Errorf("goroutine %d got %q", g, c.ID)
					return
				}
				switch (g + i) % 4 {
				case 0:
					p.Fail(c.ID, ReasonAuth, 0, baseTime())
				case 1:
					p.Fail(c.ID, ReasonRateLimited, time.Second, baseTime())
				case 2:
					p.Success(c.ID, baseTime())
				default:
					_ = p.Status(baseTime())
				}
			}
		})
	}
	wg.Wait()
}
