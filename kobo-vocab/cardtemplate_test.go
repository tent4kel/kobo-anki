package main

import "testing"

func TestCardTemplateDefaultMatchesOldFormatCard(t *testing.T) {
	tpl, err := parseCardTemplate(DefaultCardFormat)
	if err != nil {
		t.Fatalf("parseCardTemplate(DefaultCardFormat): %v", err)
	}
	cases := []struct {
		name         string
		headword     string
		alternatives []string
		example      string
		want         string
	}{
		{"all present", "lopen", []string{"stappen", "wandelen"}, "Zij liepen.", "**lopen**, stappen, wandelen *Zij liepen.*"},
		{"headword only", "lopen", nil, "", "**lopen**"},
		{"headword and example, no alternatives", "lopen", nil, "Zij liepen.", "**lopen** *Zij liepen.*"},
		{"headword and alternatives, no example", "lopen", []string{"stappen"}, "", "**lopen**, stappen"},
		{"everything empty", "", nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tpl.render(c.headword, c.alternatives, c.example)
			if got != c.want {
				t.Errorf("render(%q, %v, %q) = %q, want %q", c.headword, c.alternatives, c.example, got, c.want)
			}
		})
	}
}

func TestCardTemplateCustomReordering(t *testing.T) {
	// A user could reorder/restyle the card entirely - e.g. example first,
	// underlined headword instead of bold, alternatives in parens.
	tpl, err := parseCardTemplate("[*{ex}* — ]{head}[ ({alts})]")
	if err != nil {
		t.Fatalf("parseCardTemplate: %v", err)
	}
	got := tpl.render("lopen", []string{"stappen", "wandelen"}, "Zij liepen.")
	want := "*Zij liepen.* — lopen (stappen, wandelen)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// No example - the "[*{ex}* — ]" block vanishes entirely, no dangling
	// "— " left behind. head itself is outside any bracket, so it's always
	// emitted even with no alternatives.
	got = tpl.render("lopen", nil, "")
	want = "lopen"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCardTemplateOptionalBlockRequiresAllPlaceholders(t *testing.T) {
	// A bracketed block with more than one placeholder is dropped unless
	// ALL of them are non-empty (AND semantics) - avoids a half-filled
	// fragment like a dangling "lopen: " when only one side is present.
	tpl, err := parseCardTemplate("{head}[: {alts} / {ex}]")
	if err != nil {
		t.Fatalf("parseCardTemplate: %v", err)
	}
	if got := tpl.render("lopen", []string{"gaan"}, ""); got != "lopen" {
		t.Errorf("got %q, want %q (block dropped - ex is empty)", got, "lopen")
	}
	if got := tpl.render("lopen", []string{"gaan"}, "Ik loop."); got != "lopen: gaan / Ik loop." {
		t.Errorf("got %q, want the full block (both placeholders present)", got)
	}
}

func TestCardTemplateLiteralOnlyBracketAlwaysIncluded(t *testing.T) {
	// A bracketed run with no placeholder at all has nothing to condition
	// on, so it's always included - not a realistic template, but should
	// degrade sensibly rather than error or vanish unpredictably.
	tpl, err := parseCardTemplate("{head}[!]")
	if err != nil {
		t.Fatalf("parseCardTemplate: %v", err)
	}
	if got := tpl.render("lopen", nil, ""); got != "lopen!" {
		t.Errorf("got %q, want %q", got, "lopen!")
	}
}

func TestCardTemplateParseErrors(t *testing.T) {
	cases := []struct {
		name string
		tpl  string
	}{
		{"unclosed brace", "{head"},
		{"unknown placeholder", "{foo}"},
		{"nested bracket", "[[{head}]]"},
		{"unmatched close bracket", "{head}]"},
		{"unclosed bracket", "[{head}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseCardTemplate(c.tpl); err == nil {
				t.Errorf("parseCardTemplate(%q): expected an error", c.tpl)
			}
		})
	}
}

func TestCompileCardFormatOrDefaultFallsBackOnBadInput(t *testing.T) {
	// A typo in card_format shouldn't break every provider that shares it -
	// falls back to DefaultCardFormat instead of propagating the error.
	tpl := compileCardFormatOrDefault("{nonsense}")
	want := compileCardFormatOrDefault(DefaultCardFormat)
	if tpl.render("lopen", nil, "Zij liepen.") != want.render("lopen", nil, "Zij liepen.") {
		t.Errorf("bad card_format did not fall back to the default template")
	}
}

func TestCompileCardFormatOrDefaultEmptyString(t *testing.T) {
	// An unset card_format (empty string, e.g. Config's zero value before
	// loadConfig's default kicks in) also falls back to the default rather
	// than compiling into a template that emits nothing.
	tpl := compileCardFormatOrDefault("")
	if got := tpl.render("lopen", nil, ""); got != "**lopen**" {
		t.Errorf("got %q, want %q (default template)", got, "**lopen**")
	}
}
