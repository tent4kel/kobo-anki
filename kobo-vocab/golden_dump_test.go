package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestDumpGolden renders every entry of every local dictionary exactly as a
// real run would, so two builds can be diffed card for card. It is how the
// config re-scoping was shown to change nothing: 165,298 entries identical
// across both dictionaries, with only the reported profile *origin* differing
// (detected -> configured, since the migrated config now names the profile).
//
// It needs real dictionaries and a real config, so it does nothing unless
// GOLDEN_OUT and GOLDEN_CONF are both set - there is no fixture that could
// stand in for "the dictionaries actually installed on this machine". Kept
// rather than deleted because the same check is what will show that a
// generated config reproduces a hand-written one.
//
//	GOLDEN_CONF=$PWD/anki-mywords.conf GOLDEN_OUT=/tmp/a.txt \
//	  go test ./kobo-vocab/ -run TestDumpGolden
func TestDumpGolden(t *testing.T) {
	out, conf := os.Getenv("GOLDEN_OUT"), os.Getenv("GOLDEN_CONF")
	if out == "" || conf == "" {
		t.Skip("set GOLDEN_CONF and GOLDEN_OUT to dump")
	}
	cfg := loadConfig(conf)

	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	for _, pair := range []string{"nl-de", "nl-en"} {
		set := effectivePair(cfg, pair)
		path := filepath.Join(cfg.DictDir, "dicthtml-"+pair+".zip")
		prof, origin := resolveDictProfile(set, path)
		render := dictRender{
			maxSenses: set.MaxSenses, maxChars: set.MaxChars,
			asCard: set.DictExamples, cardFormat: compileCardFormatOrDefault(cfg.CardFormat),
		}
		d, err := parseDict(path, compileProfile(prof, cfg.StripLabels), render)
		if err != nil {
			t.Fatalf("%s: %v", pair, err)
		}
		fmt.Fprintf(w, "== %s profile=%s (%s) entries=%d\n", pair, prof.name, origin, len(d.Entries))

		words := make([]string, 0, len(d.Entries))
		for word := range d.Entries {
			words = append(words, word)
		}
		sort.Strings(words)
		for _, word := range words {
			fmt.Fprintf(w, "%s\t%q\t%q\n", word, d.Fronts[word], d.Entries[word])
		}
	}
}
