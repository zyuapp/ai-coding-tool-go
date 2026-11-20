package domain

import (
	"strconv"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// featuresJSON is a value's JSON, for comparing against what the TypeScript test expects.
func featuresJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := jsonx.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %v: %v", value, err)
	}
	return string(data)
}

// featuresDecode is JSON text as JSON.parse reads it.
func featuresDecode(t *testing.T, text string) any {
	t.Helper()
	var value any
	if err := jsonx.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("unmarshal %s: %v", text, err)
	}
	return value
}

func featuresExpectJSON(t *testing.T, got any, want string) {
	t.Helper()
	text := featuresJSON(t, got)
	if featuresJSON(t, featuresDecode(t, text)) != featuresJSON(t, featuresDecode(t, want)) {
		t.Fatalf("got  %s\nwant %s", text, want)
	}
}

type findSeed struct {
	kind   ConversationMessageKind
	text   string
	detail *string
}

func findMessages(seeds ...findSeed) []*ConversationMessage {
	messages := make([]*ConversationMessage, len(seeds))
	for index, seed := range seeds {
		messages[index] = &ConversationMessage{ID: "m" + strconv.Itoa(index), At: float64(index * 1000), Kind: seed.kind, Text: seed.text, Detail: seed.detail}
	}
	return messages
}

func findText(text string) *string { return &text }

func TestFindHitsMatchOwnTextFoldedAndOffScreen(t *testing.T) {
	found := FindHits(findMessages(
		findSeed{kind: "user", text: "Where is the retry?"},
		findSeed{kind: "tool", text: "Read", detail: findText("retry once, then retry again")},
		findSeed{kind: "assistant", text: "Nothing here."},
	), "RETRY")
	featuresExpectJSON(t, found, `[
		{"messageId":"m0","field":"text","start":13,"occurrence":0},
		{"messageId":"m1","field":"detail","start":0,"occurrence":0},
		{"messageId":"m1","field":"detail","start":17,"occurrence":1}]`)
}

func TestFindHitsBlankQueryFindsNothing(t *testing.T) {
	featuresExpectJSON(t, FindHits(findMessages(findSeed{kind: "user", text: "anything"}), "   "), `[]`)
}

func TestFindHitsCapped(t *testing.T) {
	wall := findMessages(findSeed{kind: "assistant", text: strings.Repeat("a", MaxFindHits+50)})
	found := FindHits(wall, "a")
	if len(found) != MaxFindHits {
		t.Fatalf("len = %d", len(found))
	}
	featuresExpectJSON(t, found[len(found)-1], `{"messageId":"m0","field":"text","start":499,"occurrence":499}`)
}

func TestFindHitsCountTextThenDetailPerMessage(t *testing.T) {
	found := FindHits(findMessages(
		findSeed{kind: "tool", text: "hit hit", detail: findText("hit")},
		findSeed{kind: "assistant", text: "hit"},
	), "hit")
	featuresExpectJSON(t, found, `[
		{"messageId":"m0","field":"text","start":0,"occurrence":0},
		{"messageId":"m0","field":"text","start":4,"occurrence":1},
		{"messageId":"m0","field":"detail","start":0,"occurrence":2},
		{"messageId":"m1","field":"text","start":0,"occurrence":0}]`)
}

func TestFindHitsKeepMessageMatchesWhenThreadGrows(t *testing.T) {
	before := findMessages(findSeed{kind: "user", text: "retry"})
	found := FindHits(before, "retry")
	again := FindHits(append(before[:1:1], &ConversationMessage{ID: "m1", At: 1, Kind: "assistant", Text: "retry"}), "retry")
	if again[0] != found[0] {
		t.Fatal("a message nothing touched is not scanned again")
	}
	featuresExpectJSON(t, again, `[
		{"messageId":"m0","field":"text","start":0,"occurrence":0},
		{"messageId":"m1","field":"text","start":0,"occurrence":0}]`)
}

func TestFindHitsHeldMessageStopsAtRoomLeft(t *testing.T) {
	wall := findMessages(findSeed{kind: "assistant", text: strings.Repeat("a", MaxFindHits+50)})
	if len(FindHits(wall, "a")) != MaxFindHits {
		t.Fatal("wall")
	}
	crowded := FindHits(append([]*ConversationMessage{{ID: "first", Kind: "user", Text: "aa"}}, wall...), "a")
	if len(crowded) != MaxFindHits {
		t.Fatalf("len = %d", len(crowded))
	}
	featuresExpectJSON(t, crowded[len(crowded)-1], `{"messageId":"m0","field":"text","start":497,"occurrence":497}`)
}

func TestFindHitsStartsAreUTF16Offsets(t *testing.T) {
	found := FindHits(findMessages(findSeed{kind: "user", text: "😀é retry"}), "retry")
	if len(found) != 1 || found[0].Start != 4 {
		t.Fatalf("found = %s", featuresJSON(t, found))
	}
}

func TestMemoizedFindHitsReusesSearchOfSameList(t *testing.T) {
	messages := findMessages(findSeed{kind: "user", text: "retry"}, findSeed{kind: "user", text: "retry"})
	first := MemoizedFindHits(messages, " Retry ")
	again := MemoizedFindHits(messages, "retry")
	if len(first) != 2 || len(again) != 2 || &first[0] != &again[0] {
		t.Fatal("the same list and query reuse the same hits")
	}
	shorter := MemoizedFindHits(messages[:1], "retry")
	if len(shorter) != 1 {
		t.Fatalf("a shorter view of the list is searched on its own: %d", len(shorter))
	}
	if len(MemoizedFindHits(nil, "retry")) != 0 {
		t.Fatal("no messages")
	}
}

func TestStepMatchWraps(t *testing.T) {
	if StepMatch(2, 1, 3) != 0 || StepMatch(0, -1, 3) != 2 || StepMatch(0, 1, 0) != 0 {
		t.Fatal("stepping wraps at both ends, and stays put with nothing to step through")
	}
}

func TestSameFindTarget(t *testing.T) {
	t1, t2 := "t1", "t2"
	if !SameFindTarget(FindTargetThread{TaskID: &t1}, FindTargetThread{TaskID: &t1}) ||
		SameFindTarget(FindTargetThread{TaskID: &t1}, FindTargetThread{TaskID: &t2}) ||
		SameFindTarget(FindTargetBrowser{TabID: "a"}, FindTargetBrowser{TabID: "b"}) ||
		SameFindTarget(FindTargetBrowser{TabID: "a"}, FindTargetTerminal{TerminalID: "a"}) {
		t.Fatal("sameFindTarget tells one page from another")
	}
	if !SameFindTarget(FindTargetReview{Owner: "t1"}, FindTargetReview{Owner: "t1"}) || SameFindTarget(FindTargetReview{Owner: "t1"}, FindTargetReview{Owner: "t2"}) {
		t.Fatal("a review target names the dock it belongs to")
	}
	if SearchesItself(FindTargetReview{Owner: "t1"}) || !SearchesItself(FindTargetBrowser{TabID: "a"}) {
		t.Fatal("searchesItself")
	}
}

type findLine struct {
	kind DiffLineKind
	text string
}

func findPatch(path string, lines ...findLine) *DiffFile {
	rows := make([]*DiffRowContext, len(lines))
	for index, line := range lines {
		number := float64(index + 1)
		rows[index] = &DiffRowContext{Kind: line.kind, Key: "0:" + strconv.Itoa(index), Text: line.text, OldLine: &number, NewLine: &number}
	}
	return &DiffFile{Path: path, Hunks: []*DiffHunk{{OldStart: 1, OldLines: float64(len(lines)), NewStart: 1, NewLines: float64(len(lines)), Rows: rows}}}
}

func TestFileReviewHitsNameFirstThenLines(t *testing.T) {
	file := findPatch("src/find.ts", findLine{"context", "const find = 1;"}, findLine{"add", "let other = 2;"}, findLine{"delete", "find(find);"})
	featuresExpectJSON(t, FileReviewHits("src/find.ts", file, "find", MaxFindHits), `[
		{"path":"src/find.ts","key":null,"occurrence":0},
		{"path":"src/find.ts","key":"0:0","occurrence":0},
		{"path":"src/find.ts","key":"0:2","occurrence":0},
		{"path":"src/find.ts","key":"0:2","occurrence":1}]`)
}

func TestFileReviewHitsSkipHunkHeaders(t *testing.T) {
	file := findPatch("a.ts", findLine{"context", "kept"})
	file.Hunks[0].Header = "function findHits"
	featuresExpectJSON(t, FileReviewHits("a.ts", file, "find", MaxFindHits), `[]`)
}

func TestFileReviewHitsUnreadPatchAnswersForName(t *testing.T) {
	featuresExpectJSON(t, FileReviewHits("src/find.ts", nil, "find", MaxFindHits), `[{"path":"src/find.ts","key":null,"occurrence":0}]`)
}

func TestFileReviewHitsStopAtRoom(t *testing.T) {
	file := findPatch("a.ts", findLine{"add", "e e e"}, findLine{"add", "e e"})
	if len(FileReviewHits("a.ts", file, "e", 3)) != 3 {
		t.Fatal("room 3")
	}
	featuresExpectJSON(t, FileReviewHits("a.ts", file, "e", 0), `[]`)
}

func TestSameReviewHit(t *testing.T) {
	key := "0:1"
	hit := &ReviewHit{Path: "a.ts", Key: &key, Occurrence: 0}
	copied := *hit
	other := *hit
	other.Occurrence = 1
	unkeyed := *hit
	unkeyed.Key = nil
	moved := *hit
	moved.Path = "b.ts"
	if !SameReviewHit(hit, &copied) || SameReviewHit(hit, &other) || SameReviewHit(hit, &unkeyed) || SameReviewHit(hit, &moved) {
		t.Fatal("two review hits are the same only when they name the same match of the same row")
	}
}

func findRead(path string, lines ...findLine) *ReviewFile {
	return &ReviewFile{Path: path, Version: path + "|1", File: findPatch(path, lines...)}
}

func findComing(path string) *ReviewFile {
	return &ReviewFile{Path: path, Version: path + "|1", Coming: true}
}

func findSkipped(path string) *ReviewFile {
	return &ReviewFile{Path: path, Version: path + "|1"}
}

func findHitPlaces(hits []*ReviewHit) string {
	places := []string{}
	for _, hit := range hits {
		places = append(places, hit.Path+"@"+featuresDeref(hit.Key)+"#"+strconv.Itoa(int(hit.Occurrence)))
	}
	return strings.Join(places, ",")
}

func TestReviewHitsFoldInOncePatchLands(t *testing.T) {
	needle := findLine{"add", "needle"}
	before := ReviewHits([]*ReviewFile{findRead("a.ts", needle), findComing("b.ts"), findRead("c.ts", needle)}, "needle", nil)
	if got := findHitPlaces(before.Hits); got != "a.ts@0:0#0,c.ts@0:0#0" || !before.Counting {
		t.Fatalf("before = %s counting %v", got, before.Counting)
	}
	after := ReviewHits([]*ReviewFile{findRead("a.ts", needle), findRead("b.ts", findLine{"add", "needle needle"}), findRead("c.ts", needle)}, "needle", nil)
	if got := findHitPlaces(after.Hits); got != "a.ts@0:0#0,b.ts@0:0#0,b.ts@0:0#1,c.ts@0:0#0" || after.Counting {
		t.Fatalf("after = %s counting %v", got, after.Counting)
	}
}

func TestReviewHitsSkipFilesNeverRead(t *testing.T) {
	found := ReviewHits([]*ReviewFile{findSkipped("huge.lock"), findRead("a.ts", findLine{"add", "needle"})}, "needle", nil)
	featuresExpectJSON(t, found.Hits, `[{"path":"a.ts","key":"0:0","occurrence":0}]`)
	if found.Counting {
		t.Fatal("a file that will never be read cannot hold the total open")
	}
}

func TestReviewHitsKeepListOrder(t *testing.T) {
	needle := findLine{"add", "needle"}
	files := []*ReviewFile{findRead("c.ts", needle), findRead("a.ts", needle), findRead("b.ts", needle)}
	scanned := map[string][]*ReviewHit{}
	first := ReviewHits(files, "needle", scanned)
	again := ReviewHits([]*ReviewFile{files[2], files[1], files[0]}, "needle", scanned)
	if findHitPlaces(first.Hits) != "c.ts@0:0#0,a.ts@0:0#0,b.ts@0:0#0" || findHitPlaces(again.Hits) != "b.ts@0:0#0,a.ts@0:0#0,c.ts@0:0#0" {
		t.Fatal("the list's order is the order")
	}
}

func TestReviewHitsScannedFileNotReadAgain(t *testing.T) {
	needle := findLine{"add", "needle"}
	scanned := map[string][]*ReviewHit{}
	one := ReviewHits([]*ReviewFile{findRead("a.ts", needle), findComing("b.ts")}, "needle", scanned)
	two := ReviewHits([]*ReviewFile{findRead("a.ts", needle), findRead("b.ts", needle)}, "needle", scanned)
	if len(scanned) != 2 || two.Hits[0] != one.Hits[0] {
		t.Fatal("the first file's matches are the very same ones")
	}
}

func TestReviewHitsCapped(t *testing.T) {
	many := make([]*ReviewFile, 200)
	for index := range many {
		many[index] = findRead("file-"+strconv.Itoa(index)+".ts", findLine{"add", "e e e e e"})
	}
	found := ReviewHits(many, "e", map[string][]*ReviewHit{})
	if len(found.Hits) != MaxFindHits || found.Hits[0].Path != "file-0.ts" {
		t.Fatalf("len = %d", len(found.Hits))
	}
}
