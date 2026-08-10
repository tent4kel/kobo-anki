package core

import "testing"

func TestHeadwordOnly(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"headword, alternatives, and example",
			"**ruim**\nwijd, breed\n*een ruime kamer.*",
			"**ruim**",
		},
		{"headword alone", "**afkicken**", "**afkicken**"},
		{
			"multi-word headword is one bold run",
			"**van a tot z**\nalles\n*van a tot z.*",
			"**van a tot z**",
		},
		{
			// The case where the two old implementations disagreed: the AST
			// walk in cmd/fbink merged adjacent bold runs into
			// "**een twee**", the regex in cmd/server took only the first.
			// Only the first run is the headword - card_format emits it as a
			// single **...** span - so the regex behaviour is the correct one.
			"two adjacent bold runs - only the first is the headword",
			"**een** **twee**, drie",
			"**een**",
		},
		{
			"no leading bold run - returned unchanged",
			"plain text, no headword marked up",
			"plain text, no headword marked up",
		},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HeadwordOnly(c.in); got != c.want {
				t.Errorf("HeadwordOnly(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
