package assemble

import (
	"strconv"
	"strings"
	"time"
)

// Volatile caps from SPEC §6 and ADR-067. The sub-caps sum to [CapVolatile].
const (
	CapVolatile   = 100
	CapVolTime    = 12
	CapVolBudget  = 15
	CapVolTodo    = 30
	CapVolNames   = 20
	CapVolNotices = 23
	MaxOpenTodos  = 3
	TodoTextChars = 40
)

// Todo is one active plan item. Only pending and in_progress items render.
type Todo struct {
	Text   string
	Status string
}

// VolatileInput is the turn-start data of the volatile tier. It is rendered
// once by [Assembler.Freeze] and never changes during the turn (ADR-067).
type VolatileInput struct {
	Now time.Time
	// Loc is the identity's IANA zone; nil renders UTC (PROV-03).
	Loc             *time.Location
	RemainingTokens int64
	RemainingUSD    float64
	Todos           []Todo
	NewNames        []string
	Notices         []string
}

// Volatile is the rendered volatile tier, one component per sub-cap.
type Volatile struct {
	Time    string
	Budget  string
	Todo    string
	Names   string
	Notices string
}

// String joins the non-empty components. It is byte-stable for a frozen
// [Turn].
func (v Volatile) String() string {
	parts := []string{v.Time, v.Budget, v.Todo, v.Names, v.Notices}
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}

// Turn is one frozen volatile tier. The rendered bytes are computed once and
// reused for every iteration of the turn, so the request prefix does not move.
type Turn struct {
	volatile Volatile
	text     string
}

// Freeze renders the volatile tier for one turn (ADR-067 butir 2).
func (a *Assembler) Freeze(in *VolatileInput) *Turn {
	if in == nil {
		return &Turn{}
	}
	v := Volatile{
		Time:    renderTime(in.Now, in.Loc, a.tokens),
		Budget:  renderBudget(in, a.tokens),
		Todo:    a.renderTodos(in.Todos),
		Names:   a.renderNames(in.NewNames),
		Notices: a.renderNotices(in.Notices),
	}
	text := v.String()
	if a.tokens.Count(text) > CapVolatile {
		text = dropVolatile(&v, a)
	}
	return &Turn{volatile: v, text: text}
}

// Volatile returns the frozen components.
func (t *Turn) Volatile() Volatile { return t.volatile }

// String returns the frozen bytes. A nil Turn renders "".
func (t *Turn) String() string {
	if t == nil {
		return ""
	}
	return t.text
}

func renderTime(now time.Time, loc *time.Location, c Counter) string {
	if now.IsZero() {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	return clip("now: "+now.In(loc).Format("2006-01-02 15:04 MST Mon"), CapVolTime, "", c)
}

func renderBudget(in *VolatileInput, c Counter) string {
	if in.RemainingTokens == 0 && in.RemainingUSD == 0 {
		return ""
	}
	text := "left: " + itoa64(in.RemainingTokens) + " tok, $" + strconv.FormatFloat(in.RemainingUSD, 'f', 2, 64)
	return clip(text, CapVolBudget, "", c)
}

func (a *Assembler) renderTodos(todos []Todo) string {
	var b strings.Builder
	b.WriteString("todo:")
	open, shown := 0, 0
	for _, t := range todos {
		if t.Status != "pending" && t.Status != "in_progress" {
			continue
		}
		open++
		if shown == MaxOpenTodos {
			continue
		}
		shown++
		b.WriteString("\n- ")
		b.WriteString(truncateRunes(a.scrub(t.Text), TodoTextChars))
	}
	if open == 0 {
		return ""
	}
	if open > shown {
		b.WriteString("\n+")
		b.WriteString(itoa(open - shown))
		b.WriteString(" more")
	}
	return clip(b.String(), CapVolTodo, "\n[truncated]", a.tokens)
}

func (a *Assembler) renderNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("new: ")
	omitted := 0
	for i, name := range names {
		piece := a.scrub(name)
		if i > 0 {
			piece = ", " + piece
		}
		if a.tokens.Count(b.String()+piece) > CapVolNames-2 {
			omitted = len(names) - i
			break
		}
		b.WriteString(piece)
	}
	if omitted > 0 {
		b.WriteString(" +")
		b.WriteString(itoa(omitted))
	}
	return clip(b.String(), CapVolNames, "", a.tokens)
}

func (a *Assembler) renderNotices(notices []string) string {
	if len(notices) == 0 {
		return ""
	}
	text := a.scrub(notices[0])
	if len(notices) > 1 {
		text += " (+" + itoa(len(notices)-1) + " more)"
	}
	return clip(text, CapVolNotices, "", a.tokens)
}

// dropVolatile trims the least important components until the tier fits
// [CapVolatile]. Time and budget survive longest; the whole tier is a hard
// cap.
func dropVolatile(v *Volatile, a *Assembler) string {
	for _, field := range []*string{&v.Notices, &v.Names, &v.Todo, &v.Budget} {
		*field = ""
		if a.tokens.Count(v.String()) <= CapVolatile {
			return v.String()
		}
	}
	return clip(v.String(), CapVolatile, "", a.tokens)
}
