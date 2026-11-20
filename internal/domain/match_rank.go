package domain

import "strings"

// MatchRanks is how many ranks a match can take.
const MatchRanks = 3

// MatchRank is the rank a name takes for a lower-cased query; false when the query is not in it at all.
func MatchRank(name, wanted string) (int, bool) {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, wanted) {
		return 0, true
	}
	if StartsTitleWord(name, wanted) {
		return 1, true
	}
	if strings.Contains(lower, wanted) {
		return 2, true
	}
	return 0, false
}

// RankedMatches is the items any of whose names answer a query, best rank first. An empty name is
// TypeScript's undefined.
func RankedMatches[Item any](items []Item, query string, names func(item Item) []string) []Item {
	wanted := strings.ToLower(jsTrim(query))
	if wanted == "" {
		return append([]Item{}, items...)
	}
	var ranks [MatchRanks][]Item
	for _, item := range items {
		best := -1
		for _, name := range names(item) {
			if name == "" {
				continue
			}
			if rank, ok := MatchRank(name, wanted); ok && (best < 0 || rank < best) {
				best = rank
			}
		}
		if best >= 0 {
			ranks[best] = append(ranks[best], item)
		}
	}
	ranked := []Item{}
	for _, rank := range ranks {
		ranked = append(ranked, rank...)
	}
	return ranked
}
