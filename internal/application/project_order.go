package application

import (
	"cmp"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Sidebar order. `sortIndex` wins so folders never move on their own; projects stored before
// sortIndex existed keep the order they were stored in until BackfillProjectSortIndex pins them down.
func OrderProjects(projects []*domain.Project) []*domain.Project {
	ordered := slices.Clone(projects)
	slices.SortStableFunc(ordered, func(left, right *domain.Project) int {
		switch {
		case left.SortIndex != nil && right.SortIndex != nil:
			return cmp.Compare(*left.SortIndex, *right.SortIndex)
		case left.SortIndex != nil:
			return -1
		case right.SortIndex != nil:
			return 1
		}
		return 0
	})
	return ordered
}

// Freezes the order projects loaded in, so a first launch after upgrading keeps the list the user last saw.
func BackfillProjectSortIndex(projects []*domain.Project) []*domain.Project {
	if !slices.ContainsFunc(projects, func(project *domain.Project) bool { return project.SortIndex == nil }) {
		return projects
	}
	positions := make(map[string]float64, len(projects))
	for index, project := range OrderProjects(projects) {
		positions[project.ID] = float64(index)
	}
	filled := make([]*domain.Project, len(projects))
	for i, project := range projects {
		if project.SortIndex != nil {
			filled[i] = project
			continue
		}
		next := *project
		position := positions[project.ID]
		next.SortIndex = &position
		filled[i] = &next
	}
	return filled
}

func NextProjectSortIndex(projects []*domain.Project) float64 {
	lowest := 0.0
	for _, project := range projects {
		index := 0.0
		if project.SortIndex != nil {
			index = *project.SortIndex
		}
		lowest = min(lowest, index)
	}
	return lowest - 1
}

// Names a folder in the sidebar. An empty name takes the last one off, so it goes by its folder
// again. An absent `name` changes nothing; null takes the name off.
func NameProject(projects []*domain.Project, projectID string, name jsonx.Opt[string]) []*domain.Project {
	if name.IsAbsent() {
		return projects
	}
	var wanted *string
	if value, ok := name.Get(); ok {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			wanted = &trimmed
		}
	}
	index := slices.IndexFunc(projects, func(item *domain.Project) bool { return item.ID == projectID })
	if index < 0 {
		return projects
	}
	project := projects[index]
	if (project.Name == nil && wanted == nil) || (project.Name != nil && wanted != nil && *project.Name == *wanted) {
		return projects
	}
	named := slices.Clone(projects)
	for i, item := range named {
		if item.ID == projectID {
			next := *item
			next.Name = wanted
			named[i] = &next
		}
	}
	return named
}
