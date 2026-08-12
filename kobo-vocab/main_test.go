package main

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"golang.org/x/net/html"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"kobo-anki/core"
)

// writeTestDictZip builds a minimal single-file dicthtml-*.zip containing
// the given raw HTML body, for tests that need to exercise parseKoboDict
// itself (not just extractEntry) - e.g. cross-<w>-block behavior.
func writeTestDictZip(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	w, err := zw.Create("aa.html")
	if err != nil {
		t.Fatalf("zip.Create: %v", err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return path
}

func TestComputeGroupsCountsAcrossLanguagePairs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	createTestDB(dbPath) // 15 words, all -nl-en, per its hardcoded list

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer db.Close()

	cfg := loadConfig("") // no config file - defaults, OutDir="."
	cfg.OutDir = t.TempDir()

	groups, totalNew, err := computeGroups(cfg, db)
	if err != nil {
		t.Fatalf("computeGroups: %v", err)
	}
	if totalNew != 15 {
		t.Errorf("totalNew = %d, want 15", totalNew)
	}
	g, ok := groups["-nl-en"]
	if !ok {
		t.Fatalf("expected a -nl-en group")
	}
	if len(g.newWords) != 15 {
		t.Errorf("len(newWords) = %d, want 15", len(g.newWords))
	}
	if g.fromLang != "nl" || g.toLang != "en" {
		t.Errorf("fromLang/toLang = %q/%q, want nl/en", g.fromLang, g.toLang)
	}
}

func TestComputeGroupsExcludesExistingAndMissedWords(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	createTestDB(dbPath)

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer db.Close()

	cfg := loadConfig("")
	cfg.OutDir = t.TempDir()

	// First pass: everything is new.
	_, totalNew, err := computeGroups(cfg, db)
	if err != nil {
		t.Fatalf("computeGroups: %v", err)
	}
	if totalNew != 15 {
		t.Fatalf("first pass totalNew = %d, want 15", totalNew)
	}

	// Simulate one word already exported and one already a recorded miss.
	csvPath := filepath.Join(cfg.OutDir, "vocab-nl-en.csv")
	exportCSV(csvPath, nil, []Translation{{Word: "behoefte", Translation: "need", FromLang: "nl", ToLang: "en", Source: "dict"}})
	missesPath := filepath.Join(cfg.OutDir, "vocab-misses-nl-en.txt")
	appendMisses(missesPath, []string{"leek"})

	_, totalNew, err = computeGroups(cfg, db)
	if err != nil {
		t.Fatalf("computeGroups (second pass): %v", err)
	}
	if totalNew != 13 {
		t.Errorf("second pass totalNew = %d, want 13 (15 - 1 exported - 1 miss)", totalNew)
	}
}

func TestComputeGroupsRecognizesWordsWithStyledFrontAsExisting(t *testing.T) {
	// Regression test: dict_examples/lexicala/chatgpt override Entry.Front
	// with styled card content (e.g. "**behoefte**\n*een voorbeeldzin*")
	// instead of leaving it as the plain word. readExistingCSV's dedup key
	// is that CSV front column, so once Front diverges from the raw Kobo
	// word, the CSV-based check alone can never match it again - the word
	// looks "new" every single run, duplicating it forever. vocab-seen-*.txt
	// (independent of card content, see computeGroups/appendMisses call
	// sites in main()) is the fix - verify it alone is enough to exclude
	// the word even though its CSV front doesn't match the raw word at all.
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	createTestDB(dbPath)

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer db.Close()

	cfg := loadConfig("")
	cfg.OutDir = t.TempDir()

	_, totalNew, err := computeGroups(cfg, db)
	if err != nil {
		t.Fatalf("computeGroups: %v", err)
	}
	if totalNew != 15 {
		t.Fatalf("first pass totalNew = %d, want 15", totalNew)
	}

	// Simulate exporting "behoefte" with a styled front that no longer
	// matches the raw word - what dict_examples/lexicala/chatgpt actually
	// produce - and record it as seen, exactly as main()'s export step now
	// does after this fix.
	csvPath := filepath.Join(cfg.OutDir, "vocab-nl-en.csv")
	exportCSV(csvPath, nil, []Translation{{
		Word:        "**behoefte**\n*een voorbeeldzin*",
		Translation: "**need**\n*an example sentence*",
		FromLang:    "nl", ToLang: "en", Source: "dict",
	}})
	seenPath := filepath.Join(cfg.OutDir, "vocab-seen-nl-en.txt")
	appendMisses(seenPath, []string{"behoefte"})

	groups, totalNew, err := computeGroups(cfg, db)
	if err != nil {
		t.Fatalf("computeGroups (second pass): %v", err)
	}
	if totalNew != 14 {
		t.Errorf("second pass totalNew = %d, want 14 (15 - 1 seen, despite the CSV front not matching the raw word)", totalNew)
	}
	for _, w := range groups["-nl-en"].newWords {
		if strings.ToLower(w.Text) == "behoefte" {
			t.Errorf("behoefte was re-offered as new - vocab-seen-*.txt was not consulted")
		}
	}
}

func TestExtractEntryFlatFormat(t *testing.T) {
	block := `<w><a name="Aagje"/><div><b>Aagje</b><br/><font color="#007000">noun</font><br>
female first name</div></w>`
	word, def, _, ok := extractEntry(block, 1, 1, 150, nil, false, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if word != "aagje" {
		t.Errorf("word = %q, want %q", word, "aagje")
	}
	if def != "female first name" {
		t.Errorf("def = %q, want %q", def, "female first name")
	}
}

func TestExtractEntryFlatFormatEmbeddedNewline(t *testing.T) {
	// Real-world case found via a real device run: this dict only has one
	// <br/> (between the bold headword and everything else), then packs
	// "word [phonetic]" and the actual gloss into that same segment,
	// separated by a literal newline rather than another <br> tag. Before
	// the fix this produced "invloed [invlut] Einfluß" (headword repeated,
	// phonetic left unstripped, glued to the real gloss).
	block := `<w><a name="invloed"/><div><b>invloed</b><br/>invloed [invlut]
     Einfluß</div></w>`
	word, def, _, ok := extractEntry(block, 1, 1, 150, nil, false, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if word != "invloed" {
		t.Errorf("word = %q, want %q", word, "invloed")
	}
	if def != "Einfluß" {
		t.Errorf("def = %q, want %q (should not repeat the headword or leak the [phonetic] annotation)", def, "Einfluß")
	}
}

var boldTagRe = regexp.MustCompile(`(?is)<b(?:\s[^>]*)?>(.*?)</b\s*>`)

// defaultCardFormat compiles DefaultCardFormat for tests that need a
// cardTemplate but aren't testing card_format itself.
func defaultCardFormat() cardTemplate {
	return compileCardFormatOrDefault(DefaultCardFormat)
}

func TestExtractEntryTranslationTagKeepsOnlyBoldSpans(t *testing.T) {
	// Real-world shape (nl-de "invloed"): the Dutch gloss and grammar tags
	// are wrapped in <i>, the actual German translations in <b>. With a
	// translation_tag of "b" configured, the definition should end up as
	// just the bold spans, comma-joined - no gloss, no "(der"/"(die" tags.
	block := `<w><p><a name="invloed" /><b>invloed</b></p><var></var><ol>
<li><div><i>inwerking</i><br/><b>Einfluss</b><i> (der</i>), <b>Einwirkung</b><i> (die</i>)</div></li>
</ol></w>`
	_, def, _, ok := extractEntry(block, 1, 1, 150, boldTagRe, false, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	want := "Einfluss, Einwirkung"
	if def != want {
		t.Errorf("def = %q, want %q", def, want)
	}
}

func TestExtractEntryTranslationTagDoesNotMatchBr(t *testing.T) {
	// Regression: a naive `<b[^>]*>` opening-tag pattern also matches
	// `<br/>` (since "br" starts with "b" and everything up to the next
	// '>' is swallowed as if it were attributes), which would spuriously
	// "open" a bold span at the <br/> and glue in whatever italic/plain
	// text sits between it and the next real </b> - exactly the shape of
	// the real dicthtml-nl-de.zip "waarschuwing" entry that exposed this.
	block := `<w><p><a name="test" /><b>test</b></p><var></var><ol>
<li><div><b>Warnung</b><i> (die</i>)<br/><i>(x)</i><b>Verwarnung</b><i> (die</i>)</div></li>
</ol></w>`
	_, def, _, ok := extractEntry(block, 1, 1, 150, boldTagRe, false, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	want := "Warnung, Verwarnung"
	if def != want {
		t.Errorf("def = %q, want %q (the <br/> and italic '(x)' must not leak into the bold-only extraction)", def, want)
	}
}

func TestExtractEntryTranslationTagFallsBackWhenTagAbsent(t *testing.T) {
	// If a sense has no match for the configured tag at all (rather than
	// this dict simply not using <b> for this particular word), falling
	// back to the full cleaned text avoids silently emitting a blank card.
	block := `<w><p><a name="test" /><b>test</b></p><var></var><ol>
<li><div>plain sense with no bold or italic spans at all</div></li>
</ol></w>`
	_, def, _, ok := extractEntry(block, 1, 1, 150, boldTagRe, false, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if def != "plain sense with no bold or italic spans at all" {
		t.Errorf("def = %q, want the full cleaned sense as a fallback", def)
	}
}

func TestExtractEntryTranslationTagIgnoredForFlatFormat(t *testing.T) {
	// Verified against the real dicthtml-nl-en.zip: in the flat format, <b>
	// wraps the repeated headword, not the translation - applying the same
	// tag filter there must not run at all (extractEntry only applies
	// tagRe within the nested/<li> branch), or it would extract nothing.
	block := `<w><a name="Aagje"/><div><b>Aagje</b><br/><font color="#007000">noun</font><br>
female first name</div></w>`
	_, def, _, ok := extractEntry(block, 1, 1, 150, boldTagRe, false, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if def != "female first name" {
		t.Errorf("def = %q, want %q (flat format ignores translation_tag)", def, "female first name")
	}
}

func TestExtractEntryCardWithExample(t *testing.T) {
	// Real shape (nl-de "afspraak"): a sense's own class="ex" div, holding a
	// <ul> of one <li>Dutch phrase<br/>German translation</li> per example -
	// the first one is used.
	block := `<w><p><a name="afspraak" /><b>afspraak</b></p><var></var><ol>
<li>
<div><b>Verabredung</b><i> (die</i>), <b>Vereinbarung</b><i> (die</i>)</div>
<div class="ex"><div>♦ voorbeelden</div><ul><li>een afspraak maken<br/>eine Verabredung treffen</li><li>een afspraak verzetten<br/>einen Termin verschieben</li></ul></div>
</li>
</ol></w>`
	word, def, front, ok := extractEntry(block, 1, 1, 150, boldTagRe, true, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if word != "afspraak" {
		t.Errorf("word = %q, want afspraak", word)
	}
	wantDef := "**Verabredung**\n\nVereinbarung\n\n*eine Verabredung treffen*"
	if def != wantDef {
		t.Errorf("def = %q, want %q", def, wantDef)
	}
	wantFront := "**afspraak**\n\n*een afspraak maken*"
	if front != wantFront {
		t.Errorf("front = %q, want %q", front, wantFront)
	}
}

func TestExtractEntryCardDoesNotMistakeSecondSenseForExample(t *testing.T) {
	// Real shape (nl-de "tegenwoordig"): the <li> right after the first
	// sense is itself a second sense (wraps its own <div>), not an example -
	// must not be treated as one, or the wrong text gets paired together.
	block := `<w><p><a name="tegenwoordig" /><b>tegenwoordig</b></p><var></var><ol>
<li><div><b>anwesend</b></div></li>
<li><div><b>heutig</b>, <b>gegenwärtig</b></div></li>
</ol></w>`
	_, def, front, ok := extractEntry(block, 1, 1, 150, boldTagRe, true, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if def != "**anwesend**" {
		t.Errorf("def = %q, want %q (no example - li[1] is a second sense)", def, "**anwesend**")
	}
	if front != "**tegenwoordig**" {
		t.Errorf("front = %q, want %q (no example)", front, "**tegenwoordig**")
	}
}

func TestExtractEntryCardWithoutExampleAvailable(t *testing.T) {
	// Real shape (nl-de "onomkoopbaar"): a single sense, no example <li> at
	// all - still produces a themed card, just with no example.
	block := `<w><p><a name="onomkoopbaar" /><b>onomkoopbaar</b></p><var></var><ol>
<li><div><b>unbestechlich</b></div></li>
</ol></w>`
	_, def, front, ok := extractEntry(block, 1, 1, 150, boldTagRe, true, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if def != "**unbestechlich**" {
		t.Errorf("def = %q, want %q", def, "**unbestechlich**")
	}
	if front != "**onomkoopbaar**" {
		t.Errorf("front = %q, want %q", front, "**onomkoopbaar**")
	}
}

func TestExtractEntryCardExampleWithInlineAnnotationDoesNotNestMarkdown(t *testing.T) {
	// Real shape (nl-de "verschrikkelijk"): the German half of the first
	// example opens with its own inline "<i>(note)</i>" usage annotation.
	// Naively running that half through the markdown-emitting cleanText (as
	// used for definitions) before formatCard wraps the whole example in
	// *asterisks* would nest emphasis markers into broken output like
	// "**(ook)* text*" - the example halves must use the non-markdown-
	// emitting cleanPlainText instead.
	block := `<w><p><a name="verschrikkelijk" /><b>verschrikkelijk</b></p><var></var><ol>
<li>
<div><b>schrecklich</b></div>
<div class="ex"><div>♦ voorbeelden</div><ul><li>verschrikkelijke gevolgen<br/><i>(ook)</i> verheerende Folgen</li></ul></div>
</li>
</ol></w>`
	_, def, _, ok := extractEntry(block, 1, 1, 150, boldTagRe, true, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	want := "**schrecklich**\n\n*(ook) verheerende Folgen*"
	if def != want {
		t.Errorf("def = %q, want %q (no nested/unbalanced emphasis markers)", def, want)
	}
}

func TestExtractEntryCardFallsBackWhenNoTagMatch(t *testing.T) {
	// A sense with no bold match at all (tagRe finds nothing) can't build a
	// card - falls back to the plain multi-sense extraction, same as when
	// dict_examples is off.
	block := `<w><p><a name="test" /><b>test</b></p><var></var><ol>
<li><div>plain sense with no bold spans at all</div></li>
</ol></w>`
	_, def, front, ok := extractEntry(block, 1, 1, 150, boldTagRe, true, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if def != "plain sense with no bold spans at all" {
		t.Errorf("def = %q, want the plain cleaned sense as a fallback", def)
	}
	if front != "" {
		t.Errorf("front = %q, want empty - the fallback path never sets Front", front)
	}
}

func TestExtractEntryCardDisabledByDefault(t *testing.T) {
	// Same input as TestExtractEntryCardWithExample, but wantExamples=false
	// (dict_examples off, the default) - must reproduce today's exact plain
	// multi-sense behavior, not the card shape.
	block := `<w><p><a name="afspraak" /><b>afspraak</b></p><var></var><ol>
<li><div><b>Verabredung</b><i> (die</i>), <b>Vereinbarung</b><i> (die</i>)</div></li>
<li>een afspraak maken<br/>eine Verabredung treffen</li>
</ol></w>`
	_, def, front, ok := extractEntry(block, 1, 1, 150, boldTagRe, false, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if def != "Verabredung, Vereinbarung" {
		t.Errorf("def = %q, want %q (plain tag-extracted text, not a card)", def, "Verabredung, Vereinbarung")
	}
	if front != "" {
		t.Errorf("front = %q, want empty - dict_examples=false never sets Front", front)
	}
}

func TestExtractEntryMultiSenseDoesNotAbsorbNestedExampleLis(t *testing.T) {
	// Regression test for a real bug this session's investigation surfaced:
	// a naive non-nesting-aware <li>...</li> match (the old liRe-based
	// approach) matches the FIRST </li> it finds regardless of depth - for
	// a sense followed by its own "<div class=\"ex\"><ul><li>example</li>
	// ...", that's the closing tag of the first EXAMPLE bullet, not the
	// sense's own closing tag. That silently truncated every "sense" and
	// flattened every example bullet into what looked like separate
	// top-level senses - verified against the real "boodschap" entry, which
	// appeared to have 19 senses; it actually has 4, each with its own
	// example list. topLevelLis must correctly treat the whole class="ex"
	// block, examples included, as part of ITS enclosing sense - so with
	// max_senses=2 here, the two real senses come back whole, with none of
	// the first sense's own example text leaking into the list.
	block := `<w><p><a name="boodschap" /><b>boodschap</b></p><var></var><ol>
<li>
<div><b>Besorgung</b><i> (die</i>), <b>Einkauf</b><i> (der</i>)</div>
<div class="ex"><div>♦ voorbeelden</div><ul><li>een boodschap doen<br/>eine Besorgung machen</li><li>boodschappen gaan doen<br/>einkaufen gehen</li></ul></div>
</li>
<li>
<div><b>Auftrag</b><i> (der</i>)</div>
<div class="ex"><div>♦ voorbeelden</div><ul><li>iemand met een boodschap belasten<br/>jemanden mit einem Auftrag betrauen</li></ul></div>
</li>
</ol></w>`
	_, def, _, ok := extractEntry(block, 2, 1, 300, boldTagRe, false, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	want := "- Besorgung, Einkauf\n- Auftrag"
	if def != want {
		t.Errorf("def = %q, want %q (exactly the two real senses, no example text absorbed)", def, want)
	}
}

func TestParseKoboDictKeepsRicherHomographBlock(t *testing.T) {
	// Regression test for a real finding: 4.76% of headwords in
	// dicthtml-nl-de.zip have more than one <w> block sharing the same
	// name (homographs across parts of speech - e.g. "ruim" as adverb,
	// nautical term, adjective, and noun each get their own block). The
	// old code did a plain map overwrite (dict.Entries[word] = definition)
	// per block, so whichever block happened to be parsed LAST silently
	// won - for 67% of affected real words, that was a thinner block than
	// another one available for the same word (verified: "ruim" kept a
	// 2-sense nautical noun over the 8-sense adjective entry that's its
	// actually-common meaning). Keep whichever block's extracted
	// definition is longer, regardless of parse order.
	body := `<w><p><a name="ruim" /><b>ruim</b></p><var></var><ol>
<li><div><b>raum</b></div></li>
</ol></w>
<w><p><a name="ruim" /><b>ruim</b></p><var></var><ol>
<li><div><b>weit</b>, <b>geräumig</b>, <b>großzügig</b></div></li>
</ol></w>`
	dict, err := parseKoboDict(writeTestDictZip(t, body), 1, 1, 300, "b", false, defaultCardFormat(), nil)
	if err != nil {
		t.Fatalf("parseKoboDict: %v", err)
	}
	want := "weit, geräumig, großzügig"
	if got := dict.Entries["ruim"]; got != want {
		t.Errorf("Entries[ruim] = %q, want %q (the richer block, even though it was parsed second)", got, want)
	}
}

func TestParseKoboDictKeepsRicherHomographBlockRegardlessOfOrder(t *testing.T) {
	// Same as above with the richer block FIRST - proves this is genuinely
	// "keep the richer one", not an accidental "keep the first one" that
	// only happened to work in the other test.
	body := `<w><p><a name="ruim" /><b>ruim</b></p><var></var><ol>
<li><div><b>weit</b>, <b>geräumig</b>, <b>großzügig</b></div></li>
</ol></w>
<w><p><a name="ruim" /><b>ruim</b></p><var></var><ol>
<li><div><b>raum</b></div></li>
</ol></w>`
	dict, err := parseKoboDict(writeTestDictZip(t, body), 1, 1, 300, "b", false, defaultCardFormat(), nil)
	if err != nil {
		t.Fatalf("parseKoboDict: %v", err)
	}
	want := "weit, geräumig, großzügig"
	if got := dict.Entries["ruim"]; got != want {
		t.Errorf("Entries[ruim] = %q, want %q (the richer block, parsed first this time)", got, want)
	}
}

func TestParseKoboDictDiscardsStaleFrontFromReplacedBlock(t *testing.T) {
	// dict_examples sets Fronts[word] from whichever block wins. If a
	// thinner block (with an example, so it got a Front) is later replaced
	// by a richer block that happens to have no example, the stale Front
	// from the discarded block must not linger - it would pair a Front
	// built from one block's content with a Definition from a different
	// one.
	body := `<w><p><a name="test" /><b>test</b></p><var></var><ol>
<li>
<div><b>kurz</b></div>
<div class="ex"><div>♦ voorbeelden</div><ul><li>een testzin<br/>ein Testsatz</li></ul></div>
</li>
</ol></w>
<w><p><a name="test" /><b>test</b></p><var></var><ol>
<li><div>a much longer plain sense with no bold spans at all so the card path falls back and never sets Front for this block</div></li>
</ol></w>`
	dict, err := parseKoboDict(writeTestDictZip(t, body), 1, 1, 300, "b", true, defaultCardFormat(), nil)
	if err != nil {
		t.Fatalf("parseKoboDict: %v", err)
	}
	want := "a much longer plain sense with no bold spans at all so the card path falls back and never sets Front for this block"
	if got := dict.Entries["test"]; got != want {
		t.Errorf("Entries[test] = %q, want %q (the richer, no-bold second block, falling back to the plain path)", got, want)
	}
	if front, ok := dict.Fronts["test"]; ok {
		t.Errorf("Fronts[test] = %q, want no entry - the block that set it was discarded, and the winning block's fallback path never sets Front", front)
	}
}

func TestExtractEntryNestedFormatBoldItalic(t *testing.T) {
	block := `<w><p><a name="kilte" /><b>Kühle</b></p><var></var><ol>
<li>
<div><b>Kühle</b><i> (die;</i> mv: <i>geen meervoud)</i></div>
</li>
</ol></w>`
	word, def, _, ok := extractEntry(block, 1, 1, 150, nil, false, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if word != "kilte" {
		t.Errorf("word = %q, want %q", word, "kilte")
	}
	if !strings.Contains(def, "**Kühle**") {
		t.Errorf("def = %q, want it to contain bold marker around Kühle", def)
	}
	if !strings.Contains(def, "*(die;*") {
		t.Errorf("def = %q, want an italic span for '(die;'", def)
	}
}

func TestExtractEntryBrBecomesLineBreak(t *testing.T) {
	// Flat format keeps only the segment after the last <br> (pre-existing,
	// documented legacy behavior) - use the nested/<li> format here, where
	// the full sense content is preserved and <br> should survive as '\n'.
	block := `<w><p><a name="test" /><b>test</b></p><var></var><ol>
<li><div><i>first</i><br/><i>second</i></div></li>
</ol></w>`
	_, def, _, ok := extractEntry(block, 1, 1, 150, nil, false, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if !strings.Contains(def, "\n") {
		t.Errorf("def = %q, want a literal newline where <br/> was", def)
	}
	if strings.Contains(def, "\x00") {
		t.Errorf("def = %q, brSentinel leaked through unconverted", def)
	}
}

func TestExtractEntryMultipleSensesBecomeList(t *testing.T) {
	block := `<w><p><a name="gaan" /><b>gaan</b></p><var></var><ol>
<li><div>first sense</div></li>
<li><div>second sense</div></li>
</ol></w>`
	_, def, _, ok := extractEntry(block, 2, 1, 150, nil, false, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	want := "- first sense\n- second sense"
	if def != want {
		t.Errorf("def = %q, want %q", def, want)
	}
}

func TestExtractEntrySingleSenseHasNoBullet(t *testing.T) {
	block := `<w><p><a name="gaan" /><b>gaan</b></p><var></var><ol>
<li><div>only sense</div></li>
</ol></w>`
	_, def, _, ok := extractEntry(block, 1, 1, 150, nil, false, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if strings.HasPrefix(def, "-") {
		t.Errorf("def = %q, single sense should not get a list bullet", def)
	}
}

func TestCleanTextEntityDecoding(t *testing.T) {
	got := cleanText(`<div><b>B&amp;B</b></div>`)
	if got != "**B&B**" {
		t.Errorf("got %q, want %q (entity decoded, bold preserved)", got, "**B&B**")
	}
}

func TestCleanTextNBSP(t *testing.T) {
	got := cleanText("foo&nbsp;bar")
	if got != "foo bar" {
		t.Errorf("got %q, want NBSP collapsed to a plain space", got)
	}
}

func TestToMarkdownAdjacentSpansMerge(t *testing.T) {
	got := toMarkdown(`<i> (</i><i>die</i><i>;</i>`)
	if strings.Count(got, "*") != 2 {
		t.Errorf("got %q, want adjacent <i> pairs merged into a single '*...*' span (2 stars)", got)
	}
}

func TestToMarkdownNoAmbiguousStarRun(t *testing.T) {
	got := toMarkdown(`<b>Synchronie</b><i> (die</i>)`)
	if strings.Contains(got, "***") {
		t.Errorf("got %q, want no ambiguous run of 3+ literal asterisks", got)
	}
}

func TestTruncateMarkdownVisibleNeverUnbalanced(t *testing.T) {
	cases := []string{
		"**bold word here** and *italic text* and more plain words to push past the limit",
		"- first sense here\n- second sense that is quite a bit longer than the first one",
		"*a*",
		"**",
		"no markdown at all just plain text that is long enough to truncate",
	}
	for _, s := range cases {
		for maxChars := 1; maxChars <= 20; maxChars++ {
			got := truncateMarkdownVisible(s, maxChars)
			if strings.Count(got, "*")%2 != 0 {
				t.Errorf("truncateMarkdownVisible(%q, %d) = %q: odd number of '*' (unbalanced)", s, maxChars, got)
			}
			trimmed := strings.TrimRight(got, "\n")
			if idx := strings.LastIndexByte(trimmed, '\n'); idx >= 0 {
				if strings.TrimSpace(trimmed[idx+1:]) == "-" {
					t.Errorf("truncateMarkdownVisible(%q, %d) = %q: dangling empty list item", s, maxChars, got)
				}
			} else if strings.TrimSpace(trimmed) == "-" {
				t.Errorf("truncateMarkdownVisible(%q, %d) = %q: dangling empty list item", s, maxChars, got)
			}
		}
	}
}

func TestTruncateMarkdownVisibleZeroMeansUnlimited(t *testing.T) {
	s := "some text **with bold** that should not be cut"
	if got := truncateMarkdownVisible(s, 0); got != s {
		t.Errorf("got %q, want unchanged input when maxChars=0", got)
	}
}

func TestLoadConfigDoesNotWarnAboutAutoParse(t *testing.T) {
	// auto_parse lives in anki-mywords.conf but is read by start.sh's shell
	// grep, not by this binary. It must be recognized here anyway, or every
	// real run prints a spurious "unknown setting" warning.
	dir := t.TempDir()
	p := filepath.Join(dir, "anki-mywords.conf")
	os.WriteFile(p, []byte("auto_parse=true\nproviders=dict\n"), 0644)

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	loadConfig(p)
	log.SetOutput(old)

	if strings.Contains(buf.String(), "auto_parse") {
		t.Errorf("auto_parse was reported as unknown:\n%s", buf.String())
	}
}

func TestComputeGroupsUsesIDColumnInsteadOfSeenFile(t *testing.T) {
	// The duplicate-word bug, now solved by the id column rather than the
	// vocab-seen sidecar: a card whose rendered front looks nothing like the
	// source word is still recognized as already-done, with no
	// vocab-seen-*.txt present at all.
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	createTestDB(dbPath)

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer db.Close()

	cfg := loadConfig("")
	cfg.OutDir = t.TempDir()

	csvPath := filepath.Join(cfg.OutDir, "vocab-nl-en.csv")
	exportCSV(csvPath, nil, []Translation{{
		SourceWord:  "behoefte",
		Word:        "**behoefte**\nnood\n*een dringende behoefte.*",
		Translation: "**need**\nrequirement",
	}})

	if _, err := os.Stat(filepath.Join(cfg.OutDir, "vocab-seen-nl-en.txt")); !os.IsNotExist(err) {
		t.Fatal("vocab-seen file should not be written any more")
	}

	groups, totalNew, err := computeGroups(cfg, db)
	if err != nil {
		t.Fatalf("computeGroups: %v", err)
	}
	if totalNew != 14 {
		t.Errorf("totalNew = %d, want 14 (15 - 1 already in the deck)", totalNew)
	}
	for _, w := range groups["-nl-en"].newWords {
		if strings.ToLower(w.Text) == "behoefte" {
			t.Error("behoefte re-offered as new despite its id being in the deck")
		}
	}
}

func TestExportCSVWritesSourceWordAsID(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.csv")
	if err := exportCSV(p, nil, []Translation{{
		SourceWord: "liepen", Word: "**lopen**", Translation: "**laufen**",
	}}); err != nil {
		t.Fatalf("exportCSV: %v", err)
	}
	cards, err := core.LoadCards(p)
	if err != nil {
		t.Fatalf("LoadCards: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	if cards[0].ID != "liepen" {
		t.Errorf("ID = %q, want the source word", cards[0].ID)
	}
	if cards[0].Key() != "liepen" {
		t.Errorf("Key() = %q, want the source word", cards[0].Key())
	}
}

// --- class-aware extraction (see CLASSES.md in the dict source) ---

func TestExtractCardEntryPrefersClassMarkers(t *testing.T) {
	// class="tr" identifies real translations, class="t" the target half of
	// an example. Note the bolded "(in" government fragment carries no tr
	// class and must not reach the card, and the example holds two labelled
	// alternatives of which only the first is wanted.
	block := `<w><p><a name="test" /><b>test</b></p><var></var><ol>
<li>
<div><b class="tr">hineinstürzen</b><i class="gram"> (sein)</i><b> (in</b><b>)</b>, <b class="tr">stürzen</b></div>
<div class="ex"><div>♦ voorbeelden</div><ul><li>iets openbaar maken<br/><i class="reg">(aan het licht brengen)</i><span class="t">etwas an die Öffentlichkeit bringen</span> <i class="reg">(publiciteit geven)</i><span class="t">etwas veröffentlichen</span></li></ul></div>
</li>
</ol></w>`
	_, def, front, ok := extractEntry(block, 1, 1, 300, boldTagRe, true, defaultCardFormat(),
		map[string]bool{"gen": true, "gram": true})
	if !ok {
		t.Fatalf("expected ok=true")
	}
	wantDef := "**hineinstürzen**\n\nstürzen\n\n*etwas an die Öffentlichkeit bringen*"
	if def != wantDef {
		t.Errorf("def  = %q\nwant = %q", def, wantDef)
	}
	wantFront := "**test**\n\n*iets openbaar maken*"
	if front != wantFront {
		t.Errorf("front = %q\nwant  = %q", front, wantFront)
	}
}

func TestExtractCardEntryFallsBackWithoutClasses(t *testing.T) {
	// A dictionary from another source (the nl-en one here) carries no
	// class markers at all; extraction must still work via translation_tag
	// and the <br> split.
	block := `<w><p><a name="test" /><b>test</b></p><var></var><ol>
<li>
<div><b>Verabredung</b><i> (die</i>), <b>Abmachung</b></div>
<div class="ex"><div>♦ voorbeelden</div><ul><li>een afspraak maken<br/>eine Verabredung treffen</li></ul></div>
</li>
</ol></w>`
	_, def, front, ok := extractEntry(block, 1, 1, 300, boldTagRe, true, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if def != "**Verabredung**\n\nAbmachung\n\n*eine Verabredung treffen*" {
		t.Errorf("def = %q", def)
	}
	if front != "**test**\n\n*een afspraak maken*" {
		t.Errorf("front = %q", front)
	}
}

func TestStripLabelClassesRemovesOnlyRequestedClasses(t *testing.T) {
	src := `<i class="gen">(der)</i><i class="reg">(figuurlijk)</i><i class="dom">(medisch)</i><i>(ook)</i>`
	got := stripLabelClasses(src, map[string]bool{"gen": true, "dom": true})
	if strings.Contains(got, "der") || strings.Contains(got, "medisch") {
		t.Errorf("requested classes not removed: %q", got)
	}
	if !strings.Contains(got, "figuurlijk") || !strings.Contains(got, "(ook)") {
		t.Errorf("unrequested / unclassified annotations were removed: %q", got)
	}
	if stripLabelClasses(src, nil) != src {
		t.Error("an empty set must leave the markup untouched")
	}
}

func TestLabelSetIgnoresUnknownClasses(t *testing.T) {
	got := labelSet([]string{"gen", "nonsense", "reg", ""})
	if !got["gen"] || !got["reg"] {
		t.Errorf("known classes missing: %v", got)
	}
	if got["nonsense"] {
		t.Errorf("unknown class accepted: %v", got)
	}
}

func TestExtractCardEntryPutsArticleOnSourceSide(t *testing.T) {
	// A learner needs "het huis", not "huis". The article is salvaged from
	// the entry's headword line by the dict pipeline (class="art") and
	// belongs with the source-language headword only - the German "(das)"
	// on the translation is a different article and stays an annotation.
	block := `<w><p><a name="huis" /><b>huis</b></p><var></var><i class="art">(het)</i><ol>
<li><div><b class="tr">Haus</b><i class="gen"> (das)</i></div></li>
</ol></w>`
	_, def, front, ok := extractEntry(block, 1, 1, 300, boldTagRe, true, defaultCardFormat(),
		map[string]bool{"gen": true, "gram": true})
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if front != "**het huis**" {
		t.Errorf("front = %q, want %q", front, "**het huis**")
	}
	if def != "**Haus**" {
		t.Errorf("def = %q, want the German article stripped as an annotation", def)
	}
}

func TestExtractCardEntryWithoutArticle(t *testing.T) {
	// Verbs and adjectives carry no article; the headword is used as-is.
	block := `<w><p><a name="lopen" /><b>lopen</b></p><var></var><ol>
<li><div><b class="tr">laufen</b></div></li>
</ol></w>`
	_, _, front, ok := extractEntry(block, 1, 1, 300, boldTagRe, true, defaultCardFormat(), nil)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if front != "**lopen**" {
		t.Errorf("front = %q, want the bare headword", front)
	}
}

func TestExtractCardEntryWorksWithoutTranslationTag(t *testing.T) {
	// A dictionary carrying class="tr" needs no translation_tag: the card
	// path used to be gated on one being configured, so dropping the
	// setting silently disabled dict_examples even for a marked-up dict.
	block := `<w><p><a name="huis" /><b>huis</b></p><var></var><i class="art">(het)</i><ol>
<li>
<div><b class="tr">Haus</b><i class="gen"> (das)</i></div>
<div class="ex"><div>♦ voorbeelden</div><ul><li>een mooi huis<br/><span class="t">ein schönes Haus</span></li></ul></div>
</li>
</ol></w>`
	_, def, front, ok := extractEntry(block, 1, 1, 300, nil, true, defaultCardFormat(),
		map[string]bool{"gen": true})
	if !ok {
		t.Fatalf("expected ok=true with tagRe nil")
	}
	if def != "**Haus**\n\n*ein schönes Haus*" {
		t.Errorf("def = %q", def)
	}
	if front != "**het huis**\n\n*een mooi huis*" {
		t.Errorf("front = %q", front)
	}
}

// withBoldFallback appends a bare-tag fallback to a profile's translation
// chain, at the same depth as its own selector.
//
// The retired global translation_tag= setting used to build this inside
// resolveDictProfile, for every dictionary at once. It is reproduced here
// because the tests below pin *rendering* - what a card looks like given a
// chain that reaches bold-only translations - which is still worth checking;
// only the config mechanism that reached every dictionary indiscriminately
// has gone. A [pair:] section states such a chain directly.
func withBoldFallback(p dictProfile, tag string) []string {
	if tag == "" || p.translationText != "" {
		return p.translation
	}
	if len(p.translation) == 0 {
		return []string{"> div:nth-of-type(1) " + tag, tag}
	}
	scope := ""
	if f := strings.Fields(p.translation[0]); len(f) > 1 {
		scope = strings.Join(f[:len(f)-1], " ") + " "
	}
	return append(append([]string{}, p.translation...), scope+tag)
}

// extractEntry adapts the pre-profile signature onto the profile pipeline,
// so every behaviour these tests pin down - flat glosses, embedded
// newlines, sense limits, bold-only translations, themed cards - is still
// checked, now through the code that actually runs. The profile is detected
// from the block itself, exactly as a real dictionary's would be.
func extractEntry(block string, maxSenses, maxDepth, maxChars int, tagRe *regexp.Regexp, wantExamples bool, cf cardTemplate, dropLabels map[string]bool) (word, definition, front string, ok bool) {
	tag := ""
	if tagRe != nil {
		tag = "b" // every caller here uses the bold tag
	}
	var strip []string
	for k, v := range dropLabels {
		if v {
			strip = append(strip, k)
		}
	}
	sort.Strings(strip)

	name := detectProfile(block)
	p := builtinProfiles[name]
	p.name = name
	p.translation = withBoldFallback(p, tag)

	doc, err := html.Parse(strings.NewReader(normalizeDictHTML(block)))
	if err != nil {
		return "", "", "", false
	}
	c := compileProfile(p, strip)
	nodes := query(doc, c.entry)
	if len(nodes) == 0 {
		return "", "", "", false
	}
	e, got := extractProfiled(c, nodes[0])
	if !got {
		return "", "", "", false
	}
	definition, front = renderEntry(e, dictRender{
		maxSenses: maxSenses, maxChars: maxChars,
		asCard: wantExamples, cardFormat: cf,
	}, p)
	return e.word, definition, front, definition != ""
}

// parseKoboDict adapts the pre-profile loader signature onto parseDict,
// detecting the profile from the dictionary itself.
func parseKoboDict(path string, maxSenses, maxDepth, maxChars int, tag string, dictExamples bool, cf cardTemplate, dropLabels map[string]bool) (*KoboDict, error) {
	p, _ := resolveDictProfile(PairSettings{}, path)
	p.translation = withBoldFallback(p, tag)
	var strip []string
	for k, v := range dropLabels {
		if v {
			strip = append(strip, k)
		}
	}
	sort.Strings(strip)
	return parseDict(path, compileProfile(p, strip), dictRender{
		maxSenses: maxSenses, maxChars: maxChars,
		asCard: dictExamples, cardFormat: cf,
	})
}

// stripLabelClasses adapts the old regex-based label stripper onto the
// selector-based one, which now removes annotations by class alone rather
// than by a fixed <i class="..."> pattern.
func stripLabelClasses(src string, drop map[string]bool) string {
	if len(drop) == 0 {
		return src
	}
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return src
	}
	for cls, want := range drop {
		if want {
			removeMatching(doc, parseSelector("."+cls))
		}
	}
	var b strings.Builder
	if err := html.Render(&b, doc); err != nil {
		return src
	}
	return b.String()
}
