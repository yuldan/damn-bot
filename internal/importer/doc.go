// Package importer imports a Hermes home into the Damn-Bot stores without the
// CLI (SPEC §9.15). It enumerates five sources, imports everything it can map,
// and reports every unmapped or rejected item with a reason; nothing is lost
// silently.
//
// The package is a library: callers supply file/directory readers, a [Sink]
// for the target stores and a [Secrets] seam for the keychain. Secrets are only
// ever written through that seam; reports, config values and error messages
// carry names and keychain references, never values.
package importer
