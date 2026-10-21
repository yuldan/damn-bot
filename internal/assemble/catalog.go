package assemble

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Catalog caps from SPEC §5.2 and §6. CatalogLineMax bounds one rendered
// group line ("- <name>: <summary>", ADR-119 butir 7) in [Deps.Tokens] units.
const (
	CapCatalog       = 600
	CatalogMaxGroups = 24
	CatalogLineMax   = 15
)

// catalogHeader opens the deferred catalog that stable rule 3 refers to
// ("the deferred catalog below"). It is part of the catalog tier and its cap.
const catalogHeader = "Deferred tool groups (tool_search, then tool_invoke):"

// CatalogGroup is one enabled module's catalog contribution, mapped by the
// composition root from the module registry (ADR-119, ADR-121): Name is
// modules.Descriptor.Group and Summary is modules.Descriptor.Catalog, the
// registry-validated one-line list of the group's deferred tools (≤ 15 tokens).
// A Summary that already starts with "- <Name>:" is accepted as is.
type CatalogGroup struct {
	Name    string
	Summary string
}

// Catalog renders the deferred catalog tier. It returns "" for an empty
// catalog, so the stable tier's rule 3 is dropped with it.
//
// Rendering is a pure function of the group set (P15, ADR-056): names and
// summaries are scrubbed and flattened to one line without control or format
// runes, groups are sorted byte-wise by name, groups sharing a name are merged
// into one line (summaries sorted, joined with "; "), and no usage statistic
// is consulted. A group line above [CatalogLineMax] tokens is never cut (a cut
// can forge a tool name): the group is left to tool_search and counted in the
// single overflow line of SPEC §5.2, as is the sorted tail beyond
// [CatalogMaxGroups] groups or [CapCatalog] tokens.
func (a *Assembler) Catalog(groups []CatalogGroup) string {
	lines := a.catalogLines(groups)
	if len(lines) == 0 {
		return ""
	}
	groupCount := len(lines)
	lines = slices.DeleteFunc(lines, func(l string) bool { return a.tokens.Count(l) > CatalogLineMax })
	oversized := groupCount - len(lines)
	for limit := min(len(lines), CatalogMaxGroups); limit >= 0; limit-- {
		text := renderCatalog(lines[:limit], len(lines)-limit+oversized)
		if a.tokens.Count(text) <= CapCatalog {
			return text
		}
	}
	return ""
}

// catalogLines returns one "- <name>: <summary>" line per distinct group
// name, sorted by name.
func (a *Assembler) catalogLines(groups []CatalogGroup) []string {
	byName := make(map[string][]string, len(groups))
	for _, g := range groups {
		name := oneLine(a.scrub(g.Name))
		if name == "" {
			continue
		}
		summary := oneLine(a.scrub(g.Summary))
		summary = strings.TrimSpace(strings.TrimPrefix(summary, "- "+name+":"))
		byName[name] = append(byName[name], summary)
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	slices.Sort(names)
	lines := make([]string, len(names))
	for i, name := range names {
		summaries := slices.DeleteFunc(byName[name], func(s string) bool { return s == "" })
		slices.Sort(summaries)
		line := "- " + name
		if s := strings.Join(slices.Compact(summaries), "; "); s != "" {
			line += ": " + s
		}
		lines[i] = line
	}
	return lines
}

func renderCatalog(lines []string, omitted int) string {
	var b strings.Builder
	b.WriteString(catalogHeader)
	for _, l := range lines {
		b.WriteByte('\n')
		b.WriteString(l)
	}
	if omitted > 0 {
		b.WriteString("\n- +" + strconv.Itoa(omitted) + " more groups: tool_search")
	}
	return b.String()
}

// oneLine flattens s to one prompt line: every whitespace run, newlines
// included, becomes one space and control or format runes are dropped (see
// [lineRune]), so caller text can neither add prompt structure nor hide text.
func oneLine(s string) string { return strings.Join(strings.Fields(strings.Map(lineRune, s)), " ") }

// lineRune maps any Unicode space (line breaks, U+2028) to ' ' and drops C0/C1
// controls (ESC) and format runes (bidi overrides and isolates such as
// U+202E, zero-width marks), which can reorder or hide text in a prompt, log
// or review (REVIEW-F2-SEC4 ASM4-5).
func lineRune(r rune) rune {
	switch {
	case unicode.IsSpace(r):
		return ' '
	case unicode.In(r, unicode.Cc, unicode.Cf):
		return -1
	}
	return r
}
