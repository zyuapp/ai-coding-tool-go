package application

import (
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func threadsCurrent() *WorkspaceState {
	return coreWorkspace(func(state *WorkspaceState) {
		state.Threads = []*domain.Thread{coreTask("task-1")}
		state.CurrentID = new("task-1")
	})
}

func TestPromptWithAnnotationsQuotesEachNote(t *testing.T) {
	flat := PromptWithAnnotations("Fix it", []*domain.Annotation{
		{ID: "a", Quote: "line one\nline two", Note: "this is wrong"},
		{ID: "b", Quote: "another claim", Note: "  "},
	})
	if !strings.HasPrefix(flat, "Fix it\n\n") || !strings.Contains(flat, "> line one\n> line two\nthis is wrong") || !strings.HasSuffix(flat, "> another claim") {
		t.Fatalf("prompt: %q", flat)
	}
	if got := PromptWithAnnotations("Just text", nil); got != "Just text" {
		t.Fatalf("no annotations: %q", got)
	}
}

func TestAnnotationQuoteIsClamped(t *testing.T) {
	corePorted(t, func() {
		state := Reduce(threadsCurrent(), contracts.AnnotationCommandAnnotationAdd{Quote: "  " + strings.Repeat("x", 5000) + "  "}).State
		added := state.Annotations["task-1"]
		if len(added) != 1 || threadsUTF16Len(added[0].Quote) != 2000 || !strings.HasSuffix(added[0].Quote, "…") {
			t.Fatalf("clamped: %+v", added)
		}
		if ClampQuote("short") != "short" {
			t.Fatal("short quote changed")
		}
		if blank := Reduce(threadsCurrent(), contracts.AnnotationCommandAnnotationAdd{Quote: "   "}).State; len(blank.Annotations) != 0 {
			t.Fatalf("blank quote drafted: %+v", blank.Annotations)
		}
	})
}

func TestAnnotationsDraftedPerTask(t *testing.T) {
	corePorted(t, func() {
		anchor := domain.AnnotationAnchorMessage{MessageID: "m-1", Start: 4, End: 9}
		drafted := coreRun(threadsCurrent(),
			contracts.AnnotationCommandAnnotationAdd{Quote: "first", Note: new("typed at the highlight"), Anchor: anchor},
			contracts.AnnotationCommandAnnotationAdd{Quote: "second"},
		)
		held := drafted.Annotations["task-1"]
		if len(held) != 2 || held[0].Quote != "first" || held[1].Quote != "second" || held[0].Note != "typed at the highlight" {
			t.Fatalf("drafted: %s", coreJSON(t, held))
		}
		if held[0].Anchor != anchor || held[1].Anchor != nil {
			t.Fatalf("anchors: %s", coreJSON(t, held))
		}
		noted := coreRun(drafted, contracts.AnnotationCommandAnnotationNote{AnnotationID: held[0].ID, Note: "keep this"})
		if noted.Annotations["task-1"][0].Note != "keep this" {
			t.Fatal("note not edited")
		}
		removed := coreRun(noted, contracts.AnnotationCommandAnnotationRemove{AnnotationID: held[1].ID})
		if got := removed.Annotations["task-1"]; len(got) != 1 || got[0].Quote != "first" {
			t.Fatalf("removed: %s", coreJSON(t, got))
		}
		if gone := coreRun(removed, contracts.AnnotationCommandAnnotationRemove{AnnotationID: held[0].ID}); len(gone.Annotations) != 0 {
			t.Fatalf("emptied draft kept its entry: %s", coreJSON(t, gone.Annotations))
		}
	})
}

func TestAnnotationRecallReplacesTheDraft(t *testing.T) {
	corePorted(t, func() {
		drafted := coreRun(threadsCurrent(), contracts.AnnotationCommandAnnotationAdd{Quote: "drafted since"})
		sent := []*domain.Annotation{{ID: "a", Quote: "what was sent", Note: "and its note"}}
		recalled := Reduce(drafted, contracts.AnnotationCommandAnnotationRecall{Annotations: sent}).State
		coreEqualJSON(t, recalled.Annotations["task-1"], sent, "recalled")
		stepped := Reduce(&recalled, contracts.AnnotationCommandAnnotationRecall{Annotations: []*domain.Annotation{}}).State
		if len(stepped.Annotations) != 0 {
			t.Fatalf("empty recall left %s", coreJSON(t, stepped.Annotations))
		}
	})
}

func TestNewAnnotationFocusesComposerButDiffCommentDoesNot(t *testing.T) {
	corePorted(t, func() {
		start := threadsCurrent()
		noted := Reduce(start, contracts.AnnotationCommandAnnotationAdd{Quote: "the claim", Anchor: domain.AnnotationAnchorMessage{MessageID: "m-1", End: 9}}).State
		if noted.ComposerFocus != start.ComposerFocus+1 {
			t.Fatal("annotation did not focus the composer")
		}
		reviewed := Reduce(&noted, contracts.AnnotationCommandAnnotationAdd{Quote: "a changed line", Anchor: domain.AnnotationAnchorDiff{Path: "a.ts", Start: "1", End: "1"}}).State
		if reviewed.ComposerFocus != noted.ComposerFocus {
			t.Fatal("diff comment took the caret")
		}
		empty := Reduce(&reviewed, contracts.AnnotationCommandAnnotationAdd{Quote: "   "}).State
		if empty.ComposerFocus != reviewed.ComposerFocus {
			t.Fatal("blank quote took the caret")
		}
	})
}

func TestSideChatDraftsItsOwnAnnotations(t *testing.T) {
	corePorted(t, func() {
		opened := coreRun(threadsCurrent(), contracts.SideChatCommandSideChatOpen{ChatID: "chat-1"})
		noted := coreRun(opened, contracts.AnnotationCommandAnnotationAdd{TaskID: new("chat-1"), Quote: "for the side"})
		if got := noted.Annotations["chat-1"]; len(got) != 1 || len(noted.Annotations["task-1"]) != 0 {
			t.Fatalf("side chat annotations: %s", coreJSON(t, noted.Annotations))
		}
		if noted.ComposerFocus != opened.ComposerFocus {
			t.Fatal("thread's own composer took the caret")
		}
		if noted.DockFocus == nil || noted.DockFocus.Owner != "task-1" || noted.DockFocus.Tab != "chat-1" || noted.DockFocus.Count != opened.DockFocus.Count+1 {
			t.Fatalf("dock focus: %s", coreJSON(t, noted.DockFocus))
		}
		closed := coreRun(noted, contracts.SideChatCommandSideChatClose{ChatID: "chat-1"})
		if len(closed.Annotations) != 0 {
			t.Fatalf("closing kept %s", coreJSON(t, closed.Annotations))
		}
	})
}

func TestRevealAskesOnlyTheDraftingTranscript(t *testing.T) {
	corePorted(t, func() {
		anchor := domain.AnnotationAnchorMessage{MessageID: "m-1", End: 9}
		drafted := coreRun(threadsCurrent(),
			contracts.SideChatCommandSideChatOpen{ChatID: "chat-1"},
			contracts.AnnotationCommandAnnotationAdd{Quote: "in the thread", Anchor: anchor},
			contracts.AnnotationCommandAnnotationAdd{TaskID: new("chat-1"), Quote: "in the fork", Anchor: anchor},
			contracts.AnnotationCommandAnnotationAdd{Quote: "a bare reference"},
		)
		mine, bare, forked := drafted.Annotations["task-1"][0], drafted.Annotations["task-1"][1], drafted.Annotations["chat-1"][0]
		revealed := Reduce(drafted, contracts.AnnotationCommandAnnotationReveal{TaskID: new("chat-1"), AnnotationID: forked.ID}).State
		coreEqualJSON(t, revealed.AnnotationReveal, AnnotationReveal{TaskID: "chat-1", AnnotationID: forked.ID, MessageID: "m-1", Count: 1}, "reveal")
		if AnnotationRevealFor(&revealed, "task-1") != nil {
			t.Fatal("the thread was asked too")
		}
		again := Reduce(&revealed, contracts.AnnotationCommandAnnotationReveal{AnnotationID: mine.ID}).State
		coreEqualJSON(t, again.AnnotationReveal, AnnotationReveal{TaskID: "task-1", AnnotationID: mine.ID, MessageID: "m-1", Count: 2}, "second reveal")
		if bareReveal := Reduce(&again, contracts.AnnotationCommandAnnotationReveal{AnnotationID: bare.ID}).State; bareReveal.AnnotationReveal != again.AnnotationReveal {
			t.Fatal("a reference with no passage revealed")
		}
		if other := Reduce(&again, contracts.AnnotationCommandAnnotationReveal{AnnotationID: forked.ID}).State; other.AnnotationReveal != again.AnnotationReveal {
			t.Fatal("another composer's annotation revealed")
		}
	})
}

func TestTextAfterSend(t *testing.T) {
	for _, item := range []struct{ typed, sent, want string }{
		{"mine and more", "mine", "and more"},
		{"handed back\n\nmine", "mine", "handed back"},
		{"handed back\n\nmine and more", "mine", "handed back\n\nand more"},
		{"rewritten", "mine", "rewritten"},
	} {
		if got := TextAfterSend(item.typed, item.sent); got != item.want {
			t.Errorf("TextAfterSend(%q, %q) = %q, want %q", item.typed, item.sent, got, item.want)
		}
	}
}

func TestPromptWithAttachments(t *testing.T) {
	if got := PromptWithAttachments("Fix the header", nil); got != "Fix the header" {
		t.Fatalf("nothing attached: %q", got)
	}
	one := PromptWithAttachments("Fix the header", []*domain.RunAttachment{{Path: "/tmp/a.png", Labels: []string{"button overlaps", "", "text is clipped"}}})
	if !strings.HasPrefix(one, "Fix the header\n\n") || !strings.HasSuffix(one, "/tmp/a.png\n  1. button overlaps\n  3. text is clipped") {
		t.Fatalf("one: %q", one)
	}
	two := PromptWithAttachments("Fix these", []*domain.RunAttachment{
		{Path: "/tmp/a.png", Labels: []string{"text 1", "text 2", "text 3"}},
		{Path: "/tmp/b.png", Labels: []string{"text 5"}},
	})
	if !strings.Contains(two, "/tmp/a.png (marks A1–A3)\n  A1. text 1\n  A2. text 2\n  A3. text 3\n") || !strings.HasSuffix(two, "/tmp/b.png (mark B1)\n  B1. text 5") {
		t.Fatalf("lettered: %q", two)
	}
	bare := PromptWithAttachments("", []*domain.RunAttachment{{Path: "/tmp/a.png", Labels: []string{"only mark"}}, {Path: "/tmp/b.png", Labels: []string{}}})
	if !strings.HasSuffix(bare, "/tmp/a.png (mark A1)\n  A1. only mark\n/tmp/b.png") || !strings.HasPrefix(bare, "Attached screenshots") {
		t.Fatalf("unmarked: %q", bare)
	}
	if ThreadTitleFor("", []*domain.RunAttachment{{Path: "/a.png"}}) != "Screenshot" ||
		ThreadTitleFor("", []*domain.RunAttachment{{Path: "/a.png"}, {Path: "/b.png"}}) != "2 screenshots" ||
		ThreadTitleFor("Fix it", []*domain.RunAttachment{{Path: "/a.png"}}) != "Fix it" {
		t.Fatal("titles")
	}
	if got := AttachmentURL("/x/My File.png", new("t 1")); got != "attachment://file/My%20File.png?taskId=t+1" {
		t.Fatalf("url: %q", got)
	}
}
