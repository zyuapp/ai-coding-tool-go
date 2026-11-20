package application

import "github.com/zyuapp/ai-coding-tool-go/internal/jsonx"

// Side chats as one input leaves them. Only the functions here make one.
type sideChatsChange = SideChatsEdit

func SideChatsOf(state *WorkspaceState) SideChatsEdit {
	return SideChatsEdit{View: SideChatsState{SideChats: state.SideChats, SideChatSequence: state.SideChatSequence}}
}

// A side chat whose run could not start, saying why in the dock rather than in the main thread's banner.
func ChatFailedIn(edit SideChatsEdit, chatID, err string) SideChatsEdit {
	view := edit.View
	chats := make([]*SideChat, len(view.SideChats))
	for index, chat := range view.SideChats {
		if chat.ID == chatID {
			failed := *chat
			message := err
			failed.Error = &message
			chat = &failed
		}
		chats[index] = chat
	}
	view.SideChats = chats
	return SideChatsEdit{View: view}
}

// Threads gone for good take the side chats they were, or were forked from, with them.
func SideChatsForgottenIn(edit SideChatsEdit, removed jsonx.Set[string]) SideChatsEdit {
	view := edit.View
	view.SideChats = WithoutMatching(view.SideChats, func(chat *SideChat) bool { return removed.Has(chat.ID) || removed.Has(chat.SourceThreadID) })
	return SideChatsEdit{View: view}
}
