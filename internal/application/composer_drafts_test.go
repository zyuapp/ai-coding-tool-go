package application

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func draftsTwoTasks() *WorkspaceState {
	return coreWorkspace(threadsOn([]*domain.Thread{coreTask("task-1"), coreTask("task-2")}, "task-1"))
}

func TestImagesDraftedPerThreadAndBounded(t *testing.T) {
	corePorted(t, func() {
		drafted := coreRun(draftsTwoTasks(),
			contracts.ImageCommandImageAdd{Path: "/attachments/one.png", Label: "Figma — Untitled"},
			contracts.ImageCommandImageAdd{Path: "/attachments/two.png", Label: "Chrome"},
		)
		held := drafted.Images["task-1"]
		if len(held) != 2 || held[0].Path != "/attachments/one.png" || held[0].Label != "Figma — Untitled" {
			t.Fatalf("drafted: %s", coreJSON(t, held))
		}
		removed := coreRun(drafted, contracts.ImageCommandImageRemove{ImageID: held[0].ID})
		if got := removed.Images["task-1"]; len(got) != 1 || got[0].Path != "/attachments/two.png" {
			t.Fatal("removed")
		}
		if gone := coreRun(removed, contracts.ImageCommandImageRemove{ImageID: held[1].ID}); len(gone.Images) != 0 {
			t.Fatal("emptied draft kept its entry")
		}
		twice := coreRun(draftsTwoTasks(),
			contracts.ImageCommandImageAdd{Path: "/attachments/one.png", Label: "shot.png", Source: new("/Users/me/shot.png")},
			contracts.ImageCommandImageAdd{Path: "/attachments/two.png", Label: "shot.png", Source: new("/Users/me/shot.png")},
		)
		pasted := coreRun(draftsTwoTasks(), contracts.ImageCommandImageAdd{Path: "/attachments/one.png"}, contracts.ImageCommandImageAdd{Path: "/attachments/two.png"})
		if len(twice.Images["task-1"]) != 1 || len(pasted.Images["task-1"]) != 2 {
			t.Fatal("the same file dropped twice")
		}
		if blank := coreRun(drafted, contracts.ImageCommandImageAdd{}); len(blank.Images["task-1"]) != 2 {
			t.Fatal("an empty path drafted")
		}
		var additions []WorkspaceInput
		for index := range domain.MaxAttachments + 1 {
			additions = append(additions, contracts.ImageCommandImageAdd{Path: fmt.Sprintf("/attachments/%d.png", index), Label: "Figma"})
		}
		filled := coreRun(draftsTwoTasks(), additions...)
		if len(filled.Images["task-1"]) != domain.MaxAttachments || filled.ActionError == nil || !strings.Contains(*filled.ActionError, "up to 6 images") {
			t.Fatalf("filled: %d %v", len(filled.Images["task-1"]), filled.ActionError)
		}
		start := draftsTwoTasks()
		focused := coreRun(start, contracts.ImageCommandImageAdd{Path: "/attachments/one.png"})
		if focused.ComposerFocus != start.ComposerFocus+1 {
			t.Fatal("an image did not ask for the caret")
		}
		recalled := coreRun(drafted, contracts.ImageCommandImageRecall{Paths: []string{"/kept/a.png"}})
		if got := recalled.Images["task-1"]; len(got) != 1 || got[0].Path != "/kept/a.png" || got[0].Label != "" {
			t.Fatalf("recalled: %s", coreJSON(t, got))
		}
	})
}

func TestFilesDraftedPerThreadAndBounded(t *testing.T) {
	corePorted(t, func() {
		drafted := coreRun(draftsTwoTasks(), contracts.FileCommandFileAttach{Files: []*domain.AttachedFileDraft{{Path: "/tmp/one.md", Name: "one.md"}, {Path: "/tmp/pics", Name: "pics", Folder: new(true)}}})
		held := drafted.Files["task-1"]
		if len(held) != 2 || held[0].Path != "/tmp/one.md" || held[1].Folder == nil || !*held[1].Folder {
			t.Fatalf("drafted: %s", coreJSON(t, held))
		}
		removed := coreRun(drafted, contracts.FileCommandFileDetach{FileID: held[0].ID})
		if got := removed.Files["task-1"]; len(got) != 1 || got[0].Name != "pics" {
			t.Fatal("detached")
		}
		twice := coreRun(draftsTwoTasks(),
			contracts.FileCommandFileAttach{Files: []*domain.AttachedFileDraft{{Path: "/tmp/one.md", Name: "one.md"}}},
			contracts.FileCommandFileAttach{Files: []*domain.AttachedFileDraft{{Path: "/tmp/one.md", Name: "one.md"}, {Path: "/tmp/one.md", Name: "one.md"}}},
			contracts.FileCommandFileAttach{Files: []*domain.AttachedFileDraft{{Path: "", Name: ""}}},
		)
		if len(twice.Files["task-1"]) != 1 {
			t.Fatal("the same file twice")
		}
		var many []*domain.AttachedFileDraft
		for index := range domain.MaxAttachedFiles + 1 {
			many = append(many, &domain.AttachedFileDraft{Path: fmt.Sprintf("/tmp/%d.md", index), Name: fmt.Sprintf("%d.md", index)})
		}
		filled := coreRun(draftsTwoTasks(), contracts.FileCommandFileAttach{Files: many})
		if len(filled.Files["task-1"]) != domain.MaxAttachedFiles || filled.ActionError == nil || !strings.Contains(*filled.ActionError, "up to 10 files") {
			t.Fatalf("filled: %d %v", len(filled.Files["task-1"]), filled.ActionError)
		}
		start := draftsTwoTasks()
		if attached := coreRun(start, contracts.FileCommandFileAttach{Files: []*domain.AttachedFileDraft{{Path: "/tmp/one.md", Name: "one.md"}}}); attached.ComposerFocus != start.ComposerFocus+1 {
			t.Fatal("a file did not ask for the caret")
		}
	})
}

func TestHandedBackPutsMessagesAheadOfTheDraft(t *testing.T) {
	state := draftsTwoTasks()
	state.Prompts = map[string]string{"task-1": "typed since"}
	state.Images = map[string][]*domain.StagedImage{"task-1": {{ID: "kept", Path: "/kept.png"}}}
	handed := HandedBackIn(ComposerOf(state), "task-1", []*domain.HeldMessage{
		{ID: "q1", Text: "first", Attachments: []string{"/q1.png"}, Pastes: []*domain.PastedText{{ID: "p", Text: "pasted"}}},
		{ID: "q2", Text: "second", Attachments: []string{}},
	}).View
	if handed.Prompts["task-1"] != "first\n\nsecond\n\ntyped since" || len(handed.Pastes["task-1"]) != 1 {
		t.Fatalf("handed: %s", coreJSON(t, handed.Prompts))
	}
	var paths []string
	for _, image := range handed.Images["task-1"] {
		paths = append(paths, image.Path)
	}
	threadsSame(t, paths, []string{"/q1.png", "/kept.png"}, "images")

	sent := SentElsewhereIn(ComposerOf(state), &SentDraft{Key: "task-1", Prompt: "typed", Pastes: []*domain.PastedText{}, Annotations: []*domain.Annotation{}, Attachments: &SentDraftAttachments{Key: MainComposer, IDs: []string{"kept"}}}).View
	if sent.Prompts["task-1"] != "since" || len(sent.Images["task-1"]) != 0 || !slices.Equal(sent.AttachmentSends[MainComposer].Sent, []string{"kept"}) {
		t.Fatalf("sent elsewhere: %s", coreJSON(t, sent))
	}
	cleared := ClearedDraft(ComposerOf(state).View, "task-1")
	if _, ok := cleared.Prompts["task-1"]; ok || len(cleared.Images) != 0 || state.Prompts["task-1"] != "typed since" {
		t.Fatal("cleared draft")
	}
}
