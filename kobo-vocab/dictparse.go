package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// dictRender holds the settings that decide what a parsed entry looks like
// on a card. These are user preferences and stay in the ordinary config -
// only structure lives in the profile.
type dictRender struct {
	maxSenses  int
	maxDepth   int
	maxChars   int
	asCard     bool // dict_examples
	cardFormat cardTemplate
}

// readShards walks a dictzip, decompressing members as needed, and hands
// each shard's bytes to fn.
func readShards(zipPath string, fn func(data []byte)) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		data := raw
		if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
			gr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				continue
			}
			data, err = io.ReadAll(gr)
			gr.Close()
			if err != nil {
				continue
			}
		}
		fn(data)
	}
	return nil
}

// sniffDict reads enough of a dictionary to identify its generator.
func sniffDict(zipPath string) (string, error) {
	var sample strings.Builder
	err := readShards(zipPath, func(data []byte) {
		if sample.Len() > 400_000 {
			return
		}
		sample.Write(data)
	})
	if err != nil {
		return "", err
	}
	return detectProfile(sample.String()), nil
}

// resolveDictProfile decides which profile describes one dictionary, and
// says where the answer came from so -print-config and -test-dict can show
// it. Order: an explicit [dict:xx-yy] section wins, otherwise the markup is
// sniffed. Selectors given in the section are layered over the named base,
// so overriding one of them does not mean restating the rest.
func resolveDictProfile(cfg Config, pair, zipPath, translationTag string) (dictProfile, string) {
	sec := cfg.Dicts[pair]

	base, origin := "", ""
	if sec != nil && sec.Base != "" && sec.Base != "auto" {
		base, origin = sec.Base, "configured"
	}
	if base == "" || base == "custom" {
		if sec != nil && sec.HasFields && sec.Base == "custom" {
			base, origin = "", "custom"
		} else {
			detected, err := sniffDict(zipPath)
			if err == nil {
				base, origin = detected, "detected"
			} else {
				base, origin = "flat", "default"
			}
		}
	}

	p := builtinProfiles[base] // zero value for "custom", which starts empty
	p.name = base
	if p.name == "" {
		p.name = "custom"
	}

	if sec != nil && sec.HasFields {
		p = applyProfileOverrides(p, sec.Overrides)
		if origin == "configured" {
			origin = "configured + overrides"
		}
	}

	// translation_tag predates profiles and says "the translation is
	// whatever this tag wraps". It is kept as a last resort in the chain,
	// scoped like the profile's own translation selector, so a dictionary
	// that marks translations with a class is preferred and one that only
	// bolds them still works. A custom profile states its own chain and
	// does not need this.
	if translationTag != "" && p.translationText == "" {
		if len(p.translation) > 0 {
			p.translation = append(p.translation, scopeOf(p.translation[0])+translationTag)
		} else {
			// No selector of its own: the tag is all there is to go on.
			// Scoped to the sense's first block first (where the gloss
			// lives), then anywhere in the sense.
			p.translation = []string{"> div:nth-of-type(1) " + translationTag, translationTag}
		}
	}
	return p, origin
}

// scopeOf returns everything but the final step of a selector, so a fallback
// can be added at the same depth as the selector it backs up.
func scopeOf(sel string) string {
	fields := strings.Fields(sel)
	if len(fields) <= 1 {
		return ""
	}
	return strings.Join(fields[:len(fields)-1], " ") + " "
}

func applyProfileOverrides(p, o dictProfile) dictProfile {
	if o.entry != "" {
		p.entry = o.entry
	}
	if o.headAttr != "" {
		p.headAttr = o.headAttr
	}
	if o.sense != "" {
		p.sense = o.sense
	}
	if len(o.translation) > 0 {
		p.translation = o.translation
	}
	if o.translationDrop != "" {
		p.translationDrop = o.translationDrop
	}
	if o.translationText != "" {
		p.translationText = o.translationText
		p.translation = nil
	}
	if o.example != "" {
		p.example = o.example
	}
	if o.exampleTarget != "" {
		p.exampleTarget = o.exampleTarget
	}
	if o.article != "" {
		p.article = o.article
	}
	if len(o.dropTags) > 0 {
		p.dropTags = o.dropTags
	}
	if o.splitOn != "" {
		p.splitOn = o.splitOn
	}
	return p
}

// describeProfile renders a profile back into [dict:xx-yy] syntax. A
// built-in is therefore also a worked example, and anyone generating their
// own dictionary can read off the structure it has to produce.
func describeProfile(p dictProfile) []string {
	var out []string
	add := func(k, v string) {
		if v != "" {
			out = append(out, k+"="+v)
		}
	}
	add("entry", p.entry)
	add("head", p.headAttr)
	add("sense", p.sense)
	add("translation", strings.Join(p.translation, " | "))
	add("translation_reject", p.translationDrop)
	add("translation_text", p.translationText)
	add("example", p.example)
	add("example_target", p.exampleTarget)
	add("article", p.article)
	add("drop_tags", strings.Join(p.dropTags, ","))
	add("split_on", p.splitOn)
	return out
}

// parseDict loads a dictionary using a profile for structure and a
// dictRender for presentation.
func parseDict(zipPath string, c *compiledProfile, r dictRender) (*KoboDict, error) {
	dict := &KoboDict{Entries: make(map[string]string), Fronts: make(map[string]string)}

	err := readShards(zipPath, func(data []byte) {
		doc, err := html.Parse(strings.NewReader(normalizeDictHTML(string(data))))
		if err != nil {
			return
		}
		for _, node := range query(doc, c.entry) {
			e, ok := extractProfiled(c, node)
			if !ok {
				continue
			}
			definition, front := renderEntry(e, r, c.p)
			if definition == "" {
				continue
			}
			// Homographs - the same headword across parts of speech - get
			// their own <w> block rather than being combined. Keeping
			// whichever block yields the most text is an order-independent
			// proxy for "the more complete entry"; it is what distinguishes
			// "wezel -> Wiesel" from "wezel -> Wesel", and it measurably
			// beats last-wins on real data.
			if existing, exists := dict.Entries[e.word]; exists && len(existing) >= len(definition) {
				continue
			}
			dict.Entries[e.word] = definition
			delete(dict.Fronts, e.word) // drop a stale front from the discarded block
			if front != "" {
				dict.Fronts[e.word] = front
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return dict, nil
}

// renderEntry turns an extracted entry into the definition (card back) and,
// when the dictionary supplies enough for one, a themed front.
func renderEntry(e parsedEntry, r dictRender, p dictProfile) (definition, front string) {
	if len(e.senses) == 0 {
		return "", ""
	}

	// A themed card needs the translations individually - one becomes the
	// headword, the rest alternatives - so it is only on the table for a
	// profile that can tell them apart. A positional profile cannot: comma-
	// splitting its gloss would invent alternatives that aren't there.
	//
	// This is what used to be the flat/nested branch. dict_examples could
	// be set globally while quietly doing nothing to a flat dictionary,
	// because there was nothing there to build a card from; now the profile
	// says so instead of the parser guessing from the markup each time.
	if r.asCard && p.translationText == "" {
		// A themed card only ever uses the first sense: "one example"
		// doesn't compose with a multi-sense list.
		s := e.senses[0]
		if len(s.translations) > 0 {
			headword := e.word
			if e.article != "" {
				headword = e.article + " " + e.word
			}
			back := formatCard(r.cardFormat, s.translations[0], s.translations[1:], s.exTarget)
			frnt := formatCard(r.cardFormat, headword, nil, s.exSource)
			return truncateMarkdownVisible(back, r.maxChars), truncateMarkdownVisible(frnt, r.maxChars)
		}
		// Fall through: a sense that is genuinely just usage notes still
		// gets a plain definition rather than going blank.
	}

	n := len(e.senses)
	if r.maxSenses > 0 && n > r.maxSenses {
		n = r.maxSenses
	}
	var parts []string
	for _, s := range e.senses[:n] {
		// A positional profile has no way to tell a translation from the
		// rest of the gloss, so its split-up translations are only good
		// enough for a card; a plain definition shows the text as written.
		text := ""
		if p.translationText == "" {
			text = strings.Join(s.translations, ", ")
		}
		if text == "" {
			blocks := s.blocks
			if r.maxDepth > 0 && len(blocks) > r.maxDepth {
				blocks = blocks[:r.maxDepth]
			}
			text = strings.Join(blocks, " ")
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	switch len(parts) {
	case 0:
		return "", ""
	case 1:
		definition = parts[0]
	default:
		definition = "- " + strings.Join(parts, "\n- ")
	}
	return truncateMarkdownVisible(definition, r.maxChars), ""
}
