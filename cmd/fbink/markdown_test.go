package main

import "testing"

func TestHeadwordOnly(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want string
	}{
		{
			"headword, alternatives, and example",
			"**ruim**, wijd, breed\n*een ruime kamer.*",
			"**ruim**",
		},
		{
			"headword only",
			"**afkicken**",
			"**afkicken**",
		},
		{
			"multi-word bold headword",
			"**van a tot z**, alles\n*van a tot z.*",
			"**van a tot z**",
		},
		{
			"no bold run at all - falls back to the full string",
			"plain text, no headword marked up",
			"plain text, no headword marked up",
		},
		{
			"empty string",
			"",
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := headwordOnly(c.md); got != c.want {
				t.Errorf("headwordOnly(%q) = %q, want %q", c.md, got, c.want)
			}
		})
	}
}
