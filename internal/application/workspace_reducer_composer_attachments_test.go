package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func attachmentsImage(id string, path *string) *domain.OutgoingAttachment {
	return &domain.OutgoingAttachment{ID: id, Source: "data:image/png;base64,AQID", Annotations: []*domain.ImageAnnotation{}, Path: path}
}

func attachmentsDrafted() *WorkspaceState {
	return coreRun(coreWorkspace(), contracts.ViewCommandViewSetPrompt{Prompt: "Look at this"})
}

func TestSendWritesImagesBeforeTheRun(t *testing.T) {
	corePorted(t, func() {
		sending := Reduce(attachmentsDrafted(), contracts.TaskCommandAttachmentsSend{Attachments: []*domain.OutgoingAttachment{attachmentsImage("one", nil), attachmentsImage("two", new("/tmp/shot.png"))}})
		coreEqualJSON(t, sending.Effects, `[{"type":"send-attachments","attachments":[{"id":"one","source":"data:image/png;base64,AQID","annotations":[]},{"id":"two","source":"data:image/png;base64,AQID","annotations":[],"path":"/tmp/shot.png"}]}]`, "send-attachments")
		if send := AttachmentSendFor(sending.State.AttachmentSends, nil); !send.Busy || send.DraftKey == nil || len(sending.State.PendingRuns) != 0 {
			t.Fatalf("sending: %s", coreJSON(t, send))
		}
		again := Reduce(&sending.State, contracts.TaskCommandAttachmentsSend{Attachments: []*domain.OutgoingAttachment{attachmentsImage("one", nil)}})
		if len(again.Effects) != 0 {
			t.Fatal("a second send started on top of the first")
		}
		written := Reduce(&sending.State, AttachmentEventAttachmentsSaved{IDs: []string{"one", "two"}, Attachments: []*domain.RunAttachment{{Path: "/tmp/one.png", Labels: []string{}}, {Path: "/tmp/shot.png", Labels: []string{}}}})
		send := AttachmentSendFor(written.State.AttachmentSends, nil)
		if send.Busy || len(send.Sent) != 2 || send.Sent[0] != "one" {
			t.Fatalf("written: %s", coreJSON(t, send))
		}
		coreEffect[WorkspaceEffectResolveRunWorkspace](t, written)
	})
}

func TestTooManyImagesStopTheSend(t *testing.T) {
	corePorted(t, func() {
		tooMany := make([]*domain.OutgoingAttachment, domain.MaxAttachments+1)
		for at := range tooMany {
			tooMany[at] = attachmentsImage("image", nil)
		}
		refused := Reduce(attachmentsDrafted(), contracts.TaskCommandAttachmentsSend{Attachments: tooMany})
		if _, failed := refused.Result.(contracts.WorkspaceCommandResultFalse); !failed || len(refused.Effects) != 0 || len(refused.State.PendingRuns) != 0 {
			t.Fatalf("refused: %s", coreJSON(t, refused.Result))
		}
		if send := AttachmentSendFor(refused.State.AttachmentSends, nil); send.Error == nil || *send.Error != tooManyImagesError {
			t.Fatalf("error: %s", coreJSON(t, send))
		}
	})
}

func TestFailedWriteGivesTheComposerBack(t *testing.T) {
	corePorted(t, func() {
		sending := Reduce(attachmentsDrafted(), contracts.TaskCommandAttachmentsSend{Attachments: []*domain.OutgoingAttachment{attachmentsImage("one", nil)}})
		failed := Reduce(&sending.State, AttachmentEventAttachmentsFailed{Message: "The disk is full."})
		coreEqualJSON(t, failed.Result, `{"ok":false,"message":"The disk is full."}`, "result")
		send := AttachmentSendFor(failed.State.AttachmentSends, nil)
		if send.Busy || send.Error == nil || *send.Error != "The disk is full." {
			t.Fatalf("send: %s", coreJSON(t, send))
		}
		noticed := Reduce(&failed.State, contracts.TaskCommandAttachmentsNotice{Message: new("Pasted too many")})
		if got := AttachmentSendFor(noticed.State.AttachmentSends, nil).Error; got == nil || *got != "Pasted too many" {
			t.Fatal("notice")
		}
	})
}

func TestSavedImagesFollowTheirWrittenFiles(t *testing.T) {
	corePorted(t, func() {
		state := coreRun(attachmentsDrafted(), contracts.ImageCommandImageAdd{Path: "/tmp/shot.png", Label: "Shot"})
		id := ImagesFor(state, NewThreadDraft)[0].ID
		sending := Reduce(state, contracts.TaskCommandAttachmentsSend{Attachments: []*domain.OutgoingAttachment{attachmentsImage(id, new("/tmp/shot.png"))}})
		written := Reduce(&sending.State, AttachmentEventAttachmentsSaved{IDs: []string{id}, Attachments: []*domain.RunAttachment{{Path: "/tmp/marked-copy.png", Labels: []string{}}}})
		images := ImagesFor(&written.State, NewThreadDraft)
		if len(images) != 1 || images[0].Path != "/tmp/marked-copy.png" {
			t.Fatalf("draft images: %s", coreJSON(t, images))
		}
	})
}

func TestMarkAllReadFollowsTheComputerFilter(t *testing.T) {
	corePorted(t, func() {
		unread := func(id string) *domain.Thread {
			return coreTask(id, func(t *domain.Thread) {
				t.Outcome, t.OutcomeUnread = new(domain.ThreadOutcomeFinished), new(true)
				t.Findings = []*domain.AutomationFinding{{ID: id + "-f", Headline: "Found", At: 1}}
			})
		}
		paired := func(id string) *PairedComputer {
			return &PairedComputer{
				ComputerLink: domain.ComputerLink{ID: id, Name: id, Host: id + ".tail.ts.net", Status: domain.ComputerStatusConnected, PairedAt: 1, Capabilities: contracts.ComputerCapabilitiesComputerCapabilities()},
				State:        coreWorkspace(func(s *WorkspaceState) { s.Threads = []*domain.Thread{unread(id)} }),
			}
		}
		computers := []*PairedComputer{paired("one"), paired("two")}
		for filter, forwards := range map[string][]string{"this": {}, "one": {"one"}, "all": {"one", "two"}} {
			state := coreWorkspace(func(s *WorkspaceState) {
				s.Threads = []*domain.Thread{unread("local"), coreTask("quiet")}
				s.Computers.Name, s.Computers.Paired, s.Computers.Filter = "This Mac", computers, filter
			})
			read := Reduce(state, contracts.TaskCommandTaskReadAll{})
			local := read.State.Threads[0]
			if (local.OutcomeUnread != nil) != (filter == "one") || *local.Outcome != domain.ThreadOutcomeFinished || read.State.Threads[1] != state.Threads[1] {
				t.Fatalf("%s: local %s", filter, coreJSON(t, local))
			}
			forwarded := []string{}
			for _, effect := range read.Effects {
				if forward, ok := effect.(WorkspaceEffectComputerForward); ok {
					forwarded = append(forwarded, forward.ID)
				}
			}
			threadsSame(t, forwarded, forwards, filter)
		}
		older := &PairedComputer{
			ComputerLink: domain.ComputerLink{ID: "older", Name: "older", Status: domain.ComputerStatusConnected, Capabilities: []string{}},
			State:        coreWorkspace(func(s *WorkspaceState) { s.Threads = []*domain.Thread{unread("remote")} }),
		}
		state := coreWorkspace(func(s *WorkspaceState) { s.Computers.Paired = []*PairedComputer{older} })
		refused := Reduce(state, contracts.TaskCommandTaskReadAll{})
		if refused.State.ActionError == nil || len(*refused.State.ActionError) < 6 || (*refused.State.ActionError)[:6] != "older:" {
			t.Fatalf("refusal %v", refused.State.ActionError)
		}
	})
}
