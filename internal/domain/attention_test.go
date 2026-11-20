package domain

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func TestDismissalFilesFindingKeysAway(t *testing.T) {
	thread := &Thread{ThreadCore: ThreadCore{ID: "a"}}
	found := WithFinding(thread, FindingReport{Headline: "Build broke", Key: "ci"}, 1, false)
	if found == thread || len(found.Findings) != 1 || len(found.Messages) != 1 || !HasUnreadAttention(found) {
		t.Fatal("finding recorded")
	}
	if WithFinding(found, FindingReport{Headline: "Build broke again", Key: "ci"}, 2, false) != found {
		t.Fatal("a carried issue is not news")
	}
	read := ReadAttention([]*Thread{found}, "a")[0]
	if HasUnreadAttention(read) || !WantsAttention(read) || HasUnreadAttention(found) == false {
		t.Fatal("reading takes the mark off only")
	}
	filed := Dismissed([]*Thread{read}, jsonx.SetOf("a"))[0]
	if filed.Findings != nil || IssueState(filed, "ci") != "handled" {
		t.Fatalf("dismissal: %+v", filed.FindingsThread)
	}
	if WithFinding(filed, FindingReport{Headline: "Build broke", Key: "ci"}, 3, false) != filed {
		t.Fatal("a handled issue stays quiet")
	}
	if WithClosedIssues(filed, []string{"ci"}) != filed {
		t.Fatal("a reported issue stays handled")
	}
	closed := WithClosedIssues(filed, nil)
	if closed.HandledIssues != nil || !IsNews(closed, "ci") {
		t.Fatal("an issue no run reports is news again")
	}
}
