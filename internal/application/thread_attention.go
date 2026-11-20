package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// What threads have left unseen, once side chats and coordinators' threads are counted. Neither has
// a row of its own, so its mark belongs to the row that speaks for it.

// Whether the user is looking at a thread. A thread under the current coordinator is, since its
// conversation and Overview show where each stands. A side chat is on screen when the thread whose
// dock holds it is current and that dock is showing its tab. While a paired computer's thread is on
// screen, none of this computer's own are.
func ThreadOnScreen(state *WorkspaceState, taskID string) bool {
	if state.Computers.Active != nil && *state.Computers.Active != "" {
		return false
	}
	if state.CurrentID != nil && *state.CurrentID == taskID {
		return true
	}
	var thread *domain.Thread
	for _, item := range state.Threads {
		if item.ID == taskID {
			thread = item
			break
		}
	}
	if state.CurrentID != nil {
		if coordinator := domain.CoordinatorOf(state.Threads, thread); coordinator != nil && coordinator.ID == *state.CurrentID {
			return true
		}
	}
	holder, ok := TabHolderOf(state, taskID)
	if !ok || holder == "" || state.CurrentID == nil || *state.CurrentID != holder {
		return false
	}
	dock := DockFor(state, holder)
	return dock.Open && dock.Tab == taskID
}

// The marks the sidebar and the app icon share, with every side chat folded into its source thread
// and every thread under a coordinator into the coordinator, whose row speaks for it. The icon counts
// rows, so it says what the sidebar does. Answers sideChatAttention and unreadCount.
func UnreadView(state *WorkspaceState, listed []*domain.Thread) (jsonx.Set[string], float64) {
	sideChatAttention := jsonx.Set[string]{}
	for _, chat := range state.SideChats {
		for _, thread := range state.Threads {
			if thread.ID == chat.ID {
				if domain.HasUnreadAttention(thread) {
					sideChatAttention[chat.SourceThreadID] = struct{}{}
				}
				break
			}
		}
	}
	members := domain.CoordinatedThreadIds(listed)
	rows := jsonx.Set[string]{}
	for _, thread := range listed {
		_, folded := sideChatAttention[thread.ID]
		if !domain.HasUnreadAttention(thread) && !folded {
			continue
		}
		row := thread.ID
		if _, member := members[thread.ID]; member {
			row, _ = domain.LeadIDOf(thread)
		}
		rows[row] = struct{}{}
	}
	return sideChatAttention, float64(len(rows))
}

// Landing on a thread reads it, and landing on a coordinator reads the threads under it too, as its
// row speaks for them. The same list when nothing was unread.
func ReadRow(threads []*domain.Thread, threadID *string) []*domain.Thread {
	if threadID == nil || *threadID == "" {
		return threads
	}
	read := domain.ReadAttention(threads, *threadID)
	for _, member := range domain.CoordinatedThreads(read, *threadID) {
		read = domain.ReadAttention(read, member.ID)
	}
	return read
}
