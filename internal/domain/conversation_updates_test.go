package domain

import (
	"reflect"
	"runtime"
	"testing"
)

func historyMessage(id string) *ConversationMessage {
	return &ConversationMessage{ID: id, Kind: ConversationMessageKindAssistant, Text: id, At: 1}
}

func texts(messages []*ConversationMessage) []string {
	out := []string{}
	for _, message := range messages {
		out = append(out, message.Text)
	}
	return out
}

func TestAppendsAndTailReplacementsCompareOnlyTheChangedSuffix(t *testing.T) {
	before := []*ConversationMessage{historyMessage("one"), historyMessage("two")}
	appended := AppendMessages(before, []*ConversationMessage{historyMessage("three")})
	tail := *appended[2]
	tail.Text = "three more"
	updated := ReplaceLastMessage(appended, &tail)
	latest := AppendMessages(updated, []*ConversationMessage{historyMessage("four")})
	if FirstChangedMessage(before, latest) != 2 || FirstChangedMessage(appended, latest) != 2 {
		t.Fatal(FirstChangedMessage(before, latest), FirstChangedMessage(appended, latest))
	}
	if !reflect.DeepEqual(texts(before), []string{"one", "two"}) || !reflect.DeepEqual(texts(latest), []string{"one", "two", "three more", "four"}) {
		t.Fatal(texts(latest))
	}
	if ReplaceLastMessage(latest, latest[3]) == nil || FirstChangedMessage(latest, ReplaceLastMessage(latest, latest[3])) != 4 {
		t.Fatal("replacing the tail with itself changes nothing")
	}
}

func TestWithdrawalMovesTheBoundaryBack(t *testing.T) {
	before := []*ConversationMessage{historyMessage("old"), historyMessage("run-1"), historyMessage("run-2")}
	appended := AppendMessages(before, []*ConversationMessage{historyMessage("run-3")})
	withdrawn := WithdrawMessages(appended, 1)
	if FirstChangedMessage(before, withdrawn) != 1 || withdrawn[0] != before[0] {
		t.Fatal("boundary")
	}
	for i, message := range withdrawn {
		if (message.Withdrawn != nil && *message.Withdrawn) != (i > 0) {
			t.Fatalf("message %d", i)
		}
	}
	if before[1].Withdrawn != nil {
		t.Fatal("the earlier history is left alone")
	}
}

func TestUntrackedAndDivergentHistoriesCompareEverything(t *testing.T) {
	before := []*ConversationMessage{historyMessage("one"), historyMessage("two")}
	divergent := AppendMessages(before, []*ConversationMessage{historyMessage("divergent")})
	branch := AppendMessages(before, []*ConversationMessage{historyMessage("branch")})
	edited := *before[0]
	edited.Text = "edited"
	earlierEdit := []*ConversationMessage{&edited, before[1]}
	if FirstChangedMessage(divergent, branch) != 0 || FirstChangedMessage(before, AppendMessages(earlierEdit, []*ConversationMessage{historyMessage("three")})) != 0 {
		t.Fatal("divergent")
	}
	if FirstChangedMessage(nil, branch) != 0 || FirstChangedMessage(before, before) != 2 {
		t.Fatal("unknown / same")
	}
	if FirstChangedMessage(before[:1], AppendMessages(before, []*ConversationMessage{historyMessage("x")})) != 0 {
		t.Fatal("a shorter history over the same array is another history")
	}
}

func TestAncestryTraversalStaysBounded(t *testing.T) {
	before := []*ConversationMessage{historyMessage("one")}
	versions := [][]*ConversationMessage{before}
	for index := range 300 {
		versions = append(versions, ReplaceLastMessage(versions[len(versions)-1], historyMessage(string(rune('a'+index%26)))))
	}
	if FirstChangedMessage(before, versions[len(versions)-1]) != 0 {
		t.Fatal("unbounded")
	}
	if FirstChangedMessage(versions[100], versions[len(versions)-1]) != 0 || FirstChangedMessage(versions[290], versions[len(versions)-1]) != 0 {
		t.Fatal("a replaced tail differs from index 0 of a one-message history")
	}
}

func TestCollectedAncestryComparesEverything(t *testing.T) {
	before := []*ConversationMessage{historyMessage("one"), historyMessage("two")}
	middle := AppendMessages(before, []*ConversationMessage{historyMessage("three")})
	latest := AppendMessages(middle, []*ConversationMessage{historyMessage("four")})
	if FirstChangedMessage(before, latest) != 2 {
		t.Fatal("tracked")
	}
	middle = nil
	runtime.GC()
	runtime.GC()
	if got := FirstChangedMessage(before, latest); got != 0 {
		t.Fatalf("a collected ancestor is unknown: %d", got)
	}
	runtime.KeepAlive(before)
}
