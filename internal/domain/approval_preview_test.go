package domain

import (
	"reflect"
	"testing"
)

func TestApprovalPreviewReadsTheAction(t *testing.T) {
	cases := []struct {
		input map[string]any
		want  ApprovalPreview
	}{
		{map[string]any{"file_path": "a.ts", "old_string": "one\ntwo", "new_string": ""}, ApprovalPreviewEdit{Path: "a.ts", Removed: []string{"one", "two"}, Added: []string{}}},
		{map[string]any{"file_path": "a.ts", "old_string": "one\n", "new_string": "two\n\nthree\n"}, ApprovalPreviewEdit{Path: "a.ts", Removed: []string{"one"}, Added: []string{"two", "", "three"}}},
		{map[string]any{"file_path": "a.ts", "content": "x"}, ApprovalPreviewFile{Path: "a.ts", Content: "x"}},
		{map[string]any{"command": "ls", "description": "List"}, ApprovalPreviewCommand{Command: "ls"}},
		{map[string]any{"file_path": "a.ts"}, ApprovalPreviewSubject{Text: "a.ts"}},
		{map[string]any{"url": "https://example.com"}, ApprovalPreviewSubject{Text: "https://example.com"}},
		{map[string]any{"query": "a"}, ApprovalPreviewRaw{}},
	}
	for _, c := range cases {
		if got := ApprovalPreviewApprovalPreview(c.input); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: got %#v, want %#v", c.input, got, c.want)
		}
	}
}
