package domain

import (
	"math"
	"reflect"
	"testing"
)

func handleOption(id, title string, change func(option *ThreadHandleOption)) *ThreadHandleOption {
	option := &ThreadHandleOption{ID: id, Title: title, Project: new("app"), InScope: true}
	if change != nil {
		change(option)
	}
	return option
}

func handleIDs(options []*ThreadHandleOption) []string {
	ids := []string{}
	for _, option := range options {
		ids = append(ids, option.ID)
	}
	return ids
}

func TestThreadSlug(t *testing.T) {
	for title, want := range map[string]string{"Sink the mode choices into a track": "sink-the-mode-choices", "Widen the dock's resize handle": "widen-the-docks-resize", "  ": "thread", "!!!": "thread"} {
		if got := ThreadSlug(title); got != want {
			t.Fatalf("%q: %q", title, got)
		}
	}
}

func TestHandleTokenAt(t *testing.T) {
	cases := []struct {
		text  string
		caret int
		want  *HandleToken
	}{
		{"@sink", 5, &HandleToken{"sink", 0}},
		{"compare with @si", 16, &HandleToken{"si", 13}},
		{"@", 1, &HandleToken{"", 0}},
		{"mail me at zhuocheng@gmail.com", 30, nil},
		{"nothing here", 12, nil},
		{"@sink and more", 14, nil},
		{"é @Ab", 5, &HandleToken{"ab", 2}},
	}
	for _, c := range cases {
		if got := HandleTokenAt(c.text, c.caret); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%q: %+v", c.text, got)
		}
	}
}

func TestThreadHandlesQualifyAndBreakTies(t *testing.T) {
	handles := ThreadHandles([]*ThreadHandleOption{
		handleOption("t-1", "Sink the mode choices", func(o *ThreadHandleOption) { o.LastActivityAt = 3 }),
		handleOption("t-9f2c", "Sink the mode choices", func(o *ThreadHandleOption) { o.LastActivityAt = 1 }),
		handleOption("t-3", "Sink the mode choices", func(o *ThreadHandleOption) { o.Project, o.InScope, o.LastActivityAt = new("site"), false, 2 }),
	})
	got := []string{}
	for _, handle := range handles {
		got = append(got, handle.Handle)
	}
	if !reflect.DeepEqual(got, []string{"sink-the-mode-choices", "site/sink-the-mode-choices", "sink-the-mode-choices-9f2c"}) {
		t.Fatal(got)
	}
}

func TestRankThreadHandles(t *testing.T) {
	options := ThreadHandles([]*ThreadHandleOption{
		handleOption("here", "Raise the dock", func(o *ThreadHandleOption) { o.LastActivityAt = 2 }),
		handleOption("away", "Raise the panel", func(o *ThreadHandleOption) { o.Project, o.InScope, o.LastActivityAt = new("site"), false, 3 }),
	})
	cases := []struct {
		query string
		limit int
		want  []string
	}{
		{"", math.MaxInt, []string{"here"}},
		{"raise", math.MaxInt, []string{"here", "away"}},
		{"raise", 1, []string{"here"}},
		{"panel", math.MaxInt, []string{"away"}},
		{"nothing", math.MaxInt, []string{}},
	}
	for _, c := range cases {
		if got := handleIDs(RankThreadHandles(options, c.query, c.limit)); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%q: %v", c.query, got)
		}
	}
}

func TestExpandThreadHandles(t *testing.T) {
	options := ThreadHandles([]*ThreadHandleOption{handleOption("t-1", "Sink the mode choices", nil)})
	cases := map[string]string{
		"compare with @sink-the-mode-choices, please":          "compare with [Sink the mode choices](aicodingtool://thread/t-1), please",
		"@sink-the-mode-choices":                               "[Sink the mode choices](aicodingtool://thread/t-1)",
		"ask @nobody about it":                                 "ask @nobody about it",
		"mail zhuocheng@gmail.com":                             "mail zhuocheng@gmail.com",
		"nothing to expand":                                    "nothing to expand",
		"x @SINK-the-mode-choices @@a\n@sink-the-mode-choices": "x [Sink the mode choices](aicodingtool://thread/t-1) @@a\n[Sink the mode choices](aicodingtool://thread/t-1)",
	}
	for text, want := range cases {
		if got := ExpandThreadHandles(text, options); got != want {
			t.Fatalf("%q: %q", text, got)
		}
	}
	if ThreadReference("t-1", "Sink [the] mode") != "[Sink the mode](aicodingtool://thread/t-1)" || ThreadLink("t-1") != "<aicodingtool://thread/t-1>" {
		t.Fatal("reference")
	}
	if !StartsTitleWord("Sink the mode choices", "mode") || StartsTitleWord("Sink the mode choices", "ode") || StartsTitleWord("x", "a-b") {
		t.Fatal("startsTitleWord")
	}
}
