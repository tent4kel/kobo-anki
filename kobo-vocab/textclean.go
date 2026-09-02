package main

import (
	"html"
	"regexp"
	"strings"
)

// --- Text cleanup ---

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)
var whitespaceRe = regexp.MustCompile(`\s+`)

var inlineTagRe = regexp.MustCompile(`(?i)<b>|</b>|<i>|</i>|<br\s*/?>`)
var mergeItalicRe = regexp.MustCompile(`(?i)</i>(\s*)<i>`)
var mergeBoldRe = regexp.MustCompile(`(?i)</b>(\s*)<b>`)

// brSentinel stands in for a <br> line break through the tag-strip and
// whitespace-collapse steps in cleanText (a literal '\n' would just get
// collapsed away by whitespaceRe), then gets restored to '\n' at the end.
const brSentinel = "\x00BR\x00"

// cleanPlainText strips all tags to bare text with no Markdown conversion,
// unlike cleanText - for a card's example halves, which get their own outer
// *italic* wrapping from formatCard. Converting an inner
// "<i>(note)</i>" to markdown too, as cleanText would, nests emphasis
// markers into broken output like "**(ook)* text*" - verified against
// "verschrikkelijk"'s second sense, whose German half opens with a bare
// "<i>(ook)</i>" usage note.
func cleanPlainText(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = whitespaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func cleanText(s string) string {
	s = toMarkdown(s)
	s = htmlTagRe.ReplaceAllString(s, " ")
	// Unescape entities (&amp; &lt; &nbsp; &#233; ...) after tags are gone,
	// so an entity that decodes to '<' or '>' can't be mistaken for a tag.
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ") // &nbsp; decodes to NBSP, not ASCII space
	s = whitespaceRe.ReplaceAllString(s, " ")
	// Restore <br> line breaks after whitespace collapse (must happen last,
	// or whitespaceRe would have collapsed a literal '\n' back into a space).
	s = strings.ReplaceAll(s, " "+brSentinel, brSentinel)
	s = strings.ReplaceAll(s, brSentinel+" ", brSentinel)
	s = strings.ReplaceAll(s, brSentinel, "\n")
	return strings.TrimSpace(s)
}

// toMarkdown converts the small set of inline tags dictionary entries
// actually use into Markdown markers, before htmlTagRe strips everything
// else. <b>/<i> become **/*, handling two real-world quirks of this source:
//
//   - Adjacent tag boundaries: "</b><i>...</i>" would naively produce an
//     ambiguous run of literal asterisks ("**word***...") that CommonMark
//     can't parse the way we intend - a zero-width space is inserted
//     whenever a new marker would land directly against an existing one.
//   - Whitespace hugging a tag boundary: spans like "<i> (die)</i>" put a
//     leading space *inside* the tag. CommonMark requires an opening
//     delimiter not be followed by whitespace (and a closing one not be
//     preceded by it), so naively emitting "* (die)*" fails to parse as
//     emphasis at all. Hugging whitespace is relocated outside the marker.
//   - Adjacent same-type spans: this source often wraps runs of text as many
//     separate single-word/punctuation <i>...</i> pairs in a row (e.g.
//     "<i> (</i><i>die</i><i>;</i>"). Converting each independently produces
//     a long run of single-'*' spans that CommonMark's emphasis resolution
//     can pair up wrong (nesting instead of a flat sequence). Adjacent
//     same-type tag pairs are merged into one span first, which changes
//     nothing about what text ends up styled, only how many span
//     boundaries there are to convert.
//
// Known accepted limitation: a single '*' marker landing directly against
// punctuation/digits with no space (e.g. "CO<i>2</i>-neutraal") can still
// occasionally fail CommonMark's flanking rules and render as literal
// asterisks - verified against a full real dictionary (102,272 entries)
// at ~0.005% of entries, always as a few stray visible '*' characters in
// otherwise-readable text, never lost content or a parse error. Not worth
// reimplementing CommonMark's delimiter-run resolution to close that gap.
func toMarkdown(s string) string {
	s = mergeItalicRe.ReplaceAllString(s, "$1")
	s = mergeBoldRe.ReplaceAllString(s, "$1")

	out := ""
	last := 0
	for _, m := range inlineTagRe.FindAllStringIndex(s, -1) {
		if m[0] > last {
			out += s[last:m[0]]
		}
		tag := strings.ToLower(s[m[0]:m[1]])
		last = m[1]

		var marker string
		var isOpen bool
		switch tag {
		case "<b>":
			marker, isOpen = "**", true
		case "</b>":
			marker, isOpen = "**", false
		case "<i>":
			marker, isOpen = "*", true
		case "</i>":
			marker, isOpen = "*", false
		default: // <br>, <br/>, <br />
			out += brSentinel
			continue
		}

		if isOpen {
			skip := 0
			for last+skip < len(s) && isMDSpace(s[last+skip]) {
				skip++
			}
			out += s[last : last+skip] // whitespace moves before the marker
			last += skip
			if strings.HasSuffix(out, "*") {
				out += "\u200b"
			}
			out += marker
		} else {
			trimmed := strings.TrimRight(out, " \t\n")
			ws := out[len(trimmed):] // whitespace moves after the marker
			out = trimmed
			if strings.HasSuffix(out, "*") {
				out += "\u200b"
			}
			out += marker + ws
		}
	}
	out += s[last:]
	return out
}

func isMDSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' }

// truncateMarkdownVisible truncates s to roughly maxChars visible (non-'*')
// characters, extending the cut to the next word boundary with a balanced
// '*' count. kobo-vocab only ever emits well-formed, self-paired ** and *
// markers, so an even running count of '*' runes means we're not currently
// inside an open emphasis span - a global counter is enough, no need for
// full CommonMark delimiter-run resolution. This guarantees a cut never
// leaves a dangling emphasis marker or an empty trailing "-" list item.
func truncateMarkdownVisible(s string, maxChars int) string {
	if maxChars <= 0 {
		return s
	}

	runes := []rune(s)
	visible, stars, cut := 0, 0, -1

	for i, r := range runes {
		if r == '*' {
			stars++
		} else {
			visible++
		}
		atBoundary := i+1 == len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n'
		if visible >= maxChars && stars%2 == 0 && atBoundary {
			cut = i + 1
			break
		}
	}
	if cut < 0 || cut >= len(runes) {
		return s
	}

	result := strings.TrimRight(string(runes[:cut]), " \n")
	if idx := strings.LastIndexByte(result, '\n'); idx >= 0 {
		if strings.TrimSpace(result[idx+1:]) == "-" {
			result = strings.TrimRight(result[:idx], " \n")
		}
	} else if strings.TrimSpace(result) == "-" {
		result = ""
	}
	return result
}

func stripPunctuation(word string) string {
	return strings.TrimRight(strings.TrimLeft(word, "\"'([{¿¡"), ".,;:!?\"')]}…")
}
