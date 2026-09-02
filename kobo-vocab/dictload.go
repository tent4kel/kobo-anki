package main

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
)

// --- Kobo dictionary loading ---
//
// Kobo dicthtml zips come in (at least) two entry layouts:
//
//   - Flat (older dictgen / stock Kobo dicts):
//     <w><a name="Aagje"/><div><b>Aagje</b><br/>noun<br>female first name</div></w>
//
//   - Nested (newer dictgen output, e.g. from custom StarDict/.df conversions):
//     <w><p><a name="a" /><b>a</b></p><var></var><ol>
//       <li><div>first sense...</div></li>
//       <li><div>second sense...</div></li>
//     </ol></w>
//
// extractEntry below handles both by branching on whether <li> sense blocks
// are present, rather than assuming one fixed structure.
//
// Note: this format is NOT well-formed HTML5 - <a name="x" /> is written as
// self-closing but <a> is not a void element per the HTML5 spec, so a real
// spec-compliant parser (tried: goquery/golang.org/x/net/html) leaves it open
// and reparents unrelated later entries into it via the adoption agency
// algorithm, silently destroying ~99% of entries. Regex-based extraction is
// deliberate here, not an oversight.

// testDictFile parses a single dict zip and prints diagnostics, for checking
// whether a dictionary is supported before deploying it to the Kobo. It
// leads with the profile and where it came from, since that is the setting
// that decides whether anything below it makes sense - and prints it in
// [pair:] syntax, ready to paste into the config if it needs adjusting.
func testDictFile(cfg Config, path string) {
	pair := ""
	if base := filepath.Base(path); strings.HasPrefix(base, "dicthtml-") {
		pair = strings.TrimSuffix(strings.TrimPrefix(base, "dicthtml-"), ".zip")
	}
	// Resolve through this pair's own [pair:] section, so it shows what a
	// real run produces rather than what the global defaults alone would.
	set := effectivePair(cfg, pair)
	prof, origin := resolveDictProfile(set, path)

	render := dictRender{
		maxSenses: set.MaxSenses, maxChars: set.MaxChars,
		asCard: set.DictExamples, cardFormat: compileCardFormatOrDefault(cfg.CardFormat),
	}
	dict, err := parseDict(path, compileProfile(prof, cfg.StripLabels), render)
	if err != nil {
		log.Fatalf("Failed to parse %s: %v", path, err)
	}

	fmt.Printf("%s: %d entries\n", path, len(dict.Entries))
	fmt.Printf("profile: %s (%s)\n", prof.name, origin)
	if pair != "" {
		fmt.Printf("  [pair:%s]\n  profile=%s\n", pair, prof.name)
		for _, line := range describeProfile(prof) {
			fmt.Printf("  %s\n", line)
		}
	}
	if len(dict.Entries) == 0 {
		fmt.Println("No entries parsed - the profile does not match this dictionary's structure.")
		return
	}
	fmt.Println()
	n := 0
	for word, def := range dict.Entries {
		if front, ok := dict.Fronts[word]; ok {
			fmt.Printf("  %s -> front=%q back=%q\n", word, front, def)
		} else {
			fmt.Printf("  %s -> %s\n", word, def)
		}
		n++
		if n >= 10 {
			break
		}
	}
}
