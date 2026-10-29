package render

import (
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// utf16Slice returns the substring of s addressed by UTF-16 offsets.
func utf16Slice(t *testing.T, s string, off, length int) string {
	t.Helper()
	u := utf16.Encode([]rune(s))
	if off < 0 || length <= 0 || off+length > len(u) {
		t.Fatalf("entity [%d,%d) di luar %d unit: %q", off, off+length, len(u), s)
	}
	return string(utf16.Decode(u[off : off+length]))
}

// checkMessages asserts every invariant a Telegram payload must satisfy.
func checkMessages(t *testing.T, lim Limits, msgs []Message) {
	t.Helper()
	lim = lim.norm()
	for i, m := range msgs {
		checkPlain(t, i, lim, m.Text)
		checkEntities(t, i, m)
	}
}

// checkPlain checks UTF-8 validity and the UTF-16 length of one message.
func checkPlain(t *testing.T, i int, lim Limits, text string) {
	t.Helper()
	if !utf8.ValidString(text) {
		t.Fatalf("pesan %d bukan UTF-8 valid: %q", i, text)
	}
	u := Units(text)
	if u > lim.MaxLen {
		t.Fatalf("pesan %d %d unit > %d: %q", i, u, lim.MaxLen, text)
	}
	if want := len(utf16.Encode([]rune(text))); u != want {
		t.Fatalf("pesan %d: Units=%d != utf16.Encode=%d", i, u, want)
	}
	if splitsSurrogate(text, u) {
		t.Fatalf("pesan %d memotong surrogate pair: %q", i, text)
	}
}

// checkEntities checks every entity range against the UTF-16 text.
func checkEntities(t *testing.T, i int, m Message) {
	t.Helper()
	u := Units(m.Text)
	for _, e := range m.Entities {
		if e.Offset < 0 || e.Length <= 0 || e.Offset+e.Length > u {
			t.Fatalf("pesan %d entity %+v di luar teks %q", i, e, m.Text)
		}
		if splitsSurrogate(m.Text, e.Offset) || splitsSurrogate(m.Text, e.Offset+e.Length) {
			t.Fatalf("pesan %d entity %+v memotong surrogate pair", i, e)
		}
		switch e.Type {
		case EntityBold, EntityItalic, EntityCode, EntityPre, EntityTextLink:
		default:
			t.Fatalf("pesan %d tipe entity tak dikenal %q", i, e.Type)
		}
	}
}

// splitsSurrogate reports whether UTF-16 offset off falls between a high and a
// low surrogate.
func splitsSurrogate(s string, off int) bool {
	u := utf16.Encode([]rune(s))
	return off > 0 && off < len(u) && u[off-1] >= 0xd800 && u[off-1] <= 0xdbff && u[off] >= 0xdc00 && u[off] <= 0xdfff
}

func entity(t *testing.T, m Message, typ string) Entity {
	t.Helper()
	for _, e := range m.Entities {
		if e.Type == typ {
			return e
		}
	}
	t.Fatalf("entity %q tidak ada di %+v", typ, m)
	return Entity{}
}

// TestFenceClosingCRLF is the control for EC-PLAT-009: a closing fence whose
// line ends in CRLF still closes the block.
func TestFenceClosingCRLF(t *testing.T) {
	src := "```go\r\nfmt.Println(1)\r\n```\r\nAfter\r\n"
	doc, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Kids) != 2 {
		t.Fatalf("blok = %d, want 2: %#v", len(doc.Kids), doc.Kids)
	}
	cb := doc.Kids[0]
	if cb.Kind != CodeBlock || cb.Info != "go" || strings.TrimSpace(cb.Kids[0].Text) != "fmt.Println(1)" {
		t.Fatalf("blok kode = %#v", cb)
	}
	if p := doc.Kids[1]; p.Kind != Paragraph || len(p.Kids) != 1 {
		t.Fatalf("paragraf setelah fence CRLF = %#v", p)
	}

	msgs := RenderTelegram(src, Limits{})
	checkMessages(t, Limits{}, msgs)
	e := entity(t, msgs[0], EntityPre)
	if e.Language != "go" {
		t.Errorf("bahasa pre = %q, want go", e.Language)
	}
	if strings.Contains(msgs[0].Text, "```") {
		t.Errorf("fence bocor sebagai teks: %q", msgs[0].Text)
	}
}

// TestInlineBackticksNotPre is the control for EC-PLAT-010: a triple backtick
// inside a line is an inline code span, never a fenced block.
func TestInlineBackticksNotPre(t *testing.T) {
	msgs := RenderTelegram("a ```b``` c", Limits{})
	checkMessages(t, Limits{}, msgs)
	if len(msgs) != 1 {
		t.Fatalf("pesan = %d, want 1", len(msgs))
	}
	if got := msgs[0].Text; got != "a b c" {
		t.Fatalf("teks = %q, want %q", got, "a b c")
	}
	for _, e := range msgs[0].Entities {
		if e.Type == EntityPre {
			t.Fatalf("backtick inline menjadi pre: %+v", msgs[0])
		}
	}
	if got := utf16Slice(t, msgs[0].Text, entity(t, msgs[0], EntityCode).Offset, entity(t, msgs[0], EntityCode).Length); got != "b" {
		t.Errorf("isi code = %q, want b", got)
	}
}

// TestChunkUTF16 is the control for EC-PLAT-011: the 4096 limit counts UTF-16
// code units, and a split never lands inside a surrogate pair.
func TestChunkUTF16(t *testing.T) {
	const emoji = "\U0001F600"
	msgs := RenderTelegram(strings.Repeat(emoji, 3000), Limits{})
	if len(msgs) != 2 {
		t.Fatalf("pesan = %d, want 2", len(msgs))
	}
	checkMessages(t, Limits{}, msgs)
	if got := Units(msgs[0].Text); got != MaxLen {
		t.Errorf("chunk 1 = %d unit, want %d", got, MaxLen)
	}
	if got := Units(msgs[1].Text); got != 6000-MaxLen {
		t.Errorf("chunk 2 = %d unit, want %d", got, 6000-MaxLen)
	}

	// A 4-byte rune that would straddle the boundary moves whole to the next
	// chunk instead of being cut.
	msgs = RenderTelegram(strings.Repeat("a", MaxLen-1)+emoji, Limits{})
	checkMessages(t, Limits{}, msgs)
	if len(msgs) != 2 || msgs[1].Text != emoji {
		t.Fatalf("emoji di batas: %d pesan, terakhir %q", len(msgs), msgs[len(msgs)-1].Text)
	}
	// Exactly filling the budget keeps the pair intact in one message.
	msgs = RenderTelegram(strings.Repeat("a", MaxLen-2)+emoji, Limits{})
	if len(msgs) != 1 || Units(msgs[0].Text) != MaxLen || !strings.HasSuffix(msgs[0].Text, emoji) {
		t.Fatalf("emoji pas 4096: %d pesan, %+v", len(msgs), msgs)
	}
	// No chunk may end in a lone high surrogate.
	for i, m := range msgs {
		if splitsSurrogate(m.Text, Units(m.Text)) {
			t.Fatalf("chunk %d berakhir di tengah surrogate pair", i)
		}
	}
}

// TestMeasureCardAfterEscape is the control for EC-PLAT-012: the card is
// measured after escaping, so escaping that expands the text can push it over
// 4096 even when the raw text fits.
func TestMeasureCardAfterEscape(t *testing.T) {
	card := strings.Repeat("a", 4092) + "\x01" + "\U0001F600"
	if raw := Units(card); raw != 4095 {
		t.Fatalf("prasyarat: panjang mentah = %d, want 4095", raw)
	}
	if got := MeasureCard(card); got != 4098 {
		t.Fatalf("MeasureCard = %d, want 4098 (setelah escaping)", got)
	}
	msgs := RenderTelegram(card, Limits{})
	if len(msgs) != 2 {
		t.Fatalf("pesan = %d, want 2 setelah escaping", len(msgs))
	}
	checkMessages(t, Limits{}, msgs)
	if !strings.Contains(msgs[0].Text, `\x01`) {
		t.Fatalf("control char tidak di-escape: %q", msgs[0].Text[:20])
	}
	if strings.ContainsRune(msgs[0].Text, 0x01) {
		t.Fatal("control char mentah ikut terkirim")
	}
	// The approval card keeps its markup but is measured on the visible text.
	if got := MeasureCard("**setuju** `rm -rf x`"); got != Units("setuju rm -rf x") {
		t.Errorf("MeasureCard markdown = %d, want %d", got, Units("setuju rm -rf x"))
	}
}

// TestEscape covers the plain-text escaping: control characters become visible
// escapes (which is why length is measured after escaping), invalid UTF-8 is
// replaced, and CRLF is preserved for the fence scanner.
func TestEscape(t *testing.T) {
	cases := map[string]string{
		"a\x01b":      `a\x01b`,
		"\u0085":      `\x85`,
		"\xff":        "\uFFFD",
		"a\r\nb":      "a\r\nb",
		"tab\there":   "tab\there",
		"`*_[<>&]()`": "`*_[<>&]()`",
	}
	for in, want := range cases {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
		if got := Escape(Escape(in)); got != want {
			t.Errorf("Escape tidak idempoten untuk %q: %q", in, got)
		}
	}
}

// TestAnchorInvalid is the control for EC-PLAT-018: an anchor that is not a
// positive numeric message id is dropped, so the send continues without reply.
func TestAnchorInvalid(t *testing.T) {
	bad := []string{"", "abc", "12x", "-3", "0", " 42", "42 ", "1.0", "042", "2147483648", "999999999999"}
	for _, raw := range bad {
		if _, ok := ParseAnchor(raw); ok {
			t.Errorf("ParseAnchor(%q) ok, want ditolak", raw)
		}
		if anchorField(raw) != "" {
			t.Errorf("anchorField(%q) mengirim reply, want tanpa reply", raw)
		}
	}
	for raw, want := range map[string]int64{"1": 1, "42": 42, "2147483647": 2147483647} {
		if got, ok := ParseAnchor(raw); !ok || got != want {
			t.Errorf("ParseAnchor(%q) = %d,%v want %d,true", raw, got, ok, want)
		}
		if anchorField(raw) == "" {
			t.Errorf("anchorField(%q) kosong untuk anchor valid", raw)
		}
	}
}

// anchorField mirrors the adapter: the form value is empty when the anchor is
// invalid, so the field is omitted (EC-PLAT-018).
func anchorField(raw string) string {
	id, ok := ParseAnchor(raw)
	if !ok {
		return ""
	}
	return formatID(id)
}

func formatID(id int64) string {
	if id == 0 {
		return ""
	}
	var b [20]byte
	i := len(b)
	for id > 0 {
		i--
		b[i] = byte('0' + id%10)
		id /= 10
	}
	return string(b[i:])
}

// TestEntityOffsetsUTF16 checks entity offsets and lengths against
// utf16.Encode: they address the same text the Bot API will see.
func TestEntityOffsetsUTF16(t *testing.T) {
	const src = "\U0001F600 **tebal** dan `kode` lalu [tautan](https://example.com) *miring*"
	msgs := RenderTelegram(src, Limits{})
	checkMessages(t, Limits{}, msgs)
	m := msgs[0]
	if got := Units(m.Text); got != len(utf16.Encode([]rune(m.Text))) {
		t.Fatalf("Units = %d, utf16.Encode = %d", got, len(utf16.Encode([]rune(m.Text))))
	}
	for _, want := range []struct{ typ, text string }{
		{EntityBold, "tebal"},
		{EntityCode, "kode"},
		{EntityTextLink, "tautan"},
		{EntityItalic, "miring"},
	} {
		e := entity(t, m, want.typ)
		if got := utf16Slice(t, m.Text, e.Offset, e.Length); got != want.text {
			t.Errorf("%s = %q, want %q", want.typ, got, want.text)
		}
		idx := strings.Index(m.Text, want.text)
		if idx < 0 {
			t.Fatalf("%s tidak ada di teks %q", want.text, m.Text)
		}
		if wantOffset := Units(m.Text[:idx]); e.Offset != wantOffset {
			t.Errorf("%s offset = %d, want %d unit UTF-16", want.typ, e.Offset, wantOffset)
		}
	}
	if e := entity(t, m, EntityTextLink); e.URL != "https://example.com" {
		t.Errorf("url = %q", e.URL)
	}
	if strings.Contains(m.Text, "**") || strings.Contains(m.Text, "](") {
		t.Errorf("markup bocor ke teks: %q", m.Text)
	}
}

// TestChunkAtBlockBoundaries keeps blocks whole when they fit, splits an
// oversized code block without literal fences, and keeps the order stable.
func TestChunkAtBlockBoundaries(t *testing.T) {
	src := strings.Repeat("a", 100) + "\n\n" + strings.Repeat("b", 100) + "\n\n" + strings.Repeat("c", 100)
	msgs := RenderTelegram(src, Limits{MaxLen: 250})
	checkMessages(t, Limits{MaxLen: 250}, msgs)
	if len(msgs) != 2 {
		t.Fatalf("pesan = %d, want 2", len(msgs))
	}
	if got := strings.TrimSuffix(msgs[0].Text, "\n\n"); got != strings.Repeat("a", 100)+"\n\n"+strings.Repeat("b", 100) {
		t.Errorf("chunk 1 tidak di batas blok: %q", got)
	}
	if got := msgs[1].Text; got != strings.Repeat("c", 100) {
		t.Errorf("chunk 2 = %q", got)
	}

	code := strings.Repeat("x = 1\n", 3000)
	doc := "```py\n" + code + "```"
	msgs = RenderTelegram(doc, Limits{})
	checkMessages(t, Limits{}, msgs)
	if len(msgs) < 2 {
		t.Fatalf("blok kode besar tidak dipecah: %d pesan", len(msgs))
	}
	var joined strings.Builder
	for i, m := range msgs {
		if strings.Contains(m.Text, "```") {
			t.Errorf("chunk %d memuat fence literal", i)
		}
		if e := entity(t, m, EntityPre); e.Language != "py" {
			t.Errorf("chunk %d pre language = %q", i, e.Language)
		}
		joined.WriteString(m.Text)
	}
	if joined.String() != code {
		t.Errorf("isi blok kode berubah setelah chunking (%d vs %d byte)", joined.Len(), len(code))
	}

	// An entity that crosses a split covers each chunk entirely instead of
	// being dropped or pointing past the text.
	msgs = RenderTelegram("**"+strings.Repeat("q", 2*MaxLen)+"**", Limits{})
	checkMessages(t, Limits{}, msgs)
	for i, m := range msgs {
		e := entity(t, m, EntityBold)
		if e.Offset != 0 || e.Length != Units(m.Text) {
			t.Errorf("chunk %d bold = %+v, want seluruh teks (%d unit)", i, e, Units(m.Text))
		}
	}
}

// TestTruncatedMarker checks the configured hard limit: content beyond
// MaxChunks is cut and marked once, and entities are clipped with it.
func TestTruncatedMarker(t *testing.T) {
	lim := Limits{MaxLen: 100, MaxChunks: 1}
	msgs := RenderTelegram(strings.Repeat("word ", 100), lim)
	checkMessages(t, lim, msgs)
	if len(msgs) != 1 {
		t.Fatalf("pesan = %d, want 1", len(msgs))
	}
	if !strings.HasSuffix(msgs[0].Text, Truncated) {
		t.Fatalf("penanda truncation hilang: %q", msgs[0].Text)
	}
	if Units(msgs[0].Text) > 100 {
		t.Fatalf("pesan terpotong %d unit > 100", Units(msgs[0].Text))
	}

	msgs = RenderTelegram("**"+strings.Repeat("z", 300)+"**", lim)
	checkMessages(t, lim, msgs)
	for _, e := range msgs[0].Entities {
		if e.Offset+e.Length > Units(strings.TrimSuffix(msgs[0].Text, Truncated)) {
			t.Fatalf("entity melewati teks terpotong: %+v", e)
		}
	}
}

// TestPlainFallback checks that a parse failure still produces escaped plain
// text, never an error and never markup entities.
func TestPlainFallback(t *testing.T) {
	src := strings.Repeat("*a* ", maxNodes+10)
	if _, err := Parse(src); err == nil {
		t.Fatal("Parse menerima input di atas budget")
	}
	msgs := RenderTelegram(src, Limits{MaxChunks: 100})
	if len(msgs) == 0 {
		t.Fatal("fallback menghasilkan 0 pesan")
	}
	checkMessages(t, Limits{MaxChunks: 100}, msgs)
	var b strings.Builder
	for _, m := range msgs {
		if len(m.Entities) != 0 {
			t.Fatalf("fallback memuat entity: %+v", m.Entities)
		}
		b.WriteString(m.Text)
	}
	if b.String() != Escape(src) {
		t.Fatal("fallback bukan plain escaped")
	}
}

// TestFallbackCorpus measures the fallback rate: realistic replies must parse,
// keeping fallback below the 0,1% target.
func TestFallbackCorpus(t *testing.T) {
	fallbacks := 0
	for _, src := range fallbackCorpus() {
		if _, err := Parse(src); err != nil {
			fallbacks++
			t.Logf("fallback pada corpus: %q: %v", src, err)
		}
		checkMessages(t, Limits{}, RenderTelegram(src, Limits{}))
	}
	if fallbacks*1000 > len(fallbackCorpus()) {
		t.Fatalf("fallback %d/%d > 0,1%%", fallbacks, len(fallbackCorpus()))
	}
}

func fallbackCorpus() []string {
	return []string{
		"Halo! Ada yang bisa saya bantu?",
		"**Selesai**: 3 tes lulus, 0 gagal.",
		"```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```",
		"~~~python\r\nprint(1)\r\n~~~\r\n",
		"- satu\n- dua\n- tiga",
		"1. baca\n2. tulis\n3. kirim",
		"Lihat [dokumentasi](https://example.com/docs).",
		"`kode inline` dan *miring* serta **tebal**.",
		"Emoji \U0001F600\U0001F680 aman di batas 4096.",
		"Baris CRLF\r\nkedua\r\nketiga",
		"Tab\tkontrol\tdan spasi.",
		"Laporan panjang:\n\n" + strings.Repeat("lorem ipsum dolor sit amet ", 40),
		"```\nkode tanpa info string\n```",
		"Kutipan `a*b_c` tidak boleh jadi markup.",
		"URL tanpa label: https://example.com/path?q=1",
		"[label](tg://resolve?domain=damnbot)",
		"Tabel sederhana:\n| a | b |\n| - | - |",
		"# Judul sebagai paragraf",
		"> kutipan sebagai paragraf",
		"Campuran **tebal `kode` miring** aman.",
	}
}

// TestRenderCorpus10k runs the fuzz properties over 10.000 generated inputs, so
// the normal test run also proves the 10k corpus never panics.
func TestRenderCorpus10k(t *testing.T) {
	for i, src := range fuzzCorpus(10000) {
		msgs := RenderTelegram(src, Limits{})
		checkMessages(t, Limits{}, msgs)
		if i == 0 {
			continue
		}
	}
}
