package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// What the workspace puts on the desktop when a thread needs the user. The window decides whether
// the user is somewhere it has to be reached; this only decides whether there is anything to say.

// The longest line a coordinator's reply puts on the desktop.
const posted = 120

// The thread a side chat sits beside, which is whose notices it shares; any other thread is its own.
func ownerOf(state *WorkspaceState, thread *domain.Thread) *domain.Thread {
	for _, chat := range state.SideChats {
		if chat.ID == thread.ID {
			return threadsFind(state.Threads, chat.SourceThreadID)
		}
	}
	return thread
}

// threadsFind is the thread with that id, or nil.
func threadsFind(threads []*domain.Thread, id string) *domain.Thread {
	for _, thread := range threads {
		if thread.ID == id {
			return thread
		}
	}
	return nil
}

// The coordinator a thread's notices answer to: the thread itself when it is one, else the one it
// works under, which for a paired computer's thread a coordinator here started is that coordinator.
func leadOf(state *WorkspaceState, thread *domain.Thread) *domain.Thread {
	if domain.IsCoordinator(thread) {
		return thread
	}
	remote := domain.RemoteLeadOf(thread)
	if remote == nil {
		return domain.CoordinatorOf(state.Threads, thread)
	}
	if lead := threadsFind(state.Threads, remote.ThreadID); domain.IsCoordinator(lead) {
		return lead
	}
	return nil
}

// What a notice calls the thread. A side chat only exists beside another thread, and a thread under
// a coordinator is one of its threads, so each names both, wherever that coordinator is.
func noticeTitle(state *WorkspaceState, thread *domain.Thread) string {
	if owner := ownerOf(state, thread); owner != thread {
		if owner != nil {
			return owner.Title + " · " + thread.Title
		}
		return thread.Title
	}
	lead := ""
	if !domain.IsCoordinator(thread) {
		if coordinator := leadOf(state, thread); coordinator != nil {
			lead = coordinator.Title
		} else if remote := domain.RemoteLeadOf(thread); remote != nil {
			lead = remote.Title
		}
	}
	if lead != "" {
		return lead + " · " + thread.Title
	}
	return thread.Title
}

// Whether a thread works under a coordinator on another computer. That computer hears all it has to
// say, and says it there under that coordinator's mute, so this one would only say it twice.
func heardElsewhere(state *WorkspaceState, thread *domain.Thread) bool {
	remote := domain.RemoteLeadOf(thread)
	return remote != nil && threadsFind(state.Threads, remote.ThreadID) == nil
}

// Whether the user wants to hear this. Reviewers keep their notices in the app. A muted coordinator
// keeps back everything from itself and its threads. A turn its thread finished speaks through the
// coordinator's reply, so only a turn the user's own words started is theirs to hear about.
func wanted(state *WorkspaceState, thread *domain.Thread, reason NoticeReason) bool {
	if thread.Role != nil && *thread.Role == domain.ThreadRoleReviewer {
		return false
	}
	if heardElsewhere(state, thread) {
		return false
	}
	if lead := leadOf(state, ownerOf(state, thread)); lead != nil && lead.Muted != nil && *lead.Muted {
		return false
	}
	if reason != NoticeReasonFinished || !domain.WorksUnderCoordinator(state.Threads, thread) {
		return true
	}
	latest := latestUserMessage(thread)
	return latest == nil || latest.Origin == nil
}

func latestUserMessage(thread *domain.Thread) *domain.ConversationMessage {
	for index := len(thread.Messages) - 1; index >= 0; index-- {
		message := thread.Messages[index]
		if message.Kind == domain.ConversationMessageKindUser && (message.Withdrawn == nil || !*message.Withdrawn) {
			return message
		}
	}
	return nil
}

// What a run waiting on the user says on the desktop: the tool it asks to use (ToolName set), or the question it asks.
func WaitingHeadline(waiting MemberFactWaitingOn) string {
	if waiting.ToolName != nil {
		return "Waiting for your permission to use " + domain.ToolLabel(*waiting.ToolName)
	}
	if waiting.Question != nil {
		return *waiting.Question
	}
	return ""
}

// A line a thread wants in front of the user, kept back while notifications are off or the user does not want it.
func Announced(state *WorkspaceState, thread *domain.Thread, headline string, reason NoticeReason) []WorkspaceEffect {
	if !wanted(state, thread, reason) {
		return []WorkspaceEffect{}
	}
	return AnnouncedNotice(state, contracts.ThreadNotice{TaskID: thread.ID, Title: noticeTitle(state, thread), Headline: headline})
}

// A notice already worded, such as one for a thread on another computer, under the same switch.
func AnnouncedNotice(state *WorkspaceState, notice contracts.ThreadNotice) []WorkspaceEffect {
	if !state.Notifications {
		return []WorkspaceEffect{}
	}
	return []WorkspaceEffect{WorkspaceEffectAnnounceThread{Notice: notice}}
}

// What a coordinator's finished turn says on the desktop: the first line of what it replied in that
// turn that says something, read as plain words, which is the coordinator posting, or false when it
// replied nothing.
func PostedHeadline(thread *domain.Thread, from int) (string, bool) {
	reply, ok := LastReply(thread, from)
	if !ok || reply == "" {
		return "", false
	}
	return ReplyLine(reply, posted)
}
