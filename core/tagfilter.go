package core

// FilterByTags returns the cards whose Tags include at least one of tags.
// An empty/nil tags returns cards unchanged - "no filter selected" means
// everything is in scope, not nothing.
func FilterByTags(cards []Card, tags []string) []Card {
	if len(tags) == 0 {
		return cards
	}

	want := make(map[string]bool, len(tags))
	for _, t := range tags {
		want[t] = true
	}

	var out []Card
	for _, c := range cards {
		for _, t := range c.Tags {
			if want[t] {
				out = append(out, c)
				break
			}
		}
	}
	return out
}
