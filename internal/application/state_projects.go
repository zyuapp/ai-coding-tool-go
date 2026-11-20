package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

func InitialProjectsState() ProjectsState {
	return ProjectsState{Projects: []*domain.Project{}, ProjectEdit: nil}
}
