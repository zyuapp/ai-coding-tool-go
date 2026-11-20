package domain

import (
	"reflect"
	"testing"
)

func TestStoredMessagesReadTheirLabelsOnce(t *testing.T) {
	stored := decoded(t, `[
		{"id":"s1","kind":"user","text":"News","detail":"Thread updates","at":1},
		{"id":"s2","kind":"user","text":"Merge?","detail":"From Fix login · t2","at":2},
		{"id":"s3","kind":"user","text":"Older","detail":"From Monday","at":3},
		{"id":"s4","kind":"user","text":"Poll","detail":"Automation run #2","at":4},
		{"id":"s5","kind":"user","text":"Odd","detail":"Something a build once wrote","at":5},
		{"id":"s6","kind":"user","text":"Mine","at":6},
		{"id":"s7","kind":"system","text":"Moved","detail":"Detached at abcdef1","at":7},
		{"id":"s8","kind":"assistant","text":"Shh","quiet":true,"at":8}
	]`).([]any)
	messages, err := ParseStoredConversationMessages(stored)
	if err != nil {
		t.Fatal(err)
	}
	origins := []any{}
	for _, message := range messages {
		origins = append(origins, message.Origin)
	}
	jsonEqual(t, origins, `[{"kind":"coordination"},{"kind":"thread","threadId":"t2","title":"Fix login"},{"kind":"thread","title":"Monday"},{"kind":"automation","runNumber":2},{"kind":"legacy","label":"Something a build once wrote"},null,null,null]`, "origins")
	for i, message := range messages[:5] {
		if *message.Detail != stored[i].(map[string]any)["detail"] {
			t.Fatal("details stay")
		}
	}
	if messages[7].Withdrawn == nil || !*messages[7].Withdrawn {
		t.Fatal("quiet reads as withdrawn")
	}
	prompts := SentPrompts(messages)
	if len(prompts) != 1 || prompts[0].Text != "Mine" || prompts[0].Annotations == nil {
		t.Fatal(prompts)
	}
	if &SentPrompts(messages)[0] != &prompts[0] {
		t.Fatal("sent prompts are kept per history")
	}
	if len(SentPrompts(messages[:5])) != 0 {
		t.Fatal("a shorter history over the same array is its own")
	}
	if _, err := ParseStoredConversationMessages([]any{map[string]any{"id": "m", "kind": "robot", "text": "", "at": 1.0}}); err == nil || err.Error() != "Stored message 1 is unreadable." {
		t.Fatal(err)
	}
}

func TestOriginLabels(t *testing.T) {
	origin := LegacyOrigin("Something a build once wrote")
	if got, detail := OriginFields(origin); got != origin || *detail != "Something a build once wrote" {
		t.Fatal("originFields")
	}
	thread := MessageOriginThread{ThreadID: new("t2"), Title: "Fix login", Computer: new("Laptop")}
	if OriginLabel(thread) != "From Fix login on Laptop · t2" {
		t.Fatal(OriginLabel(thread))
	}
	if label, _ := SenderLabel(thread); label != "From Fix login on Laptop" {
		t.Fatal(label)
	}
	if _, ok := SenderLabel(MessageOriginAutomation{RunNumber: 3}); ok {
		t.Fatal("automation has no sender label")
	}
	if label, _ := MessageLabel(nil, new("stored")); label != "stored" {
		t.Fatal(label)
	}
	if got := LegacyOrigin("From Title with · spaced id"); !reflect.DeepEqual(got, MessageOriginThread{Title: "Title with · spaced id"}) {
		t.Fatal(got)
	}
	message := &ConversationMessage{ID: "m", Kind: ConversationMessageKindUser, Detail: new("Thread updates")}
	if labelled := WithLegacyOrigin(message); labelled == message || labelled.Origin != (MessageOriginCoordination{}) || message.Origin != nil {
		t.Fatal("withLegacyOrigin")
	}
	if !IsRemoteSender(decoded(t, `{"threadId":"t","title":"","computer":""}`)) || IsRemoteSender(decoded(t, `{"threadId":"","title":"","computer":""}`)) {
		t.Fatal("isRemoteSender")
	}
}

func TestCreatedMessagesUseTheInjectedClockAndIDs(t *testing.T) {
	now, newID := Now, NewID
	defer func() { Now, NewID = now, newID }()
	Now, NewID = func() float64 { return 42 }, func() string { return "id-1" }
	sent := CreateSentMessage(SentMessage{Text: "Hi", Origin: MessageOriginCoordination{}, Attachments: []string{}})
	jsonEqual(t, sent, `{"id":"id-1","kind":"user","text":"Hi","origin":{"kind":"coordination"},"detail":"Thread updates","at":42}`, "sent")
	jsonEqual(t, CreateFailureMessage("Broke"), `{"id":"id-1","kind":"system","text":"Broke","tone":"error","at":42}`, "failure")
}
