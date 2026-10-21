// Package memory is the structured memory of SPEC §4.8 and §11.2: typed
// records with provenance, an append-only decision ledger, quarantine of
// untrusted writes, redaction before storage, and the retrieval API used by
// the context tier and the recall tool.
//
// The package owns policy, not SQL. It talks to storage through the small
// [Store] interface (E-09, ADR-074); internal/ctxdb/memstore implements it and
// cmd/damnbot wires the two together. The append-only tables
// (memory, memory_decisions, memory_pins) mean "update" and "delete" are new
// decision rows, never in-place mutation; physical removal stays a purge
// concern (P9). There is no package-level state: every [Service] carries its
// own redactor and clock.
package memory
