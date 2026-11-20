package domain

// UnknownWorkspaceError is the TypeScript class of the same name.
type UnknownWorkspaceError struct {
	WorkspaceID string
}

func (e *UnknownWorkspaceError) Error() string { return "Unknown workspace: " + e.WorkspaceID }

// WorkspaceRegistrationError is the TypeScript class of the same name.
type WorkspaceRegistrationError struct {
	Root    string
	Message string
}

func (e *WorkspaceRegistrationError) Error() string { return e.Message }
