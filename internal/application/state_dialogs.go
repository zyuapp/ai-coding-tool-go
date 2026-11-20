package application

func InitialDialogsState() DialogsState {
	return DialogsState{ForkName: nil, ArchivePrompt: nil, OpenMenu: nil, ReviewPicker: nil}
}

const ThreadStartProjectMenu = "thread-start:project"
