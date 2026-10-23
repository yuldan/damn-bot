package archtest

// Matriks import internal (ENGINEERING §2.2.1, E-01/E-02/E-06/E-08), dibaca TestImportGraph.
// Kunci dan nilai = path paket relatif root modul. Panah yang tidak tertulis = dilarang;
// paket internal yang tidak ada di matriks = gagal (paket baru wajib menambah barisnya di sini
// bersama perubahan graf ENGINEERING §2.2.1). Paket yang belum dibangun sudah dicantumkan agar
// paket F1+ (loop, netx, provider, …) langsung terikat matriks.

// compositionRoot boleh mengimpor paket internal mana pun (E-08).
const compositionRoot = "cmd/damnbot"

// moduleWildcard menandai kunci "<induk>/*": setiap subpaket langsung <induk> (adapter
// gateway/<platform>), tidak termasuk <induk> sendiri.
const moduleWildcard = "/*"

// groupNetx = node NET "netx (+ netx/jsonrpc, netx/retry)" (ADR-100).
func groupNetx() []string {
	return []string{"internal/netx", "internal/netx/jsonrpc", "internal/netx/retry"}
}

// groupExec = node EXE: exec + exec/classify (ADR-081, satu baris SPEC §3.2.4).
func groupExec() []string { return []string{"internal/exec", "internal/exec/classify"} }

// moduleImports = E-06: modul lazy hanya mengimpor modules/host, netx (+ netx/jsonrpc), schema.
func moduleImports() []string {
	return append([]string{"internal/modules/host", "internal/schema"}, groupNetx()...)
}

// storeImports = storage konsumen internal/ctxdb/<consumer>store (ADR-074): primitif ctxdb
// beserta dependency ctxdb sendiri.
func storeImports() []string {
	return []string{"internal/ctxdb", "internal/loop/event", "internal/redact"}
}

// allowedImports mengembalikan matriks paket → impor internal yang diizinkan (produksi).
func allowedImports() map[string][]string {
	return map[string][]string{
		// Frontend (E-03: tanpa ctxdb, provider, exec, codetools).
		"internal/tui":     {"internal/loop", "internal/i18n"},
		"internal/acp":     append([]string{"internal/loop", "internal/i18n"}, groupNetx()...),
		"internal/web":     {"internal/loop", "internal/i18n"},
		"internal/gateway": {"internal/loop", "internal/modules/host", "internal/i18n"},

		// Modul lazy (E-06).
		"internal/mcp":                      moduleImports(),
		"internal/codeintel":                moduleImports(),
		"internal/gateway" + moduleWildcard: moduleImports(),

		// Core.
		"internal/loop":               {"internal/loop/event", "internal/boundary"}, // E-02
		"internal/plan":               {"internal/loop/event"},
		"internal/tools":              append([]string{"internal/schema", "internal/sandbox", "internal/codetools", "internal/redact", "internal/journal", "internal/budget", "internal/hooks", "internal/modules", "internal/modules/host", "internal/boundary"}, groupExec()...),
		"internal/provider":           append([]string{"internal/provider/registry", "internal/schema", "internal/budget"}, groupNetx()...),
		"internal/provider/registry":  nil,
		"internal/assemble":           {"internal/loop/event"},
		"internal/compaction":         {"internal/loop/event", "internal/budget"},
		"internal/memory":             {"internal/redact"},
		"internal/skills":             {"internal/schema"},
		"internal/journal":            {"internal/loop/event", "internal/sandbox"},
		"internal/budget":             nil,
		"internal/heal":               {"internal/ctxdb", "internal/config"},
		"internal/importer":           {"internal/ctxdb", "internal/memory", "internal/skills", "internal/config"},
		"internal/codetools":          {"internal/sandbox", "internal/boundary"},
		"internal/exec":               {"internal/exec/classify", "internal/sandbox", "internal/redact", "internal/boundary"},
		"internal/exec/classify":      nil,
		"internal/hooks":              groupExec(),
		"internal/modules":            {"internal/modules/host", "internal/boundary"},
		"internal/modules/host":       {"internal/schema", "internal/loop/event"},
		"internal/ctxdb":              {"internal/loop/event", "internal/redact"},
		"internal/ctxdb/ledgerstore":  storeImports(),
		"internal/ctxdb/memstore":     storeImports(),
		"internal/ctxdb/skillstore":   storeImports(),
		"internal/ctxdb/journalstore": append(storeImports(), "internal/journal", "internal/sandbox"),
		"internal/ctxdb/limitstore":   storeImports(),
		"internal/ctxdb/carrierstore": storeImports(),
		"internal/ctxdb/compactstore": append(storeImports(), "internal/compaction", "internal/ctxdb/ledgerstore"),
		"internal/ctxdb/gwstore":      storeImports(),
		"internal/config":             {"internal/schema"},
		"internal/netx":               {"internal/netx/jsonrpc", "internal/netx/retry", "internal/redact"},
		"internal/netx/jsonrpc":       {"internal/netx", "internal/netx/retry", "internal/redact"},
		"internal/netx/retry":         nil,

		// Leaf.
		"internal/loop/event": nil,
		"internal/schema":     nil,
		"internal/sandbox":    nil,
		"internal/redact":     nil,
		"internal/secret":     nil,
		"internal/i18n":       nil,
		"internal/boundary":   nil,

		// Test arsitektur dan helper test (lihat testOnlyPackages).
		"internal/archtest":              nil,
		"internal/e2e":                   {"internal/budget", "internal/ctxdb", "internal/ctxdb/ledgerstore", "internal/ctxdb/skillstore", "internal/loop/event"},
		"internal/testutil/timing":       nil,
		"internal/testutil/phase":        nil,
		"internal/provider/providertest": {"internal/provider"},
	}
}

// testOnlyPackages hanya boleh diimpor dari file _test.go (paket internal mana pun) atau dari
// paket test-only lain; kode produksi, termasuk cmd/damnbot, tidak pernah mengimpornya.
func testOnlyPackages() []string {
	return []string{"internal/testutil/timing", "internal/testutil/phase", "internal/provider/providertest", "internal/e2e"}
}
