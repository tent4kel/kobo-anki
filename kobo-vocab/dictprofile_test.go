package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func parseFragment(t *testing.T, src string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

func queryText(t *testing.T, src, sel string) []string {
	t.Helper()
	var out []string
	for _, n := range query(parseFragment(t, src), parseSelector(sel)) {
		out = append(out, nodeText(n))
	}
	return out
}

func TestSelectorSubset(t *testing.T) {
	const src = `<w><ol>
		<li>
			<div><b class="tr">eins</b> <b>zwei</b></div>
			<div><b class="tr">drei</b></div>
		</li>
		<li>
			<div><b class="tr">vier</b></div>
		</li>
	</ol></w>`

	cases := []struct {
		name string
		sel  string
		want []string
	}{
		{"tag only", "b", []string{"eins", "zwei", "drei", "vier"}},
		{"class narrows tag", "b.tr", []string{"eins", "drei", "vier"}},
		{"class alone", ".tr", []string{"eins", "drei", "vier"}},
		{"descendant", "ol b.tr", []string{"eins", "drei", "vier"}},
		{"child skips grandchildren", "ol > li", []string{"eins zwei drei", "vier"}},
		{"nth-of-type", "li > div:nth-of-type(2)", []string{"drei"}},
		{"no match", "span.t", nil},
		{"unknown syntax matches nothing", "b[data-x=1]", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := queryText(t, src, c.sel); !reflect.DeepEqual(got, c.want) {
				t.Errorf("query(%q) = %q, want %q", c.sel, got, c.want)
			}
		})
	}
}

func TestSelectorLeadingChildIsRelativeToScope(t *testing.T) {
	// A profile's selectors are evaluated inside a scope, so a leading ">"
	// means "a direct child of this sense" - which is how the gloss block is
	// told apart from the example block below it.
	const src = `<w><ol><li>
		<div><b class="tr">Haus</b></div>
		<div class="ex"><ul><li><b class="tr">nicht</b></li></ul></div>
	</li></ol></w>`

	sense := queryFirst(parseFragment(t, src), parseSelector("ol > li"))
	if sense == nil {
		t.Fatal("no sense found")
	}
	got := query(sense, parseSelector("> div:nth-of-type(1) b.tr"))
	if len(got) != 1 || nodeText(got[0]) != "Haus" {
		t.Errorf("got %d nodes (first %q), want just Haus", len(got), nodeTextOrEmpty(got))
	}
}

func nodeTextOrEmpty(ns []*html.Node) string {
	if len(ns) == 0 {
		return ""
	}
	return nodeText(ns[0])
}

func TestSelectorEmptyMatchesNothing(t *testing.T) {
	// An unset profile field compiles to an empty selector, which every
	// caller treats as "this dictionary doesn't have that part" - it must
	// never mean "match everything".
	if got := query(parseFragment(t, `<w><b>x</b></w>`), parseSelector("")); got != nil {
		t.Errorf("empty selector matched %d nodes, want none", len(got))
	}
}

func TestRemoveMatchingDeletesOnlyRequestedClasses(t *testing.T) {
	doc := parseFragment(t, `<div><i class="gen">(der)</i><i class="reg">(fig.)</i>Haus</div>`)
	removeMatching(doc, parseSelector(".gen"))
	if got, want := nodeText(doc), "(fig.)Haus"; got != want {
		t.Errorf("after strip = %q, want %q", got, want)
	}
}

func TestDetectProfileSignatures(t *testing.T) {
	cases := []struct {
		name   string
		sample string
		want   string
	}{
		{"van dale translation class", `<b class="tr">Haus</b>`, "vandale"},
		{"van dale example class", `<div class="ex"><ul><li>x</li></ul></div>`, "vandale"},
		{"wikdict grammar font", `<ol><li><font class="grammar">x</font></li></ol>`, "wikdict"},
		{"numbered senses only", `<ol><li><div>Haus</div></li></ol>`, "nested"},
		{"no structure at all", `<w><a name="huis"/><b>huis</b><br/>house</w>`, "flat"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectProfile(c.sample); got != c.want {
				t.Errorf("detectProfile = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNormalizeDictHTMLClosesSelfClosingAnchor(t *testing.T) {
	// dictgen emits `<a name="x" />`, which is not a void element, so a
	// spec-compliant parser reparents everything after it into that anchor
	// and the first entry swallows the whole shard. The node count survives
	// this, so the test has to check that the entries stay siblings.
	const src = `<w><a name="een"/>1</w><w><a name="twee"/>2</w>`
	if got := len(query(parseFragment(t, src), parseSelector("w"))); got != 2 {
		t.Fatalf("unrepaired input already parsed as %d entries - test is not testing anything", got)
	}
	doc := parseFragment(t, normalizeDictHTML(src))
	words := query(doc, parseSelector("w"))
	if len(words) != 2 {
		t.Fatalf("got %d entries, want 2", len(words))
	}
	if got, want := nodeText(words[0]), "1"; got != want {
		t.Errorf("first entry = %q, want %q - the second was swallowed", got, want)
	}
}

func TestApplyProfileOverridesKeepsUnsetFields(t *testing.T) {
	base := builtinProfiles["vandale"]
	got := applyProfileOverrides(base, dictProfile{translation: []string{"i.foo"}})

	if !reflect.DeepEqual(got.translation, []string{"i.foo"}) {
		t.Errorf("translation = %q, want the override", got.translation)
	}
	if got.example != base.example || got.exampleTarget != base.exampleTarget || got.sense != base.sense {
		t.Errorf("overriding one selector dropped the rest of the base: %+v", got)
	}
}

func TestApplyProfileOverridesTranslationTextClearsSelectors(t *testing.T) {
	// The two are alternatives, not layers: a positional profile finds its
	// gloss by position and must not also run the base's selectors.
	got := applyProfileOverrides(builtinProfiles["vandale"], dictProfile{translationText: "afterLastBR"})
	if len(got.translation) != 0 {
		t.Errorf("translation = %q, want none once translation_text is set", got.translation)
	}
}

func TestDescribeProfileRoundTripsThroughConfig(t *testing.T) {
	// -test-dict prints a profile in [dict:] syntax so it can be pasted back
	// and adjusted. That is only true if reading it back yields the same
	// profile.
	want := builtinProfiles["wikdict"]

	var b strings.Builder
	b.WriteString("[dict:xx-yy]\nprofile=custom\n")
	for _, line := range describeProfile(want) {
		b.WriteString(line + "\n")
	}
	cfg := loadTestConfig(t, b.String())

	sec := cfg.Dicts["xx-yy"]
	if sec == nil {
		t.Fatal("no [dict:xx-yy] section parsed")
	}
	got := applyProfileOverrides(dictProfile{}, sec.Overrides)
	got.name = want.name // the name is the section key, not a described field
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip lost fields:\n got %+v\nwant %+v", got, want)
	}
}

func loadTestConfig(t *testing.T, body string) Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "anki-mywords.conf")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return loadConfig(p)
}

func TestLoadConfigParsesDictSection(t *testing.T) {
	cfg := loadTestConfig(t, strings.Join([]string{
		"[dict:de-fr]",
		"profile=custom",
		"sense=ol > li",
		"translation=b.tr | div > i",
		"translation_reject=font.grammar",
		"example=div.ex ul li",
		"example_target=span.t",
		"article=i.art",
		"drop_tags=med,sport",
		"split_on=;",
	}, "\n")+"\n")

	sec := cfg.Dicts["de-fr"]
	if sec == nil {
		t.Fatal("no [dict:de-fr] section parsed")
	}
	if sec.Base != "custom" || !sec.HasFields {
		t.Errorf("base = %q hasFields = %v, want custom/true", sec.Base, sec.HasFields)
	}
	o := sec.Overrides
	if o.sense != "ol > li" {
		t.Errorf("sense = %q", o.sense)
	}
	if !reflect.DeepEqual(o.translation, []string{"b.tr", "div > i"}) {
		t.Errorf("translation = %q, want the chain split on |", o.translation)
	}
	if o.translationDrop != "font.grammar" || o.example != "div.ex ul li" ||
		o.exampleTarget != "span.t" || o.article != "i.art" || o.splitOn != ";" {
		t.Errorf("selectors = %+v", o)
	}
	if !reflect.DeepEqual(o.dropTags, []string{"med", "sport"}) {
		t.Errorf("drop_tags = %q", o.dropTags)
	}
}

func TestLoadConfigKeepsSelectorsDespiteATypoBelowThem(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	cfg := loadTestConfig(t, "[dict:de-fr]\nprofile=custom\nsense=ol > li\nexampel=div.ex\n")
	log.SetOutput(old)

	if !strings.Contains(buf.String(), "exampel") {
		t.Errorf("the misspelled key was not reported:\n%s", buf.String())
	}
	sec := cfg.Dicts["de-fr"]
	if sec == nil || !sec.HasFields || sec.Overrides.sense != "ol > li" {
		t.Errorf("a typo discarded the valid selectors above it: %+v", sec)
	}
}

func TestLoadConfigRejectsUnknownProfileName(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	cfg := loadTestConfig(t, "[dict:de-fr]\nprofile=vandale2\n")
	log.SetOutput(old)

	if !strings.Contains(buf.String(), "vandale2") {
		t.Errorf("a misspelled profile was accepted silently:\n%s", buf.String())
	}
	// A typo must not silently become a custom profile with no selectors,
	// which would parse the dictionary into nothing.
	if sec := cfg.Dicts["de-fr"]; sec != nil && sec.HasFields {
		t.Errorf("rejected profile still set fields: %+v", sec)
	}
}

// writeProfileTestDict builds a dictionary in each family's markup, for the
// resolution tests below - they have to sniff a real file.
func writeProfileTestDict(t *testing.T, body string) string {
	t.Helper()
	return writeTestDictZip(t, body)
}

const vandaleTestEntry = `<w><a name="huis"/><b>huis</b> <i class="art">(het)</i><ol><li>` +
	`<div><b class="tr">Haus</b></div>` +
	`<div class="ex"><ul><li>een mooi huis<br/><span class="t">ein schönes Haus</span></li></ul></div>` +
	`</li></ol></w>`

const flatTestEntry = `<w><a name="huis"/><b>huis</b><br/>house</w>`

func TestResolveDictProfileDetectsFromMarkup(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"vandale", vandaleTestEntry, "vandale"},
		{"flat", flatTestEntry, "flat"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, origin := resolveDictProfile(Config{}, "nl-de", writeProfileTestDict(t, c.body), "")
			if p.name != c.want || origin != "detected" {
				t.Errorf("got %q (%s), want %q (detected)", p.name, origin, c.want)
			}
		})
	}
}

func TestResolveDictProfileConfiguredBeatsDetected(t *testing.T) {
	path := writeProfileTestDict(t, vandaleTestEntry)
	cfg := loadTestConfig(t, "[dict:nl-de]\nprofile=flat\n")

	p, origin := resolveDictProfile(cfg, "nl-de", path, "")
	if p.name != "flat" || origin != "configured" {
		t.Errorf("got %q (%s), want flat (configured) - the config must win over the sniffer", p.name, origin)
	}
}

func TestResolveDictProfileLayersOverridesOnBase(t *testing.T) {
	path := writeProfileTestDict(t, vandaleTestEntry)
	cfg := loadTestConfig(t, "[dict:nl-de]\nprofile=vandale\nexample_target=span.foreign\n")

	p, origin := resolveDictProfile(cfg, "nl-de", path, "")
	if origin != "configured + overrides" {
		t.Errorf("origin = %q", origin)
	}
	if p.exampleTarget != "span.foreign" {
		t.Errorf("example_target = %q, want the override", p.exampleTarget)
	}
	if p.example != builtinProfiles["vandale"].example {
		t.Errorf("example = %q, want the base's - overriding one field must not clear the rest", p.example)
	}
}

func TestResolveDictProfileAutoStillSniffs(t *testing.T) {
	path := writeProfileTestDict(t, vandaleTestEntry)
	cfg := loadTestConfig(t, "[dict:nl-de]\nprofile=auto\n")

	if p, origin := resolveDictProfile(cfg, "nl-de", path, ""); p.name != "vandale" || origin != "detected" {
		t.Errorf("got %q (%s), want vandale (detected)", p.name, origin)
	}
}

func TestResolveDictProfileCustomStartsEmpty(t *testing.T) {
	// profile=custom with selectors of its own must not inherit the sniffed
	// dictionary's, or a deliberate description would be silently mixed with
	// a guessed one.
	path := writeProfileTestDict(t, vandaleTestEntry)
	cfg := loadTestConfig(t, "[dict:nl-de]\nprofile=custom\ntranslation=b.tr\n")

	p, origin := resolveDictProfile(cfg, "nl-de", path, "")
	if origin != "custom" {
		t.Errorf("origin = %q, want custom", origin)
	}
	if p.sense != "" || p.example != "" || p.article != "" {
		t.Errorf("custom profile inherited vandale's selectors: %+v", p)
	}
}

func TestResolveDictProfileAddsTranslationTagAsLastResort(t *testing.T) {
	// translation_tag predates profiles. A dictionary that marks its
	// translations must still prefer its own marker, with the bare tag only
	// as a fallback.
	path := writeProfileTestDict(t, vandaleTestEntry)
	p, _ := resolveDictProfile(Config{}, "nl-de", path, "b")

	want := []string{"> div:nth-of-type(1) b.tr", "> div:nth-of-type(1) b"}
	if !reflect.DeepEqual(p.translation, want) {
		t.Errorf("translation = %q, want %q", p.translation, want)
	}
}

func TestResolveDictProfileIgnoresTranslationTagForPositionalDict(t *testing.T) {
	// A flat dict bolds its repeated headword, so honouring translation_tag
	// there would turn every definition into the word itself.
	path := writeProfileTestDict(t, flatTestEntry)
	p, _ := resolveDictProfile(Config{}, "nl-en", path, "b")

	if len(p.translation) != 0 {
		t.Errorf("translation = %q, want none for a positional profile", p.translation)
	}
}

func TestBuiltinProfilesParseTheirOwnFamily(t *testing.T) {
	// The end-to-end check: each shipped profile, applied to the markup it
	// describes, yields the headword and the translation.
	cases := []struct {
		profile string
		body    string
		word    string
		def     string
	}{
		{"vandale", vandaleTestEntry, "huis", "Haus"},
		{"flat", flatTestEntry, "huis", "house"},
		{
			"wikdict",
			`<w><a name="huis"/><b>huis</b><ol><li><div><div><div>house</div></div></div></li></ol></w>`,
			"huis", "house",
		},
		{
			"nested",
			`<w><a name="huis"/><b>huis</b><ol><li><div>house</div></li></ol></w>`,
			"huis", "house",
		},
	}
	for _, c := range cases {
		t.Run(c.profile, func(t *testing.T) {
			p := builtinProfiles[c.profile]
			dict, err := parseDict(writeProfileTestDict(t, c.body),
				compileProfile(p, nil),
				dictRender{maxSenses: 1, maxDepth: 1, maxChars: 150})
			if err != nil {
				t.Fatalf("parseDict: %v", err)
			}
			if got := dict.Entries[c.word]; got != c.def {
				t.Errorf("%s -> %q, want %q", c.word, got, c.def)
			}
		})
	}
}

func TestWikdictProfileRejectsInflectionTables(t *testing.T) {
	// <font class="grammar"> sits exactly where a translation sits.
	const body = `<w><a name="huis"/><b>huis</b><ol><li>` +
		`<div><div><div><font class="grammar">huizen</font></div></div></div>` +
		`<div><div><div>house</div></div></div>` +
		`</li></ol></w>`

	dict, err := parseDict(writeProfileTestDict(t, body),
		compileProfile(builtinProfiles["wikdict"], nil),
		dictRender{maxSenses: 1, maxDepth: 1, maxChars: 150})
	if err != nil {
		t.Fatalf("parseDict: %v", err)
	}
	if got := dict.Entries["huis"]; got != "house" {
		t.Errorf("huis -> %q, want %q - the inflection table was taken as a translation", got, "house")
	}
}

func TestCardIsNotBuiltFromAPositionalProfile(t *testing.T) {
	// dict_examples can be set for a language whose other dictionary is
	// flat. Comma-splitting a positional gloss would invent alternatives
	// that aren't in the dictionary, so the flat one stays a definition.
	const body = `<w><a name="huis"/><b>huis</b><br/>house, home</w>`
	dict, err := parseDict(writeProfileTestDict(t, body),
		compileProfile(builtinProfiles["flat"], nil),
		dictRender{maxSenses: 1, maxDepth: 1, maxChars: 150, asCard: true, cardFormat: defaultCardFormat()})
	if err != nil {
		t.Fatalf("parseDict: %v", err)
	}
	if front, ok := dict.Fronts["huis"]; ok {
		t.Errorf("flat dict produced a card front %q", front)
	}
	if got := dict.Entries["huis"]; got != "house, home" {
		t.Errorf("huis -> %q, want the gloss as written", got)
	}
}

func TestDictPairsUnionsInstalledAndConfigured(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"dicthtml-nl-de.zip", "dicthtml-nl-en.zip", "notadict.zip"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	cfg := Config{DictDir: dir, Dicts: map[string]*DictSection{
		"nl-de": {}, // already installed, must not be listed twice
		"de-fr": {}, // configured but not installed
	}}

	want := []string{"de-fr", "nl-de", "nl-en"}
	if got := dictPairs(cfg); !reflect.DeepEqual(got, want) {
		t.Errorf("dictPairs = %q, want %q", got, want)
	}
}
