package domain

// ThreadRoleOption is one role and its name.
type ThreadRoleOption struct {
	Role  ThreadRole
	Label string
}

var ThreadRoles = []ThreadRoleOption{
	{Role: ThreadRoleCoordinator, Label: "Coordinator"},
	{Role: ThreadRoleImplementer, Label: "Implementer"},
	{Role: ThreadRoleReviewer, Label: "Reviewer"},
	{Role: ThreadRoleResearcher, Label: "Researcher"},
}

// IsThreadRole accepts a ThreadRole or a string.
func IsThreadRole(value any) bool {
	var role ThreadRole
	switch v := value.(type) {
	case ThreadRole:
		role = v
	case string:
		role = ThreadRole(v)
	default:
		return false
	}
	for _, option := range ThreadRoles {
		if option.Role == role {
			return true
		}
	}
	return false
}

// ThreadRoleLabel is a role's name, or its own id for one this build does not know.
func ThreadRoleLabel(role ThreadRole) string {
	for _, option := range ThreadRoles {
		if option.Role == role {
			return option.Label
		}
	}
	return string(role)
}
