package domain

import "slices"

// ThreadJumpRows is how many rows the panel draws, which is also how far a query has to read.
const ThreadJumpRows = 12

// RankThreadJumps is the threads a query names, best match first and newest first within a match.
// rows is the panel's row count; callers pass ThreadJumpRows for the default.
func RankThreadJumps(options []*ThreadJumpOption, query string, rows int) []*ThreadJumpOption {
	kept := max(0, rows)
	wanted := featuresJSLower(featuresJSTrim(query))
	if wanted == "" {
		return slices.Clone(options[:min(len(options), kept)])
	}
	pullRequest := PullRequestPullRequestQuery(wanted)
	best := 1
	if pullRequest != nil {
		best = 0
	}
	var ranked [MatchRanks + 1][]*ThreadJumpOption
	for _, option := range options {
		if pullRequest != nil && option.PullRequest != nil && LinksPullRequest(option.PullRequest, pullRequest) {
			ranked[0] = append(ranked[0], option)
		} else {
			rank, ok := MatchRank(option.Title, wanted)
			if !ok {
				continue
			}
			ranked[rank+1] = append(ranked[rank+1], option)
		}
		if len(ranked[best]) >= kept {
			break
		}
	}
	flat := []*ThreadJumpOption{}
	for _, options := range ranked {
		flat = append(flat, options...)
	}
	return flat[:min(len(flat), kept)]
}
