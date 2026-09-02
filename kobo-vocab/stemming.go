package main

import "strings"

// --- Stemming (config-driven) ---

func stemLookup(dict *KoboDict, word string, rules *LangRules) (definition, stem string) {
	w := strings.ToLower(word)
	candidates := generateStems(w, rules)
	for _, c := range candidates {
		if def, ok := dict.Entries[c]; ok {
			return def, c
		}
	}
	return "", ""
}

func generateStems(word string, rules *LangRules) []string {
	seen := make(map[string]bool)
	var stems []string
	add := func(s string) {
		if s != "" && s != word && len(s) >= 2 && !seen[s] {
			seen[s] = true
			stems = append(stems, s)
		}
	}

	// Apply replacement rules first (more specific), then plain strips
	// This ensures "redt" tries "redden" before "red"
	for _, rule := range rules.Strips {
		if rule.Replacement == "" || !strings.HasSuffix(word, rule.Suffix) {
			continue
		}
		base := word[:len(word)-len(rule.Suffix)] + rule.Replacement
		add(base)
		if rules.ReduceDouble && len(base) >= 3 && base[len(base)-1] == base[len(base)-2] {
			add(base[:len(base)-1])
		}
	}
	for _, rule := range rules.Strips {
		if rule.Replacement != "" || !strings.HasSuffix(word, rule.Suffix) {
			continue
		}
		base := word[:len(word)-len(rule.Suffix)]
		add(base)
		if rules.ReduceDouble && len(base) >= 3 && base[len(base)-1] == base[len(base)-2] {
			add(base[:len(base)-1])
		}
	}

	// Apply prefix strip rules (combined with suffix rules)
	for _, prefix := range rules.PrefixStrips {
		if !strings.HasPrefix(word, prefix) || len(word) <= len(prefix)+2 {
			continue
		}
		stripped := word[len(prefix):]
		add(stripped)
		// Also try suffix rules on the prefix-stripped form
		for _, rule := range rules.Strips {
			if strings.HasSuffix(stripped, rule.Suffix) {
				base := stripped[:len(stripped)-len(rule.Suffix)] + rule.Replacement
				add(base)
			}
		}
	}

	return stems
}
