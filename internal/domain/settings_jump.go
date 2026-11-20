package domain

import "strings"

// SettingsJumpRows is how many settings rows the panel draws, which leaves the rest of it to threads.
const SettingsJumpRows = 5

// ranks: a name beats a keyword, so "font" offers the font controls before anything merely tagged with it.
const ranks = MatchRanks + 1

// rankOf returns false when the option does not match.
func rankOf(option *SettingsJumpOption, wanted string) (int, bool) {
	if named, ok := MatchRank(option.Title, wanted); ok {
		return named, true
	}
	words := option.Title + " " + option.Keywords
	for _, word := range featuresSplitSpace(wanted) {
		if !StartsTitleWord(words, word) {
			return 0, false
		}
	}
	return MatchRanks, true
}

// RankSettingsJumps is the settings a query names; an empty query offers none.
// rows is the panel's row count; callers pass SettingsJumpRows for the default.
func RankSettingsJumps(query string, rows int) []*SettingsJumpOption {
	wanted := featuresJSLower(featuresJSTrim(query))
	if wanted == "" {
		return []*SettingsJumpOption{}
	}
	var ranked [ranks][]*SettingsJumpOption
	for _, option := range SettingsJumpOptions {
		if rank, ok := rankOf(option, wanted); ok {
			ranked[rank] = append(ranked[rank], option)
		}
	}
	flat := []*SettingsJumpOption{}
	for _, options := range ranked {
		flat = append(flat, options...)
	}
	return flat[:min(len(flat), max(0, rows))]
}

// featuresSplitSpace is split(/\s+/) on text already trimmed.
func featuresSplitSpace(text string) []string {
	if text == "" {
		return []string{""}
	}
	return strings.FieldsFunc(text, featuresIsJSSpace)
}
