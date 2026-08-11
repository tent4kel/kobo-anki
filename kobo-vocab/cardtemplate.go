package main

import (
	"fmt"
	"log"
	"strings"
)

// DefaultCardFormat: bold headword, comma-joined alternatives, then the
// italic example - each its own Markdown paragraph (a blank line between,
// not a single newline), so the renderer puts block_spacing between them
// and the three read as separate things. A single newline is only a soft
// break in Markdown and would set them solid.
//
// Each bracketed part is dropped independently, so an absent example means
// the paragraph simply isn't there rather than leaving a trailing gap.
const DefaultCardFormat = "[**{head}**][\n\n{alts}][\n\n*{ex}*]"

// cardTemplate is a compiled card_format string - see parseCardTemplate.
// Compile once per provider/dict construction, not per word: dict lookups
// alone run this thousands of times per dict load.
type cardTemplate struct {
	parts []templatePart
}

// templatePart is one top-level piece of the template: either a fixed
// literal run (optional=false, always rendered) or a `[...]` group
// (optional=true, rendered only if every placeholder inside it resolves
// non-empty).
type templatePart struct {
	optional bool
	tokens   []templateToken
}

// templateToken is either a literal text run (placeholder=="") or a
// {placeholder} reference.
type templateToken struct {
	placeholder string
	literal     string
}

var validPlaceholders = map[string]bool{"head": true, "alts": true, "ex": true}

// parseCardTemplate compiles a card_format string (see card_format in
// anki-mywords.conf.example) into a cardTemplate. Supports {head}/{alts}/
// {ex} placeholders and one level of [optional] bracketing - no nesting.
// "\n" is recognized as a line-break escape (config values are one line
// each, so a card_format wanting an actual line break has no other way to
// write one) and "\\" as a literal backslash; no other escaping (a template
// needing a literal '{'/'['/']'/'}' isn't a use case this needs to cover).
// A bracketed run disappears entirely, rather than leaving dangling
// punctuation behind, when every placeholder inside it is empty - e.g.
// "[, {alts}]" vanishes rather than emitting ", " when there are no
// alternatives. Text outside any brackets is always emitted as-is,
// placeholders and all, even if a placeholder resolves empty - brackets are
// opt-in, not inferred.
func parseCardTemplate(s string) (cardTemplate, error) {
	var t cardTemplate
	var cur templatePart
	var literal strings.Builder
	inBracket := false

	flushLiteral := func() {
		if literal.Len() > 0 {
			cur.tokens = append(cur.tokens, templateToken{literal: literal.String()})
			literal.Reset()
		}
	}
	flushPart := func() {
		flushLiteral()
		if len(cur.tokens) > 0 {
			t.parts = append(t.parts, cur)
		}
		cur = templatePart{}
	}

	i := 0
	for i < len(s) {
		switch s[i] {
		case '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return cardTemplate{}, fmt.Errorf("unclosed '{' at position %d", i)
			}
			name := s[i+1 : i+end]
			if !validPlaceholders[name] {
				return cardTemplate{}, fmt.Errorf("unknown placeholder {%s} at position %d - want head, alts, or ex", name, i)
			}
			flushLiteral()
			cur.tokens = append(cur.tokens, templateToken{placeholder: name})
			i += end + 1
		case '[':
			if inBracket {
				return cardTemplate{}, fmt.Errorf("nested '[' at position %d not supported", i)
			}
			flushPart()
			inBracket = true
			cur.optional = true
			i++
		case ']':
			if !inBracket {
				return cardTemplate{}, fmt.Errorf("unmatched ']' at position %d", i)
			}
			flushPart()
			inBracket = false
			i++
		case '\\':
			if i+1 < len(s) && (s[i+1] == 'n' || s[i+1] == '\\') {
				if s[i+1] == 'n' {
					literal.WriteByte('\n')
				} else {
					literal.WriteByte('\\')
				}
				i += 2
			} else {
				literal.WriteByte(s[i])
				i++
			}
		default:
			literal.WriteByte(s[i])
			i++
		}
	}
	if inBracket {
		return cardTemplate{}, fmt.Errorf("unclosed '['")
	}
	flushPart()
	return t, nil
}

// render fills in headword/alternatives (comma-joined)/example and drops
// each [optional] part whose placeholder(s) all resolved empty.
func (t cardTemplate) render(headword string, alternatives []string, example string) string {
	values := map[string]string{
		"head": headword,
		"alts": strings.Join(alternatives, ", "),
		"ex":   example,
	}
	var out strings.Builder
	for _, part := range t.parts {
		var buf strings.Builder
		allPresent, sawPlaceholder := true, false
		for _, tok := range part.tokens {
			if tok.placeholder == "" {
				buf.WriteString(tok.literal)
				continue
			}
			sawPlaceholder = true
			v := values[tok.placeholder]
			if v == "" {
				allPresent = false
			}
			buf.WriteString(v)
		}
		if part.optional && sawPlaceholder && !allPresent {
			continue
		}
		out.WriteString(buf.String())
	}
	return out.String()
}

// compileCardFormatOrDefault compiles a card_format string, falling back to
// DefaultCardFormat (and logging why) on an empty or malformed template - a
// typo in card_format shouldn't break every provider that shares it.
func compileCardFormatOrDefault(raw string) cardTemplate {
	if raw == "" {
		raw = DefaultCardFormat
	}
	t, err := parseCardTemplate(raw)
	if err != nil {
		log.Printf("Invalid card_format %q: %v - using default", raw, err)
		t, _ = parseCardTemplate(DefaultCardFormat)
	}
	return t
}
