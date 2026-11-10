// Package memoryproviders is the closed registry and shared contract of the
// external memory providers (SPEC §9.10, MEM-005/008/009, ADR-340).
//
// The registry is a data table, not a plugin loader: config may only name a
// provider compiled into the binary. ByteRover and RetainDB are not ported
// (GAP-PROV-05, NP-21/ADR-033) and are rejected with a typed error instead of
// being discovered or silently ignored.
//
// Every operation is bounded: 1.5 s per call, 800 tokens of recalled content
// (the same cap as the assembler retrieval tier), and 1 MiB per uploaded item.
// Provider content is untrusted data: [Snippet.Wrapped] escapes the closing
// boundary tag, and callers that feed the assembler pass the raw Text so the
// retrieval tier can apply its own escaping and wrapper.
package memoryproviders
