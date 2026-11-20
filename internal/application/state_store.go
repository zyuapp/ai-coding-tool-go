package application

func InitialStoreState(storageError *string) StoreState {
	return StoreState{StorageError: storageError, HiddenThreads: 0, Writable: storageError == nil, Restored: false}
}

func InitialErrorsState() ErrorsState {
	return ErrorsState{ActionError: nil, ActionErrorPage: nil}
}
