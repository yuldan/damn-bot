package assemble

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// Catalog caps from SPEC §5.2 and §6.
const (
	CapCatalog       = 600
	CatalogMaxGroups = 24
)

// CatalogGroup is one deferred module group: a group name and the tool names
// it exposes. Groups are rendered byte-wise sorted by Name (SPEC §5.2).
type CatalogGroup struct {
	Name  string
	Tools []string
}

// Catalog renders the deferred catalog. It returns "" for an empty catalog.
// Module groups are sorted alphabetically; more than [CatalogMaxGroups] or a
// render above [CapCatalog] tokens trims the sorted tail and replaces it with
// the single overflow line of SPEC §5.2.
func Catalog(groups []CatalogGroup, c Counter) string {
	if len(groups) == 0 {
		return ""
	}
	sorted := make([]CatalogGroup, len(groups))
	copy(sorted, groups)
	sortCatalog(sorted)
	lines := make([]string, len(sorted))
	for i, g := range sorted {
		lines[i] = "- " + g.Name + ": " + strings.Join(g.Tools, ", ")
	}
	limit := min(len(lines), CatalogMaxGroups)
	for limit > 0 {
		omitted := len(lines) - limit
		text := renderCatalog(lines[:limit], omitted)
		if c.Count(text) <= CapCatalog {
			return text
		}
		limit--
	}
	return ""
}

func renderCatalog(lines []string, omitted int) string {
	out := strings.Join(lines, "\n")
	if omitted > 0 {
		out += "\n- +" + strconv.Itoa(omitted) + " more groups: tool_search"
	}
	return out
}

func sortCatalog(groups []CatalogGroup) {
	slices.SortFunc(groups, func(a, b CatalogGroup) int { return cmp.Compare(a.Name, b.Name) })
}
