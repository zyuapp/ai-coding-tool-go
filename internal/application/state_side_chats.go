package application

import "github.com/zyuapp/ai-coding-tool-go/internal/jsonx"

func InitialSideChatsState() SideChatsState {
	return SideChatsState{SideChats: []*SideChat{}, SideChatSequence: 0}
}

func SideChatIds(state *WorkspaceState) jsonx.Set[string] {
	ids := make(jsonx.Set[string], len(state.SideChats))
	for _, chat := range state.SideChats {
		ids[chat.ID] = struct{}{}
	}
	return ids
}
