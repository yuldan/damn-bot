// Package skills is the SKILL.md metadata and retrieval of SPEC §4.8: a
// compatible file format (front matter name/description + body), the 8.192 B
// cap, quarantine of agent/hub/import content, and the ≤ 3-skill prompt offer
// used by the context tier.
//
// Storage lives in internal/ctxdb/skillstore and reaches this package through
// the small [Store] interface (E-09, ADR-074). The package owns no SQL and no
// global state: the threat scanner is injected ([Scanner]), so the package
// does not import internal/memory (E-01).
package skills
