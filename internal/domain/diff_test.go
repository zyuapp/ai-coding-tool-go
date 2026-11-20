package domain

import (
	"strings"
	"testing"
)

var diffPatchText = strings.Join([]string{
	"diff --git a/src/app.ts b/src/app.ts",
	"index 1111111..2222222 100644",
	"--- a/src/app.ts",
	"+++ b/src/app.ts",
	"@@ -1,4 +1,5 @@ export function app()",
	" const first = 1;",
	"-const second = 2;",
	"+const second = 22;",
	"+const third = 3;",
	" const last = 4;",
	"",
}, "\n")

func diffLineText(at *float64) string {
	if at == nil {
		return "null"
	}
	return featuresJSNumber(*at)
}

func TestParseFilePatchKeepsBothSidesLineNumbers(t *testing.T) {
	file := ParseFilePatch(diffPatchText, "fallback.ts")
	if file.Path != "src/app.ts" || len(file.Hunks) != 1 || file.PreviousPath != nil {
		t.Fatal(featuresJSON(t, file))
	}
	hunk := file.Hunks[0]
	if hunk.Header != "export function app()" || hunk.OldStart != 1 || hunk.OldLines != 4 || hunk.NewStart != 1 || hunk.NewLines != 5 {
		t.Fatal(featuresJSON(t, hunk))
	}
	rows := []string{}
	for _, row := range hunk.Rows {
		rows = append(rows, string(row.Kind)+":"+diffLineText(row.OldLine)+":"+diffLineText(row.NewLine))
	}
	if strings.Join(rows, ",") != "context:1:1,delete:2:null,add:null:2,add:null:3,context:3:4" {
		t.Fatal(rows)
	}
	featuresExpectJSON(t, hunk.Rows[1], `{"kind":"delete","key":"0:o2","text":"const second = 2;","oldLine":2,"newLine":null}`)
}

func TestParseFilePatchNamesHeaderlessPatchByFallback(t *testing.T) {
	if ParseFilePatch("@@ -1 +1 @@\n-a\n+b\n", "notes.md").Path != "notes.md" {
		t.Fatal("fallback")
	}
	hunk := ParseFilePatch("@@ -1 +1 @@\n-a\n+b\n", "notes.md").Hunks[0]
	if hunk.OldLines != 1 || hunk.NewLines != 1 {
		t.Fatal("a range without a count is one line long")
	}
}

func TestParseFilePatchKeepsRenameSource(t *testing.T) {
	file := ParseFilePatch(strings.Join([]string{"--- a/old/name.ts", "+++ b/new/name.ts", "@@ -1 +1 @@", "-a", "+b", ""}, "\n"), "new/name.ts")
	if file.Path != "new/name.ts" || featuresDeref(file.PreviousPath) != "old/name.ts" {
		t.Fatal(featuresJSON(t, file))
	}
}

func TestParseFilePatchMissingNewlineAnnotates(t *testing.T) {
	file := ParseFilePatch("@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+b\n", "a.txt")
	if len(file.Hunks[0].Rows) != 2 || file.Hunks[0].Rows[0].Text != "a" || file.Hunks[0].Rows[1].Text != "b" {
		t.Fatal(featuresJSON(t, file))
	}
}

func TestDiffRowsFlatWithHunkHeads(t *testing.T) {
	rows := DiffRows(ParseFilePatch(diffPatchText, "src/app.ts"))
	head, ok := rows[0].(DiffRowHunk)
	if !ok || head.Text != "@@ -1,4 +1,5 @@ export function app()" || len(rows) != 6 {
		t.Fatal(featuresJSON(t, rows))
	}
	keys := map[string]bool{}
	for _, row := range rows {
		keys[featuresDecode(t, featuresJSON(t, row)).(map[string]any)["key"].(string)] = true
	}
	if len(keys) != len(rows) {
		t.Fatal("keys are unique")
	}
}

func TestHunkTextPerSide(t *testing.T) {
	hunk := ParseFilePatch(diffPatchText, "src/app.ts").Hunks[0]
	if HunkText(hunk, DiffSideOld) != "const first = 1;\nconst second = 2;\nconst last = 4;" ||
		HunkText(hunk, DiffSideNew) != "const first = 1;\nconst second = 22;\nconst third = 3;\nconst last = 4;" {
		t.Fatal("each side of a hunk reads as the text it was cut from")
	}
	var added *DiffRowContext
	for _, row := range hunk.Rows {
		if row.Text == "const third = 3;" {
			added = row
		}
	}
	if line, ok := HunkTextIndex(hunk, DiffSideNew)[added.Key]; !ok || line != 2 {
		t.Fatal("new index")
	}
	if _, ok := HunkTextIndex(hunk, DiffSideOld)[added.Key]; ok {
		t.Fatal("an added row is not on the old side")
	}
}

func TestCommentQuote(t *testing.T) {
	rows := DiffRows(ParseFilePatch(diffPatchText, "src/app.ts"))
	if got := CommentQuote("src/app.ts", rows[3:5], DiffSideNew); got != "src/app.ts:L2-L3\n+const second = 22;\n+const third = 3;" {
		t.Fatal(got)
	}
	if got := CommentQuote("src/app.ts", rows[1:2], DiffSideNew); !strings.HasPrefix(got, "src/app.ts:L1\n") {
		t.Fatal(got)
	}
	if got := CommentQuote("src/app.ts", rows[2:3], DiffSideNew); got != "src/app.ts\n-const second = 2;" {
		t.Fatal(got)
	}
}

func TestSplitRowsPairsReplacements(t *testing.T) {
	kind := func(row DiffRow) string {
		if line, ok := diffLine(row); ok {
			return string(line.Kind)
		}
		return "null"
	}
	pairs := []string{}
	for _, row := range SplitRows(ParseFilePatch(diffPatchText, "src/app.ts")) {
		if pair, ok := row.(DiffPair); ok {
			pairs = append(pairs, kind(pair.Left)+"|"+kind(pair.Right))
		}
	}
	if strings.Join(pairs, ",") != "context|context,delete|add,null|add,context|context" {
		t.Fatal(pairs)
	}
	featuresExpectJSON(t, SplitRows(ParseFilePatch(diffPatchText, "src/app.ts"))[3], `{"kind":"pair","key":"|0:n3","left":null,"right":{"kind":"add","key":"0:n3","text":"const third = 3;","oldLine":null,"newLine":3}}`)
}

func TestLanguageForPath(t *testing.T) {
	if language, _ := LanguageForPath("src/app.tsx"); language != "tsx" {
		t.Fatal(language)
	}
	if language, _ := LanguageForPath("deep/path/run.mts"); language != "typescript" {
		t.Fatal(language)
	}
	for _, path := range []string{"Makefile", "archive.tar.gz"} {
		if _, ok := LanguageForPath(path); ok {
			t.Fatalf("%s asks for none", path)
		}
	}
}

func TestRangeKeyAndIsDiffRange(t *testing.T) {
	topic := "topic"
	if RangeKey(Uncommitted) != "uncommitted" || RangeKey(DiffRangeBranches{Base: "main"}) == RangeKey(DiffRangeBranches{Base: "main", Compare: &topic}) || RangeKey(DiffRangeCommit{Commit: "abc1234"}) != "commit:abc1234" {
		t.Fatal("rangeKey")
	}
	for text, want := range map[string]bool{
		`{"kind":"uncommitted"}`:                           true,
		`{"kind":"branches","base":"main","compare":null}`: true,
		`{"kind":"branches","base":"","compare":null}`:     false,
		`{"kind":"branches","compare":"topic"}`:            false,
		`{"kind":"branches","base":"main"}`:                false,
		`{"kind":"commit","commit":"ABC1234"}`:             true,
		`{"kind":"commit","commit":"abc12"}`:               false,
		`"uncommitted"`:                                    false,
		`null`:                                             false,
	} {
		if IsDiffRange(featuresDecode(t, text)) != want {
			t.Fatalf("%s should be %v", text, want)
		}
	}
}

func TestFileFingerprintAndFolding(t *testing.T) {
	file := &DiffFileSummary{Path: "a.ts", Status: DiffFileStatusModified, Additions: 2, Deletions: 1}
	more := *file
	more.Additions = 3
	if FileFingerprint(file) != "modified:2:1" || FileFingerprint(file) == FileFingerprint(&more) {
		t.Fatal("fingerprint")
	}
	files := []*DiffFileSummary{{Path: "lock", Additions: DrawnFileLimit + 1}}
	for index := range 10 {
		files = append(files, &DiffFileSummary{Path: "a" + featuresJSNumber(float64(index)), Additions: DrawnFileLimit - 1})
	}
	files = append(files, &DiffFileSummary{Path: "b", Additions: 20}, &DiffFileSummary{Path: "c", Additions: 10})
	folded := FoldedForSize(files)
	if !folded.Has("lock") || folded.Has("a9") || !folded.Has("b") || folded.Has("c") || len(folded) != 2 {
		t.Fatal(folded.Sorted())
	}
}
