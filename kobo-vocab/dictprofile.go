package main

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// A dictProfile describes where one dictionary keeps the parts of an entry.
// It is pure data: everything that used to be a structural guess in this
// binary - "does this dict nest its senses in <li>, or lay them out flat?",
// "is bold a translation or a repeated headword?" - is stated here instead,
// so a dictionary this project has never seen can be supported by writing a
// [dict:xx-yy] section rather than by patching the parser.
//
// Selectors are the small CSS subset in selector.go and are evaluated
// relative to the enclosing scope: entry within the shard, sense within the
// entry, and translation/example/article within the sense.
type dictProfile struct {
	name string

	entry    string // one dictionary entry; the Kobo format fixes this at "w"
	headAttr string // "tag@attr" holding the headword, i.e. "a@name"

	// sense selects one meaning. Empty means the whole entry is one sense,
	// which is the shape of every dictionary that doesn't number its senses.
	sense string

	// translation is a chain tried in order, first match wins. A chain
	// rather than one selector because a single generator can emit
	// different shapes for simple and rich entries - WikDict does, and a
	// single selector reaches only 72% of its headwords.
	translation []string

	// translationDrop rejects a candidate that contains this selector:
	// markup that sits where a translation sits but isn't one.
	translationDrop string

	// translationText replaces selector matching for dictionaries that mark
	// up nothing at all and rely on position. Only "afterLastBR" so far.
	translationText string

	// example is one illustrative phrase; its source half is the text
	// before the <br> and its target half exampleTarget, or everything
	// after the <br> when the dictionary doesn't mark which is which.
	example       string
	exampleTarget string

	// article is the source-language article, shown as part of the headword
	// ("het huis") because that is how the noun has to be learned.
	article string

	// dropTags removes custom inline elements some dictionaries use as
	// subject markers (<med>, <sport>) before any text is taken.
	dropTags []string

	// splitOn is an extra separator between translations, for dictionaries
	// that use something other than a comma.
	splitOn string
}

// builtinProfiles are the shapes worth shipping. They are ordinary
// profiles, not special cases: `-print-config` expands them into the same
// selector syntax a user would write, so a built-in profile doubles as a
// worked example and as a spec for anyone generating their own dictionary.
var builtinProfiles = map[string]dictProfile{
	// This project's own build (see CLASSES.md next to extractor.py). It
	// needs no fallbacks and no rejects, which is the whole advantage of
	// generating the dictionary yourself: every entry has the same shape,
	// so nothing has to be guessed.
	"vandale": {
		name:          "vandale",
		sense:         "ol > li",
		article:       "i.art",
		example:       "div.ex ul li",
		exampleTarget: "span.t",
		translation:   []string{"> div:nth-of-type(1) b.tr"},
	},

	// Conversions from other formats (StarDict and friends) that carry no
	// structural markup: the entry is a header, a <br>, and the gloss.
	"flat": {
		name:            "flat",
		translationText: "afterLastBR",
	},

	// A dictionary that numbers its senses but marks nothing else: the
	// sense list is the only structure there is, so translations come from
	// translation_tag if one is set and from the whole gloss otherwise.
	"nested": {
		name:  "nested",
		sense: "ol > li",
	},

	// WikDict's generated dictionaries - https://download.wikdict.com.
	// Rich entries nest a second <ol> of translations inside the sense;
	// simple ones just trail a bare <div>, hence the chain. <font
	// class="grammar"> holds inflection tables, which sit exactly where a
	// translation sits.
	"wikdict": {
		name:            "wikdict",
		sense:           "ol > li",
		translation:     []string{"ol:nth-of-type(2) > li > div", "div > div > div"},
		translationDrop: "font.grammar",
	},
}

// profileNames is the set a config value is checked against.
func profileNames() []string {
	return []string{"vandale", "flat", "nested", "wikdict", "auto", "custom"}
}

// detectProfile picks a profile by looking at the markup itself, so the
// common case needs no configuration at all. The signatures are ordered
// most-specific first: an explicit class vocabulary identifies its
// generator, and only when none is present does the shape of the entry
// decide.
func detectProfile(sample string) string {
	switch {
	case strings.Contains(sample, `class="tr"`), strings.Contains(sample, `class="ex"`):
		return "vandale"
	case strings.Contains(sample, `class="grammar"`):
		return "wikdict"
	case strings.Contains(sample, "<ol"):
		return "nested"
	default:
		return "flat"
	}
}

// selfClosingAnchorRe repairs `<a name="x" />`, which dictgen emits and
// which x/net/html - correctly, per the HTML5 spec, since <a> is not a void
// element - treats as an unclosed <a>. The adoption agency algorithm then
// reparents the rest of the shard inside that anchor, so the first entry
// swallows every entry after it. The element count survives this, which is
// why a naive check ("same number of <w> nodes") reports success while the
// content is destroyed.
var selfClosingAnchorRe = regexp.MustCompile(`<a([^>]*?)\s*/>`)

func normalizeDictHTML(s string) string {
	return selfClosingAnchorRe.ReplaceAllString(s, "<a$1></a>")
}

// parsedSense is one meaning of a headword.
type parsedSense struct {
	translations []string
	blocks       []string // cleaned text of each top-level block, for max_depth
	exSource     string
	exTarget     string
}

// parsedEntry is one <w> block after extraction, before any card formatting.
type parsedEntry struct {
	word    string
	article string
	senses  []parsedSense
}

// compiledProfile holds a profile with its selectors already parsed, since a
// dictionary load runs these over tens of thousands of entries.
type compiledProfile struct {
	p           dictProfile
	entry       selector
	headTag     selector
	headAttrKey string
	sense       selector
	translation []selector
	transDrop   selector
	example     selector
	exTarget    selector
	article     selector
	dropTags    []selector
	stripLabels []selector
}

func compileProfile(p dictProfile, stripLabelClasses []string) *compiledProfile {
	c := &compiledProfile{p: p}

	entry := p.entry
	if entry == "" {
		entry = "w" // fixed by the Kobo dictzip format
	}
	c.entry = parseSelector(entry)

	head := p.headAttr
	if head == "" {
		head = "a@name"
	}
	if i := strings.Index(head, "@"); i >= 0 {
		c.headTag = parseSelector(head[:i])
		c.headAttrKey = head[i+1:]
	}

	c.sense = parseSelector(p.sense)
	for _, t := range p.translation {
		c.translation = append(c.translation, parseSelector(t))
	}
	c.transDrop = parseSelector(p.translationDrop)
	c.example = parseSelector(p.example)
	c.exTarget = parseSelector(p.exampleTarget)
	c.article = parseSelector(p.article)
	for _, t := range p.dropTags {
		c.dropTags = append(c.dropTags, parseSelector(t))
	}
	// Label stripping is a user preference (dict_strip_labels), not a
	// property of the dictionary, so it arrives separately and is applied
	// by class alone - any dictionary that marks its annotations the same
	// way gets the same behaviour without declaring anything.
	for _, cl := range stripLabelClasses {
		c.stripLabels = append(c.stripLabels, parseSelector("."+cl))
	}
	return c
}

var parenRe = regexp.MustCompile(`^\((.*)\)$`)

// extractProfiled pulls one entry out of its parsed node.
func extractProfiled(c *compiledProfile, e *html.Node) (parsedEntry, bool) {
	var out parsedEntry

	if len(c.headTag) > 0 {
		for _, n := range query(e, c.headTag) {
			if v := attr(n, c.headAttrKey); v != "" {
				out.word = strings.ToLower(v)
				break
			}
		}
	}
	if out.word == "" {
		return out, false
	}

	// The article lives on the entry's headword line, outside any sense.
	if len(c.article) > 0 {
		if n := queryFirst(e, c.article); n != nil {
			a := nodeText(n)
			if m := parenRe.FindStringSubmatch(a); m != nil {
				a = m[1]
			}
			out.article = strings.TrimSpace(a)
		}
	}

	for _, sel := range c.dropTags {
		removeMatching(e, sel)
	}
	for _, sel := range c.stripLabels {
		removeMatching(e, sel)
	}

	senses := []*html.Node{e}
	if len(c.sense) > 0 {
		if s := query(e, c.sense); len(s) > 0 {
			senses = s
		}
	}

	for _, s := range senses {
		out.senses = append(out.senses, extractSense(c, s))
	}
	return out, true
}

func extractSense(c *compiledProfile, s *html.Node) parsedSense {
	var ps parsedSense

	if c.p.translationText == "afterLastBR" {
		ps.translations = splitTranslations(textAfterLastBR(s), c.p.splitOn)
		// The unsplit remainder is what a plain definition shows, kept as
		// markup so emphasis survives into markdown like any other block.
		if t := cleanText(htmlAfterLastBR(innerHTML(s))); t != "" {
			ps.blocks = []string{t}
		}
	} else {
		for _, chain := range c.translation {
			for _, n := range query(s, chain) {
				if len(c.transDrop) > 0 && len(query(n, c.transDrop)) > 0 {
					continue
				}
				if t := nodeText(n); t != "" {
					ps.translations = append(ps.translations, t)
				}
			}
			if len(ps.translations) > 0 {
				break
			}
		}
		ps.blocks = topLevelBlockText(s)
	}

	if len(c.example) > 0 {
		if li := queryFirst(s, c.example); li != nil {
			src := textBeforeBR(li)
			tgt := ""
			if len(c.exTarget) > 0 {
				if tn := queryFirst(li, c.exTarget); tn != nil {
					tgt = nodeText(tn)
				}
			}
			if tgt == "" {
				tgt = textAfterLastBR(li)
			}
			// A half-parsed example is worth skipping, not worth showing
			// broken - matches the previous splitExample behaviour.
			if src != "" && tgt != "" {
				ps.exSource, ps.exTarget = src, tgt
			}
		}
	}
	return ps
}

var wsRe = regexp.MustCompile(`\s+`)

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(wsRe.ReplaceAllString(b.String(), " "))
}

// topLevelBlockText is the text of each direct child element of a sense,
// which is what max_depth counts: the first is the gloss, a second (if any)
// the examples.
//
// These go through cleanText rather than nodeText because they are the
// fallback shown when no translation selector matches, and there the whole
// formatted definition is the content - <br> line breaks and <i>/<b>
// emphasis carried into markdown, exactly as the pre-profile parser did.
func topLevelBlockText(s *html.Node) []string {
	var out []string
	for c := s.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if t := cleanText(outerHTML(c)); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		if t := cleanText(innerHTML(s)); t != "" {
			out = []string{t}
		}
	}
	return out
}

// outerHTML/innerHTML re-serialize a node so the existing text pipeline -
// which works on HTML source, not on nodes - can be reused unchanged.
func outerHTML(n *html.Node) string {
	var b strings.Builder
	if err := html.Render(&b, n); err != nil {
		return ""
	}
	return b.String()
}

func innerHTML(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&b, c); err != nil {
			return ""
		}
	}
	return b.String()
}

func textBeforeBR(n *html.Node) string {
	var b strings.Builder
	done := false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if done {
				return
			}
			if c.Type == html.ElementNode && c.Data == "br" {
				done = true
				return
			}
			if c.Type == html.TextNode {
				b.WriteString(c.Data)
			} else {
				walk(c)
			}
		}
	}
	walk(n)
	return strings.TrimSpace(wsRe.ReplaceAllString(b.String(), " "))
}

// textAfterLastBR returns the text following the last <br> below n - the
// shape used by dictionaries that mark up nothing and rely on position.
func textAfterLastBR(n *html.Node) string {
	const mark = "\x00"
	var parts []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			switch {
			case c.Type == html.ElementNode && c.Data == "br":
				parts = append(parts, mark)
			case c.Type == html.TextNode:
				parts = append(parts, c.Data)
			default:
				walk(c)
			}
		}
	}
	walk(n)
	joined := strings.Join(parts, "")
	if i := strings.LastIndex(joined, mark); i >= 0 {
		joined = joined[i+1:]
	}
	// Some conversions pack "word [phonetic]\n\tgloss" into that final
	// segment with no tag boundary between the two.
	if i := strings.LastIndex(joined, "\n"); i >= 0 {
		if tail := strings.TrimSpace(joined[i+1:]); tail != "" {
			joined = tail
		}
	}
	return strings.TrimSpace(wsRe.ReplaceAllString(joined, " "))
}

// htmlAfterLastBR is textAfterLastBR's markup-preserving twin: it cuts the
// source at the last <br> instead of flattening it, so the remainder can go
// through cleanText and keep its emphasis.
func htmlAfterLastBR(src string) string {
	last := src
	if i := strings.LastIndex(strings.ToLower(src), "<br"); i >= 0 {
		last = src[i:]
		if gt := strings.Index(last, ">"); gt >= 0 {
			last = last[gt+1:]
		}
	}
	// Some conversions pack "word [phonetic]\n\tgloss" into that final
	// segment with no tag boundary between the two.
	if i := strings.LastIndex(last, "\n"); i >= 0 {
		if tail := strings.TrimSpace(last[i+1:]); tail != "" {
			last = tail
		}
	}
	return last
}

var bracketRe = regexp.MustCompile(`\[[^\]]*\]`)

func splitTranslations(s, extra string) []string {
	s = bracketRe.ReplaceAllString(s, "")
	parts := []string{s}
	seps := []string{","}
	if extra != "" {
		seps = append(seps, extra)
	}
	for _, sep := range seps {
		var next []string
		for _, p := range parts {
			next = append(next, strings.Split(p, sep)...)
		}
		parts = next
	}
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(wsRe.ReplaceAllString(p, " ")); p != "" {
			out = append(out, p)
		}
	}
	return out
}
