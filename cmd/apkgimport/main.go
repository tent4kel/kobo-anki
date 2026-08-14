// Command apkgimport turns a real Anki .apkg into a kobo-anki deck CSV:
// a content provider, playing the same role for apkg decks that kobo-vocab
// plays for the dictionary. It resolves each card's Question/Answer HTML
// once, at import time, and writes ordinary core.Card rows tagged
// core.FormatHTML - core/LoadCards/SaveCards/FSRS scheduling never need to
// know a row came from an apkg rather than a hand-written deck.
//
// Deployed and run the same way kobo-vocab is: cross-built for the Kobo and
// invoked on-device against a dropped-in .apkg file.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"kobo-anki/ankipkg"
	"kobo-anki/core"
)

// splitAllTagsSentinel, as the whole -split-tags value (not just present in
// a comma list), means "discover every tag actually in the deck and split
// by all of them" instead of a fixed list. Go's flag package can't safely
// treat a bare "-split-tags" with no value that way: a non-boolean flag
// with no "=value" unconditionally consumes whatever comes next on the
// command line as its value - even another flag like "-deck" - so an
// explicit sentinel is the only unambiguous way to ask for "all" without
// risking silently mis-parsing the rest of the arguments.
const splitAllTagsSentinel = "all"

func isSplitAllTags(s string) bool {
	s = strings.TrimSpace(s)
	return strings.EqualFold(s, splitAllTagsSentinel) || s == "*"
}

func main() {
	apkgPath := flag.String("apkg", "", "path to the .apkg to import (required)")
	deckName := flag.String("deck", "", "deck name; written to <datadir>/<deck>.csv (required)")
	dataDir := flag.String("datadir", ".", "directory containing deck CSVs")
	splitTagsFlag := flag.String("split-tags", "", `comma-separated tags to split into their own decks, named "<deck> [tag]" - "all" (or "*") splits by every tag found in the deck; overrides split_tags in anki-apkgimport.conf when given`)
	flag.Parse()

	if *apkgPath == "" || *deckName == "" {
		fmt.Fprintln(os.Stderr, "usage: apkgimport -apkg path/to/deck.apkg -deck name [-datadir .] [-split-tags tag1,tag2|all]")
		os.Exit(2)
	}

	splitTagsRaw := *splitTagsFlag
	if splitTagsRaw == "" {
		splitTagsRaw = loadSplitTagsConfig("anki-apkgimport.conf")
	}

	if err := run(*apkgPath, *deckName, *dataDir, splitTagsRaw); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// loadSplitTagsConfig reads split_tags= from anki-apkgimport.conf, so a
// split you want every time doesn't have to be retyped on the command line.
// A missing file, or a file with no split_tags line, is not an error - the
// command-line flag is a complete substitute either way.
func loadSplitTagsConfig(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if v, ok := strings.CutPrefix(line, "split_tags="); ok {
			return v
		}
	}
	return ""
}

func parseTagList(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// deckGroup is one output deck's slice of an apkg's resolved cards - either
// the base deck, or one "<deck> [tag]" split.
type deckGroup struct {
	name     string
	resolved []ankipkg.Resolved
}

// splitByTag partitions resolved into deckGroups: a card matching one of
// splitTags goes into "<baseDeck> [tag]" instead of baseDeck, once per
// matching tag if it has more than one; a card matching none of them stays
// in baseDeck. baseDeck is always first in the result (even with no
// splitTags, in which case it's the only group - today's behaviour,
// unchanged), followed by tag groups in the order splitTags were given. A
// group with zero matching cards is omitted.
func splitByTag(resolved []ankipkg.Resolved, baseDeck string, splitTags []string) []deckGroup {
	order := []string{baseDeck}
	groups := map[string]*deckGroup{baseDeck: {name: baseDeck}}
	for _, tag := range splitTags {
		name := baseDeck + " [" + tag + "]"
		if _, exists := groups[name]; !exists {
			groups[name] = &deckGroup{name: name}
			order = append(order, name)
		}
	}

	for _, r := range resolved {
		matched := false
		for _, tag := range splitTags {
			if hasTag(r.Tags, tag) {
				name := baseDeck + " [" + tag + "]"
				groups[name].resolved = append(groups[name].resolved, r)
				matched = true
			}
		}
		if !matched {
			groups[baseDeck].resolved = append(groups[baseDeck].resolved, r)
		}
	}

	out := make([]deckGroup, 0, len(order))
	for _, name := range order {
		if g := groups[name]; len(g.resolved) > 0 {
			out = append(out, *g)
		}
	}
	return out
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func run(apkgPath, deckName, dataDir, splitTagsRaw string) error {
	pkg, err := ankipkg.Open(apkgPath)
	if err != nil {
		return fmt.Errorf("opening %s: %w", apkgPath, err)
	}
	resolved := pkg.Resolve()
	if len(resolved) == 0 {
		return fmt.Errorf("%s: no cards resolved (empty deck, or an unsupported export)", apkgPath)
	}

	var splitTags []string
	if isSplitAllTags(splitTagsRaw) {
		splitTags = allTags(resolved)
	} else {
		splitTags = parseTagList(splitTagsRaw)
	}

	// Computed once, from every card regardless of which output deck it
	// ends up in - every group's media dir gets the same stylesheets
	// written into it (see importGroup), the same way every group also
	// gets a full copy of the apkg's media rather than just what that
	// group's own cards reference. Simpler and always correct; the cost is
	// some duplicated disk space across splits, which only matters if
	// -split-tags is used at all.
	styles := modelStyles(resolved)

	for _, g := range splitByTag(resolved, deckName, splitTags) {
		if err := importGroup(g.name, g.resolved, pkg.Media, styles, apkgPath, dataDir); err != nil {
			return fmt.Errorf("importing %q: %w", g.name, err)
		}
	}
	return nil
}

// allTags collects every distinct tag actually present across resolved,
// sorted for deterministic output - what -split-tags all/* resolves to.
func allTags(resolved []ankipkg.Resolved) []string {
	seen := make(map[string]bool)
	for _, r := range resolved {
		for _, t := range r.Tags {
			seen[t] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// modelStyles collects each distinct model's CSS, keyed by the slug
// importGroup/refCard use to reference it - computed once for the whole
// apkg since a model's CSS doesn't depend on which output deck a card
// carrying it ends up in.
func modelStyles(resolved []ankipkg.Resolved) map[string]string {
	styles := make(map[string]string)
	for _, r := range resolved {
		styles[slugify(r.ModelName)] = r.ModelCSS
	}
	return styles
}

var slugUnsafeRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// slugify turns a model name into the [A-Za-z0-9_-]+ charset
// core.ExtractCardStyleRef's marker accepts and a filesystem accepts
// unescaped.
func slugify(s string) string {
	slug := strings.Trim(slugUnsafeRe.ReplaceAllString(strings.TrimSpace(s), "_"), "_")
	if slug == "" {
		return "model"
	}
	return slug
}

func importGroup(deckName string, resolved []ankipkg.Resolved, media map[string][]byte, styles map[string]string, apkgPath, dataDir string) error {
	mediaDir := core.DeckMediaDir(dataDir, deckName)
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", mediaDir, err)
	}
	for name, data := range media {
		safe := ankipkg.SanitizeFilename(name)
		if safe == "" || safe == "." {
			continue
		}
		if err := os.WriteFile(filepath.Join(mediaDir, safe), data, 0o644); err != nil {
			return fmt.Errorf("writing media %s: %w", safe, err)
		}
	}

	// Archive the source .apkg itself next to its media - the resolve-at-
	// import approach's insurance policy: if the template engine later
	// gains a capability it doesn't have today, this deck can be re-run
	// through apkgimport from its own archived source, without the user
	// needing to have kept the original file anywhere.
	if err := archiveSource(apkgPath, filepath.Join(mediaDir, "source.apkg")); err != nil {
		return fmt.Errorf("archiving source apkg: %w", err)
	}

	if err := writeStylesIfMissing(mediaDir, styles); err != nil {
		return fmt.Errorf("writing stylesheets: %w", err)
	}

	existing, err := core.LoadCards(core.DeckCSVPath(dataDir, deckName))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("loading existing deck: %w", err)
	}
	byID := make(map[string]core.Card, len(existing))
	for _, c := range existing {
		byID[c.Key()] = c
	}

	cards := make([]core.Card, 0, len(resolved))
	for _, r := range resolved {
		// The Anki *card* ID, not the note ID: a single note can produce
		// several cards (Basic-and-reversed, a multi-blank Cloze note), and
		// those siblings must not collide on core.Card.Key.
		id := fmt.Sprintf("%d", r.CardID)
		slug := slugify(r.ModelName)
		front := refCard(slug, r.Question)
		back := refCard(slug, r.Answer)

		if prior, ok := byID[id]; ok {
			// Re-import: keep review history, only refresh the rendered
			// faces in case the source .apkg changed.
			prior.Front = front
			prior.Back = back
			prior.Format = core.FormatHTML
			cards = append(cards, prior)
			continue
		}

		row := core.NewCardRow(id, front, back)
		row[core.ColFormat] = core.FormatHTML
		c, ok := core.CardFromRow(row)
		if !ok {
			return fmt.Errorf("building card %s: row rejected by CardFromRow", id)
		}
		cards = append(cards, c)
	}

	if err := core.SaveCards(core.DeckCSVPath(dataDir, deckName), cards); err != nil {
		return fmt.Errorf("saving deck: %w", err)
	}

	fmt.Printf("imported %d card(s) into %s (media: %s)\n", len(cards), core.DeckCSVPath(dataDir, deckName), mediaDir)
	return nil
}

// refCard replaces the old wrapCard, which baked a full
// <html><head><style>{css}</style></head><body>... into every single card
// face - measured on a real deck, 61% of every card's stored bytes was that
// one repeated CSS block, the same string, once per face, per card. The
// face now just references its model's stylesheet by name; see
// core.ExtractCardStyleRef (the reading side) and writeStylesIfMissing
// (where "style-slug.css" actually gets written).
func refCard(styleSlug, body string) string {
	return "<!--css:" + styleSlug + `--><div class="card">` + body + "</div>"
}

// writeStylesIfMissing writes each distinct model's CSS to its own
// style-<slug>.css in mediaDir, but only the first time a deck is
// imported: a deck's CSS is meant to be editable per-deck after that (drop
// a stylesheet into a text editor and adjust it), so a re-import must never
// clobber a file the user has since hand-edited. Delete the file to reset
// it back to the deck's own original CSS on the next import.
func writeStylesIfMissing(mediaDir string, styles map[string]string) error {
	for slug, css := range styles {
		path := filepath.Join(mediaDir, "style-"+slug+".css")
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.WriteFile(path, []byte(css), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func archiveSource(srcPath, dstPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	return os.WriteFile(dstPath, data, 0o644)
}
