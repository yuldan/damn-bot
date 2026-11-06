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
func groupExec() []string {
	return []string{"internal/exec", "internal/exec/classify", "internal/exec/code"}
}

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
	m := map[string][]string{
		// Frontend (E-03: tanpa ctxdb, provider, exec, codetools).
		"internal/tui":     {"internal/loop", "internal/i18n"},
		"internal/acp":     append([]string{"internal/loop", "internal/i18n"}, groupNetx()...),
		"internal/web":     {"internal/loop", "internal/i18n"},
		"internal/gateway": {"internal/loop", "internal/modules/host", "internal/i18n", "internal/cron"},
		// Adapter Telegram memakai renderer Telegram bersama (GAP-GW-04).
		"internal/gateway/telegram": append(moduleImports(), "internal/gateway/render"),

		// Modul daemon/observabilitas F4 (ADR-244): `obs` sudah diimplementasikan
		// (stdlib saja, tanpa impor internal) dan tetap tanpa edge; `cron` sudah punya
		// API (scheduler library, ADR-248) sehingga edge-nya ditentukan; `scorecard` masih kerangka.
		"internal/cron":          append([]string{"internal/redact"}, groupExec()...),
		"internal/cron/periodic": nil, // AUTO-029: stdlib only, driven by the serve ticker
		"internal/obs":           nil, "internal/scorecard": nil,
		// Modul lazy (E-06).
		"internal/mcp":         moduleImports(),
		"internal/mcp/catalog": nil, // signature verifier: stdlib only
		// Delegasi (F5-12, ADR-273): orchestration over the tool pipeline; its
		// tests exercise the codetools lease guard directly.
		"internal/delegate":  {"internal/tools", "internal/codetools", "internal/sandbox", "internal/redact"},
		"internal/codeintel": append(moduleImports(), "internal/codeintel/ts"),
		// Sibling parser/client packages: ts is stdlib-only, lsp reaches netx/jsonrpc.
		"internal/codeintel/ts": nil, "internal/codeintel/lsp": {"internal/netx/jsonrpc"},
		"internal/gateway" + moduleWildcard: moduleImports(),
		"internal/gwplat":                   {"internal/gateway", "internal/gateway/outq", "internal/gateway/render"},
		"internal/gwmatrix":                 append([]string{"internal/gateway", "internal/gateway/outq", "internal/gateway/render", "internal/gwmatrix/e2ee", "internal/gwplat"}, groupNetx()...),
		"internal/gwmatrix/e2ee":            nil,
		"internal/gwplat" + moduleWildcard:  append([]string{"internal/gateway", "internal/gateway/outq", "internal/gateway/render", "internal/gwplat"}, groupNetx()...),

		// Core.
		"internal/loop":               {"internal/loop/event", "internal/boundary"}, // E-02
		"internal/plan":               {"internal/loop/event"},
		"internal/tools":              append([]string{"internal/schema", "internal/sandbox", "internal/codetools", "internal/redact", "internal/journal", "internal/budget", "internal/hooks", "internal/modules", "internal/modules/host", "internal/boundary", "internal/cron"}, groupExec()...),
		"internal/provider":           append([]string{"internal/provider/auth", "internal/provider/credpool", "internal/provider/registry", "internal/schema", "internal/budget", "internal/media/preflight", "internal/media/video"}, groupNetx()...),
		"internal/provider/credpool":  nil,
		"internal/provider/responses": append([]string{"internal/provider", "internal/provider/credpool", "internal/provider/registry", "internal/budget"}, groupNetx()...),
		"internal/provider/anthropic": append([]string{"internal/provider", "internal/provider/credpool", "internal/provider/registry", "internal/budget"}, groupNetx()...),
		"internal/provider/gemini":    append([]string{"internal/provider", "internal/provider/credpool", "internal/provider/registry", "internal/budget", "internal/testutil/phase"}, groupNetx()...),
		// Provider transport auth (F5-03): SigV4, Google JWT, OAuth store.
		"internal/provider/auth":     {"internal/netx", "internal/secret"},
		"internal/provider/registry": nil,
		"internal/assemble":          {"internal/loop/event"},
		"internal/backup":            {"internal/ctxdb", "internal/secret"},
		"internal/doctor":            {"internal/ctxdb", "internal/gateway"},
		"internal/compaction":        {"internal/loop/event", "internal/budget"},
		"internal/memory":            {"internal/redact"},
		"internal/skills":            {"internal/schema"},
		"internal/journal":           {"internal/loop/event", "internal/sandbox"},
		"internal/budget":            nil,
		"internal/heal":              {"internal/ctxdb", "internal/config"},
		"internal/importer":          {"internal/ctxdb", "internal/ctxdb/importstore", "internal/memory", "internal/skills", "internal/config"},
		"internal/codetools":         {"internal/sandbox", "internal/boundary"},
		"internal/exec":              {"internal/exec/classify", "internal/sandbox", "internal/redact", "internal/boundary"},
		// F5 backends (ADR-277): transport over the exec contract, plus the Exec egress point.
		"internal/exec/backends": {"internal/exec", "internal/netx"},
		// F5 execute_code contract (ADR-296): interpreter runner over exec.Backend.
		"internal/exec/code":           {"internal/exec", "internal/netx"},
		"internal/exec/classify":       nil,
		"internal/hooks":               append(groupExec(), "internal/redact"),
		"internal/modules":             {"internal/modules/host", "internal/boundary"},
		"internal/modules/host":        {"internal/schema", "internal/loop/event"},
		"internal/ctxdb":               {"internal/loop/event", "internal/redact"},
		"internal/ctxdb/ledgerstore":   storeImports(),
		"internal/ctxdb/memstore":      storeImports(),
		"internal/ctxdb/skillstore":    storeImports(),
		"internal/ctxdb/journalstore":  append(storeImports(), "internal/journal", "internal/sandbox"),
		"internal/ctxdb/usagestore":    {"internal/ctxdb", "internal/ctxdb/ledgerstore"},
		"internal/ctxdb/limitstore":    storeImports(),
		"internal/ctxdb/importstore":   nil,
		"internal/ctxdb/carrierstore":  storeImports(),
		"internal/ctxdb/compactstore":  append(storeImports(), "internal/compaction", "internal/ctxdb/ledgerstore"),
		"internal/ctxdb/gwstore":       storeImports(),
		"internal/ctxdb/mcpstore":      storeImports(),
		"internal/ctxdb/mcpservestore": storeImports(),
		"internal/ctxdb/cronstore":     storeImports(),
		"internal/config":              {"internal/schema", "internal/toolset", "internal/update"},
		"internal/update":              {"internal/netx"},
		"internal/toolset":             nil, // pure composition data (ADR-139): no internal deps
		"internal/netx":                {"internal/netx/jsonrpc", "internal/netx/retry", "internal/redact"},
		"internal/netx/jsonrpc":        {"internal/netx", "internal/netx/retry", "internal/redact"},
		"internal/netx/retry":          nil,

		// Leaf.
		"internal/loop/event": nil, "internal/schema": nil, "internal/sandbox": nil,
		"internal/redact": nil, "internal/secret": nil, "internal/i18n": nil, "internal/boundary": nil,

		// Test arsitektur dan helper test (lihat testOnlyPackages).
		"internal/archtest": nil,

		// Test-only fixture replay of the catalog (F5 provider parity): every row is
		// driven through the four wired adapters; see testOnlyPackages.
		"internal/provider/replay": {"internal/provider", "internal/provider/anthropic", "internal/provider/gemini", "internal/provider/registry", "internal/provider/responses", "internal/netx", "internal/netx/retry"},

		"internal/e2e":                   {"internal/boundary", "internal/budget", "internal/ctxdb", "internal/ctxdb/journalstore", "internal/ctxdb/ledgerstore", "internal/ctxdb/skillstore", "internal/gateway/telegram", "internal/journal", "internal/loop/event", "internal/netx", "internal/secret", "internal/voice"},
		"internal/integration":           {"internal/ctxdb", "internal/ctxdb/memstore", "internal/ctxdb/compactstore", "internal/ctxdb/skillstore", "internal/ctxdb/carrierstore", "internal/compaction", "internal/backup", "internal/secret", "internal/redact", "internal/loop/event"},
		"internal/testutil":              nil,
		"internal/testutil/timing":       nil,
		"internal/testutil/phase":        nil,
		"internal/provider/providertest": {"internal/provider"},
	}
	f6ModuleImports(m)
	return m
}

// f6ModuleImports mendaftarkan baris modul F6 (ADR-310). SPEC §3.2.4 `web`
// dibangun sebagai internal/webtools karena internal/web dipakai Web UI F8;
// semua modul F6 lazy (E-06) dan edge-nya ditambah per slice.
func f6ModuleImports(m map[string][]string) {
	m["internal/webtools"] = []string{"internal/modules/host", "internal/netx"}
	m["internal/browser"] = []string{"internal/modules/host", "internal/netx", "internal/browser/cdp"}
	// Browser leaf (ADR-309/ADR-313): coder/websocket framing; netx supplies the loopback client (E-05).
	m["internal/browser/cdp"] = []string{"internal/netx"}
	m["internal/media"] = []string{"internal/modules/host", "internal/netx", "internal/media/doc", "internal/media/preflight", "internal/media/gen", "internal/media/video", "internal/media/cache"}
	// Daun media (ADR-309): document parser, resize core, generation core.
	m["internal/media/doc"] = nil
	m["internal/media/preflight"] = nil
	// Video container header probe (TOOL-081/MEDIA-014): standard library only.
	m["internal/media/video"] = nil
	// Cache/repair gateway (MEDIA-030): module host store, stdlib only.
	m["internal/media/cache"] = []string{"internal/modules/host"}
	m["internal/media/gen"] = []string{"internal/modules/host"}
	// Voice root (ADR-319): host seam, netx TTS egress, and the three leaves.
	// Daun voice (ADR-309, F6-V1): provider contracts and implementations.
	// The root aliases their types and imports all three; leaves import only
	// the standard library.
	m["internal/voice"] = []string{"internal/modules/host", "internal/netx", "internal/voice/stt", "internal/voice/tts", "internal/voice/wake"}
	m["internal/voice/stt"] = nil
	m["internal/voice/tts"] = nil
	m["internal/voice/wake"] = nil
	m["internal/computeruse"] = []string{"internal/modules/host", "internal/netx"}
}

// testOnlyPackages hanya boleh diimpor dari file _test.go (paket internal mana pun) atau dari
// paket test-only lain; kode produksi, termasuk cmd/damnbot, tidak pernah mengimpornya.
func testOnlyPackages() []string {
	return []string{"internal/testutil", "internal/testutil/timing", "internal/testutil/phase", "internal/provider/providertest", "internal/provider/replay", "internal/e2e", "internal/integration"}
}
