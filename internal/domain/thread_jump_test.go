package domain

import (
	"strconv"
	"strings"
	"testing"
)

func threadJumpOptions(titles ...string) []*ThreadJumpOption {
	options := make([]*ThreadJumpOption, len(titles))
	for index, title := range titles {
		options[index] = &ThreadJumpOption{ID: "task-" + strconv.Itoa(index), Title: title, Engine: AgentEngineClaude, RankedAt: float64(len(titles) - index)}
	}
	return options
}

func threadJumpTitles(options []*ThreadJumpOption) string {
	titles := []string{}
	for _, option := range options {
		titles = append(titles, option.Title)
	}
	return strings.Join(titles, "|")
}

var threadJumpThreads = threadJumpOptions("Dock the browser panel", "Panel find", "Rework the panel tabs", "Ship the dock")

func TestRankThreadJumps(t *testing.T) {
	for _, check := range []struct {
		query string
		rows  int
		want  string
	}{
		{"", ThreadJumpRows, "Dock the browser panel|Panel find|Rework the panel tabs|Ship the dock"},
		{"  ", 2, "Dock the browser panel|Panel find"},
		{"panel", ThreadJumpRows, "Panel find|Dock the browser panel|Rework the panel tabs"},
		{"dock", ThreadJumpRows, "Dock the browser panel|Ship the dock"},
		{"worktree", ThreadJumpRows, ""},
		{"SHIP", ThreadJumpRows, "Ship the dock"},
	} {
		if got := threadJumpTitles(RankThreadJumps(threadJumpThreads, check.query, check.rows)); got != check.want {
			t.Fatalf("%q = %s", check.query, got)
		}
	}
}

func TestRankThreadJumpsReadsOnlyAsFarAsNeeded(t *testing.T) {
	titles := []string{}
	for index := range 500 {
		titles = append(titles, "Panel "+strconv.Itoa(index))
	}
	many := threadJumpOptions(append(titles, "Reopen the panel")...)
	if got := threadJumpTitles(RankThreadJumps(many, "panel", 3)); got != "Panel 0|Panel 1|Panel 2" {
		t.Fatal(got)
	}
}

func TestRankThreadJumpsPullRequestFirst(t *testing.T) {
	options := threadJumpOptions("Ship 42 fixes", "Dock the browser panel", "Panel find")
	options[1].PullRequest = &PullRequestLink{Number: 42, URL: "https://github.com/acme/app/pull/42"}
	options[2].PullRequest = &PullRequestLink{Number: 42, URL: "https://github.com/acme/other/pull/42"}
	for query, want := range map[string]string{
		"42":  "Dock the browser panel|Panel find|Ship 42 fixes",
		"#42": "Dock the browser panel|Panel find",
		" https://github.com/Acme/App/pull/42/files ": "Dock the browser panel",
		"4": "Ship 42 fixes",
	} {
		if got := threadJumpTitles(RankThreadJumps(options, query, ThreadJumpRows)); got != want {
			t.Fatalf("%q = %s", query, got)
		}
	}
}
