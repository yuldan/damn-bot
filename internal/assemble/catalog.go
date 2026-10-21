package assemble

import (
	"slices"
	"strconv"
	"strings"
)

// Catalog caps from SPEC §5.2 and §6.
const (
	CapCatalog       = 600
	CatalogMaxGroups = 24
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
// Rendering is a pure function of the group set (P15, ADR-056): groups are
// sorted byte-wise by name, groups sharing a name are merged into one line
// (summaries sorted, joined with "; "), whitespace is flattened so a summary
// cannot add prompt structure, and no usage statistic is consulted. More than
// [CatalogMaxGroups] groups or a render above [CapCatalog] tokens trims the
// sorted tail and replaces it with the single overflow line of SPEC §5.2.
func Catalog(groups []CatalogGroup, c Counter) string {
	lines := catalogLines(groups)
	if len(lines) == 0 {
		return ""
	}
	for limit := min(len(lines), CatalogMaxGroups); limit > 0; limit-- {
		text := renderCatalog(lines[:limit], len(lines)-limit)
		if c.Count(text) <= CapCatalog {
			return text
		}
	}
	return ""
}

// catalogLines returns one "- <name>: <summary>" line per distinct group
// name, sorted by name.
func catalogLines(groups []CatalogGroup) []string {
	byName := make(map[string][]string, len(groups))
	for _, g := range groups {
		name := oneLine(g.Name)
		if name == "" {
			continue
		}
		summary := oneLine(g.Summary)
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
	out := catalogHeader + "\n" + strings.Join(lines, "\n")
	if omitted > 0 {
		out += "\n- +" + strconv.Itoa(omitted) + " more groups: tool_search"
	}
	return out
}

// oneLine collapses every whitespace run, including newlines, to one space.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
