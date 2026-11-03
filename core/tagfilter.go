package core

// FilterByTags returns the cards matching the selected tags: with matchAll
// false, cards carrying at least one of tags (OR); with matchAll true, cards
// carrying every one of them (AND). An empty/nil tags returns cards
// unchanged - "no filter selected" means everything is in scope, not nothing.
func FilterByTags(cards []Card, tags []string, matchAll bool) []Card {
	if len(tags) == 0 {
		return cards
	}

	want := make(map[string]bool, len(tags))
	for _, t := range tags {
		want[t] = true
	}

	var out []Card
	for _, c := range cards {
		// Count distinct hits, so a card listing a tag twice can't stand in
		// for a second selected tag it doesn't have.
		hits := 0
		var seen map[string]bool
		for _, t := range c.Tags {
			if want[t] && !seen[t] {
				if seen == nil {
					seen = make(map[string]bool, len(want))
				}
				seen[t] = true
				hits++
			}
		}
		if (matchAll && hits >= len(want)) || (!matchAll && hits > 0) {
			out = append(out, c)
		}
	}
	return out
}
