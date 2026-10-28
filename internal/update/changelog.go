package update

import (
	"regexp"
	"slices"
	"strings"
)

// changelogSection is one `## ...` block of CHANGELOG.md. Version is empty for
// the Unreleased block.
type changelogSection struct {
	version string
	body    string
	raw     string
}

// versionHeading matches Keep a Changelog version headings:
//
//	## [1.2.3] - 2026-10-28
//	## [1.2.3]
//	## 1.2.3 - 2026-10-28
var versionHeading = regexp.MustCompile(`^##\s+\[?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)\]?(?:\s|$)`)

// parseChangelog splits a changelog into `## ` sections, keeping file order.
func parseChangelog(text string) []changelogSection {
	lines := strings.Split(text, "\n")
	var out []changelogSection
	var cur *changelogSection
	flush := func() {
		if cur == nil {
			return
		}
		cur.raw = strings.TrimRight(cur.raw, "\n")
		cur.body = strings.TrimRight(cur.body, "\n")
		out = append(out, *cur)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			flush()
			cur = &changelogSection{}
			if m := versionHeading.FindStringSubmatch(line); m != nil {
				cur.version = m[1]
			}
			cur.raw = line
			continue
		}
		if cur == nil {
			continue
		}
		cur.raw += "\n" + line
		cur.body += "\n" + line
	}
	flush()
	return out
}

// sliceChangelog returns the version entries with current < version <= target
// (GAP-INF-04). A legacy changelog without version headings, or a non-semver
// installed version, keeps the whole text.
func sliceChangelog(text, current, target string) string {
	cur, curOK := parseSemver(current)
	tgt, tgtOK := parseSemver(target)
	if !curOK || !tgtOK {
		return text
	}
	sections := parseChangelog(text)
	hasVersion := false
	for _, s := range sections {
		if s.version != "" {
			hasVersion = true
			break
		}
	}
	if !hasVersion {
		return text
	}
	var picked []string
	for _, s := range sections {
		v, ok := parseSemver(s.version)
		if s.version == "" || !ok {
			continue
		}
		if compareSemver(v, cur) > 0 && compareSemver(v, tgt) <= 0 {
			picked = append(picked, s.raw)
		}
	}
	if len(picked) == 0 {
		return ""
	}
	return strings.Join(slices.Compact(picked), "\n\n")
}

// changelogBody returns the body of one version section, or "" when absent.
func changelogBody(text, version string) string {
	for _, s := range parseChangelog(text) {
		if s.version == version {
			return s.body
		}
	}
	return ""
}

// hasSubsection reports whether body contains the `### name` heading.
func hasSubsection(body, name string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), "### "+name) {
			return true
		}
	}
	return false
}

// subsection returns the text under `### name` up to the next `### `.
func subsection(body, name string) string {
	var out strings.Builder
	in := false
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "### ") {
			in = strings.EqualFold(strings.TrimSpace(line), "### "+name)
			continue
		}
		if in {
			out.WriteString(line)
			out.WriteString("\n")
		}
	}
	return out.String()
}
