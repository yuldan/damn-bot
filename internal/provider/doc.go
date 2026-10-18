// Package provider is the LLM client of the core (SPEC §4.1): the
// OpenAI-compatible chat adapter (F1), route selection with model fallback
// (P7), typed error classes (P6), and the zero-allocation streaming delta
// decoder (E-36). HTTP, retry, and circuit breaking come from netx.
package provider
