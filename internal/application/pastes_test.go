package application

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func threadsLongPaste() string {
	lines := make([]string, 40)
	for line := range lines {
		lines[line] = fmt.Sprintf("line %d", line)
	}
	return strings.Join(lines, "\n")
}

func TestPasteRidesAsPillOnlyWhenBig(t *testing.T) {
	for text, want := range map[string]bool{
		"a sentence I meant to type": false,
		"one\ntwo\nthree":            false,
		threadsLongPaste():           true,
		strings.Repeat("x", 1000):    true,
		"   ":                        false,
	} {
		if got := PasteRidesAsPill(text); got != want {
			t.Errorf("PasteRidesAsPill(%.20q) = %v", text, got)
		}
	}
	if PasteSummary("one\ntwo") != "2 lines" || PasteSummary(strings.Repeat("x", 1200)) != "1,200 characters" {
		t.Fatal("summaries")
	}
	if got := PasteTitle([]*domain.PastedText{{ID: "a", Text: "\n\n  the first words  \nmore"}}); got != "the first words" {
		t.Fatalf("title: %q", got)
	}
}

func TestPromptWithPastesNumbersEachBlock(t *testing.T) {
	flat := PromptWithPastes("Read this", []*domain.PastedText{{ID: "a", Text: "first block"}, {ID: "b", Text: "second block"}})
	if !strings.HasPrefix(flat, "Read this\n\n") || !strings.Contains(flat, "Pasted text #1:\nfirst block") || !strings.HasSuffix(flat, "Pasted text #2:\nsecond block") {
		t.Fatalf("prompt: %q", flat)
	}
	if PromptWithPastes("Just text", nil) != "Just text" {
		t.Fatal("no pastes")
	}
}

func TestPastesDraftedPerTaskAndRecalled(t *testing.T) {
	corePorted(t, func() {
		long := threadsLongPaste()
		drafted := coreRun(threadsCurrent(), contracts.PasteCommandPasteAdd{Text: long}, contracts.PasteCommandPasteAdd{Text: "second"})
		held := drafted.Pastes["task-1"]
		if len(held) != 2 || held[0].Text != long || held[1].Text != "second" {
			t.Fatalf("drafted: %s", coreJSON(t, held))
		}
		if empty := coreRun(threadsCurrent(), contracts.PasteCommandPasteAdd{Text: ""}); len(empty.Pastes) != 0 {
			t.Fatal("empty paste drafted")
		}
		removed := coreRun(drafted, contracts.PasteCommandPasteRemove{PasteID: held[0].ID})
		if got := removed.Pastes["task-1"]; len(got) != 1 || got[0].Text != "second" {
			t.Fatalf("removed: %s", coreJSON(t, got))
		}
		if gone := coreRun(removed, contracts.PasteCommandPasteRemove{PasteID: held[1].ID}); len(gone.Pastes) != 0 {
			t.Fatal("emptied draft kept its entry")
		}
		sent := []*domain.PastedText{{ID: "p", Text: "what was sent"}}
		recalled := coreRun(drafted, contracts.PasteCommandPasteRecall{Pastes: sent})
		coreEqualJSON(t, recalled.Pastes["task-1"], sent, "recalled")
		if stepped := coreRun(recalled, contracts.PasteCommandPasteRecall{Pastes: []*domain.PastedText{}}); len(stepped.Pastes) != 0 {
			t.Fatal("empty recall kept the entry")
		}
		opened := coreRun(threadsCurrent(), contracts.SideChatCommandSideChatOpen{ChatID: "chat-1"}, contracts.PasteCommandPasteAdd{TaskID: new("chat-1"), Text: long})
		if len(opened.Pastes["chat-1"]) != 1 || len(opened.Pastes["task-1"]) != 0 {
			t.Fatalf("side chat pastes: %s", coreJSON(t, opened.Pastes))
		}
		if closed := coreRun(opened, contracts.SideChatCommandSideChatClose{ChatID: "chat-1"}); len(closed.Pastes) != 0 {
			t.Fatal("closing the side chat kept its pastes")
		}
	})
}
