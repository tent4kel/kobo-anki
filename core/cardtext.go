package core

import "regexp"

// leadingBoldRe matches a **bold** run anchored at the very start of a card
// face. kobo-vocab's card_format always opens with the headword in bold when
// there is one at all (see DefaultCardFormat), so the first such run is the
// headword and nothing else.
var leadingBoldRe = regexp.MustCompile(`(?s)^\*\*(.+?)\*\*`)

// HeadwordOnly reduces a formatted card face to just its bold headword,
// dropping the alternatives and example that follow.
//
// Used for the small grey "which word is this?" line on the back of a card,
// which should not restate the entire front now that fronts can carry
// alternatives and an example sentence. Both review UIs call this so the
// e-ink screen and the browser agree: they previously had separate
// implementations that disagreed, with "**een** **twee**, drie" yielding
// "**een twee**" on one and "**een**" on the other.
//
// A face that does not begin with a bold run - a custom card_format that
// styles the headword differently, or a plain hand-written card - is
// returned unchanged, so the label shows something rather than going blank.
func HeadwordOnly(cardFace string) string {
	m := leadingBoldRe.FindStringSubmatch(cardFace)
	if m == nil {
		return cardFace
	}
	return "**" + m[1] + "**"
}
