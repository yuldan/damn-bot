package gateway

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Inbound quota defaults (SPEC §9.7, §22.1 P25/GAP-GW-02).
const (
	DefaultMaxParallelTurns       = 4
	DefaultIdentityQueue          = 5
	DefaultMessagesPerMinute      = 20
	DefaultTurnsPerDay            = 200
	DefaultGroupMessagesPerMinute = 30
	DefaultNoticeInterval         = time.Minute
)

// ErrInvalidQuota rejects a quota configuration or request the policy cannot
// express.
var ErrInvalidQuota = errors.New("gateway: invalid quota")

// Clock is the only time source of the quota windows; tests inject a fake so
// crossing a window never needs a sleep.
type Clock interface{ Now() time.Time }

// SystemClock reads the wall clock.
type SystemClock struct{}

// Now returns the current local time.
func (SystemClock) Now() time.Time { return time.Now() }

// QuotaOptions configures a Quota; a zero field takes its P25 default.
type QuotaOptions struct {
	MessagesPerMinute      int
	TurnsPerDay            int
	GroupMessagesPerMinute int
	NoticeInterval         time.Duration
	Clock                  Clock
}

func (o *QuotaOptions) fill() error {
	for _, f := range [...]struct {
		name  string
		value *int
		def   int
	}{
		{"messages per minute", &o.MessagesPerMinute, DefaultMessagesPerMinute},
		{"turns per day", &o.TurnsPerDay, DefaultTurnsPerDay},
		{"group messages per minute", &o.GroupMessagesPerMinute, DefaultGroupMessagesPerMinute},
	} {
		switch {
		case *f.value == 0:
			*f.value = f.def
		case *f.value < 0:
			return fmt.Errorf("%w: %s is negative", ErrInvalidQuota, f.name)
		}
	}
	if o.NoticeInterval == 0 {
		o.NoticeInterval = DefaultNoticeInterval
	}
	if o.NoticeInterval < 0 {
		return fmt.Errorf("%w: notice interval is negative", ErrInvalidQuota)
	}
	if o.Clock == nil {
		o.Clock = SystemClock{}
	}
	return nil
}

// QuotaRequest is the identity and chat dimension of one inbound message.
// Group selects the shared group-chat bucket.
type QuotaRequest struct {
	Platform string
	Identity string
	ChatID   string
	Group    bool
}

func (r *QuotaRequest) validate() error {
	for _, f := range [...]struct{ name, value string }{
		{"platform", r.Platform},
		{"identity", r.Identity},
		{"chat", r.ChatID},
	} {
		if f.value == "" {
			return fmt.Errorf("%w: %s id is required", ErrInvalidQuota, f.name)
		}
	}
	return nil
}

// LimitKind is the quota a message exhausted.
type LimitKind uint8

// Quota limits. LimitNone admits the message.
const (
	LimitNone LimitKind = iota
	LimitMessages
	LimitTurns
	LimitGroupMessages
)

// QuotaResult is Take's answer. Limit is LimitNone when admitted; RetryAfter
// and Reset say when a retry can succeed, and the Remaining fields report the
// quota left after this message. RemainingGroup is -1 for a non-group chat.
type QuotaResult struct {
	Limit             LimitKind
	RetryAfter        time.Duration
	Reset             time.Time
	RemainingMessages int
	RemainingTurns    int
	RemainingGroup    int
}

// Message is the static reply of a denial (GAP-GW-02 "limit (reset HH:MM)");
// it is empty for an admitted message.
func (r QuotaResult) Message() string {
	if r.Limit == LimitNone {
		return ""
	}
	return "limit (reset " + r.Reset.Format("15:04") + ")"
}

type identityQuota struct {
	messages []time.Time
	day      time.Time
	turns    int
	noticeAt time.Time
}

// Quota is the inbound quota of SPEC §9.7 (per identity 20 messages/minute and
// 200 turns/day; 30 messages/minute per group chat).
type Quota struct {
	mu     sync.Mutex
	opts   QuotaOptions
	ids    map[string]*identityQuota
	groups map[string][]time.Time
}

// NewQuota builds a quota policy; every zero option takes its P25 default.
func NewQuota(opts QuotaOptions) (*Quota, error) {
	if err := opts.fill(); err != nil {
		return nil, err
	}
	return &Quota{
		opts:   opts,
		ids:    make(map[string]*identityQuota),
		groups: make(map[string][]time.Time),
	}, nil
}

// Take consumes one message of req and reports whether it is admitted; a
// denied message consumes nothing.
func (q *Quota) Take(req QuotaRequest) (QuotaResult, error) {
	if err := req.validate(); err != nil {
		return QuotaResult{}, err
	}
	now := q.opts.Clock.Now()
	q.mu.Lock()
	defer q.mu.Unlock()
	id := q.entry(req.Platform, req.Identity)
	q.rollDay(id, now)
	id.messages = prune(id.messages, now.Add(-time.Minute))
	res := QuotaResult{
		RemainingMessages: q.opts.MessagesPerMinute - len(id.messages),
		RemainingTurns:    q.opts.TurnsPerDay - id.turns,
		RemainingGroup:    -1,
	}
	if len(id.messages) >= q.opts.MessagesPerMinute {
		return refuse(res, LimitMessages, id.messages[0].Add(time.Minute), now), nil
	}
	gkey := req.Platform + "\x00" + req.ChatID
	group := prune(q.groups[gkey], now.Add(-time.Minute))
	if len(group) == 0 {
		delete(q.groups, gkey)
	} else {
		q.groups[gkey] = group
	}
	if req.Group {
		res.RemainingGroup = q.opts.GroupMessagesPerMinute - len(group)
		if len(group) >= q.opts.GroupMessagesPerMinute {
			return refuse(res, LimitGroupMessages, group[0].Add(time.Minute), now), nil
		}
	}
	if id.turns >= q.opts.TurnsPerDay {
		return refuse(res, LimitTurns, nextDay(now), now), nil
	}
	id.messages = append(id.messages, now)
	id.turns++
	res.RemainingMessages--
	res.RemainingTurns--
	if req.Group {
		q.groups[gkey] = append(group, now)
		res.RemainingGroup--
	}
	return res, nil
}

// Notice reports whether the static "limit"/"antre" reply may be sent now and
// starts the one-minute window (max one static reply per identity per minute).
func (q *Quota) Notice(req QuotaRequest) bool {
	now := q.opts.Clock.Now()
	q.mu.Lock()
	defer q.mu.Unlock()
	id := q.entry(req.Platform, req.Identity)
	if !id.noticeAt.IsZero() && now.Sub(id.noticeAt) < q.opts.NoticeInterval {
		return false
	}
	id.noticeAt = now
	return true
}

func (q *Quota) entry(platform, identity string) *identityQuota {
	key := platform + "\x00" + identity
	id := q.ids[key]
	if id == nil {
		id = &identityQuota{}
		q.ids[key] = id
	}
	return id
}

func (q *Quota) rollDay(id *identityQuota, now time.Time) {
	day := startOfDay(now)
	if id.day.Equal(day) {
		return
	}
	id.day = day
	id.turns = 0
}

func refuse(res QuotaResult, k LimitKind, reset, now time.Time) QuotaResult {
	res.Limit = k
	res.Reset = reset
	res.RetryAfter = max(reset.Sub(now), 0)
	return res
}

// prune drops timestamps older than cutoff from the front of a sliding
// window; entries are appended in clock order.
func prune(window []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(window) && !window[i].After(cutoff) {
		i++
	}
	return window[i:]
}

func startOfDay(t time.Time) time.Time { return dayStart(t, 0) }

func nextDay(t time.Time) time.Time { return dayStart(t, 1) }

func dayStart(t time.Time, days int) time.Time {
	y, m, d := t.AddDate(0, 0, days).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
