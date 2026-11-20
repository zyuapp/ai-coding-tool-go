package application

import (
	"cmp"
	"slices"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Ignores `worktree.Safe`.
func SafeToDelete(worktree *WorktreeSettingsView) bool {
	status := worktree.Status
	return worktree.Available && worktree.Repository != nil && *worktree.Repository != "" &&
		status.ChangedFiles != nil && *status.ChangedFiles == 0 &&
		status.Comparison != nil && status.Comparison.Ahead == 0 && !worktree.Busy && !worktree.Deleting
}

// The last non-empty segment of a path, as `split("/").filter(Boolean).at(-1)` finds it.
func lastSegment(path string) (string, bool) {
	segments := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	if len(segments) == 0 {
		return "", false
	}
	return segments[len(segments)-1], true
}

// Nil until the managed list has landed.
func WorktreeSettingsViews(state *WorkspaceState, busy jsonx.Set[string]) jsonx.NullSlice[*WorktreeSettingsView] {
	if state.ManagedWorktrees == nil {
		return nil
	}
	onDisk := make(map[string]*domain.ManagedWorktree, len(state.ManagedWorktrees))
	for _, worktree := range state.ManagedWorktrees {
		onDisk[worktree.Root] = worktree
	}
	projectsByID := map[string]*domain.Project{}
	projectsByRoot := map[string]*domain.Project{}
	for _, project := range state.Projects {
		projectsByID[project.ID] = project
		projectsByRoot[project.Root] = project
	}
	recordsByRoot := map[string]*domain.Worktree{}
	recordsByID := map[string]*domain.Worktree{}
	for _, worktree := range state.Worktrees {
		recordsByRoot[worktree.Root] = worktree
		recordsByID[worktree.ID] = worktree
	}
	threadsByWorktree := map[string][]*WorktreeSettingsViewThreads{}
	for _, thread := range state.Threads {
		if thread.WorktreeID == nil || *thread.WorktreeID == "" {
			continue
		}
		threadsByWorktree[*thread.WorktreeID] = append(threadsByWorktree[*thread.WorktreeID], &WorktreeSettingsViewThreads{ID: thread.ID, Title: thread.Title, Archived: thread.ArchivedAt != nil})
	}
	directories := slices.Clone([]*domain.ManagedWorktree(state.ManagedWorktrees))
	for _, worktree := range state.Worktrees {
		if _, ok := onDisk[worktree.Root]; ok {
			continue
		}
		var repository *string
		if project := projectsByID[worktree.ProjectID]; project != nil {
			repository = &project.Root
		}
		directories = append(directories, &domain.ManagedWorktree{
			ID: worktree.ID, Root: worktree.Root, Repository: repository, Branch: nil,
			Status: domain.WorktreeStatus{ChangedFiles: nil, Comparison: nil},
		})
	}
	deleting := jsonx.SetOf(state.DeletingWorktrees...)
	releasing := jsonx.SetOf(state.ReleasingWorktrees...)
	views := make(jsonx.NullSlice[*WorktreeSettingsView], len(directories))
	for index, directory := range directories {
		record := recordsByRoot[directory.Root]
		if record == nil {
			record = recordsByID[directory.ID]
		}
		key := directory.ID
		if record != nil {
			key = record.ID
		}
		threads := threadsByWorktree[key]
		if threads == nil {
			threads = []*WorktreeSettingsViewThreads{}
		}
		var project *domain.Project
		if record != nil {
			project = projectsByID[record.ProjectID]
		} else {
			repository := ""
			if directory.Repository != nil {
				repository = *directory.Repository
			}
			project = projectsByRoot[repository]
		}
		name, ok := lastSegment(directory.Root)
		if !ok {
			name = directory.ID
		}
		var title string
		switch {
		case record != nil && record.Name != nil:
			title = *record.Name
		case directory.Branch != nil:
			title = *directory.Branch
		case len(threads) > 0:
			title = threads[0].Title
		default:
			title = name
		}
		projectLabel, projectKey := "Unknown project", "unknown"
		if project != nil {
			projectLabel, projectKey = domain.ProjectName(project), project.ID
		} else if directory.Repository != nil {
			projectKey = *directory.Repository
			if segment, ok := lastSegment(*directory.Repository); ok {
				projectLabel = segment
			}
		}
		_, available := onDisk[directory.Root]
		_, deletingRoot := deleting[directory.Root]
		view := &WorktreeSettingsView{
			ManagedWorktree: *directory,
			Name:            name,
			Title:           title,
			Project:         projectLabel,
			ProjectKey:      projectKey,
			Available:       available,
			Recorded:        record != nil,
			Threads:         threads,
			Busy: slices.ContainsFunc(threads, func(thread *WorktreeSettingsViewThreads) bool {
				_, ok := busy[thread.ID]
				return ok
			}),
			Deleting: deletingRoot || slices.ContainsFunc(threads, func(thread *WorktreeSettingsViewThreads) bool {
				_, ok := releasing[thread.ID]
				return ok
			}),
		}
		view.Safe = SafeToDelete(view)
		views[index] = view
	}
	collator := collate.New(language.Und)
	slices.SortStableFunc(views, func(left, right *WorktreeSettingsView) int {
		return cmp.Or(
			collator.CompareString(left.Project, right.Project),
			collator.CompareString(left.Title, right.Title),
			collator.CompareString(left.Root, right.Root),
		)
	})
	return views
}

// `worktrees` is nil until the managed list has landed.
func WorktreeSettingsWorktreeSettingsPage(state *WorkspaceState, worktrees jsonx.NullSlice[*WorktreeSettingsView]) WorktreeSettingsPage {
	settings := state.WorktreeSettings
	var order []string
	projects := map[string]*WorktreeSettingsPageProjects{}
	for _, item := range state.Projects {
		if _, ok := projects[item.ID]; !ok {
			order = append(order, item.ID)
		}
		projects[item.ID] = &WorktreeSettingsPageProjects{Key: item.ID, Name: domain.ProjectName(item), Count: 0}
	}
	for _, worktree := range worktrees {
		option, ok := projects[worktree.ProjectKey]
		if !ok {
			option = &WorktreeSettingsPageProjects{Key: worktree.ProjectKey, Name: worktree.Project, Count: 0}
			order = append(order, option.Key)
		}
		option.Count++
		projects[option.Key] = option
	}
	var selected *string
	if settings.Project != nil && *settings.Project != "" {
		if _, ok := projects[*settings.Project]; ok {
			selected = settings.Project
		}
	}
	available := []*WorktreeSettingsView{}
	missing := []*WorktreeSettingsView{}
	var confirmation *WorktreeSettingsView
	for _, worktree := range worktrees {
		if selected != nil && worktree.ProjectKey != *selected {
			continue
		}
		if worktree.Available {
			available = append(available, worktree)
		} else {
			missing = append(missing, worktree)
		}
		if confirmation == nil && settings.Confirming != nil && worktree.Root == *settings.Confirming && !worktree.Deleting {
			confirmation = worktree
		}
	}
	safe := []*WorktreeSettingsView{}
	for _, worktree := range available {
		if worktree.Safe {
			safe = append(safe, worktree)
		}
	}
	options := make([]*WorktreeSettingsPageProjects, len(order))
	for index, key := range order {
		options[index] = projects[key]
	}
	collator := collate.New(language.Und)
	slices.SortStableFunc(options, func(left, right *WorktreeSettingsPageProjects) int {
		return collator.CompareString(left.Name, right.Name)
	})
	missingOpen := len(available) == 0 && len(missing) > 0
	if settings.MissingOpen != nil {
		missingOpen = *settings.MissingOpen
	}
	var safeConfirmation jsonx.NullSlice[*WorktreeSettingsView]
	if settings.ConfirmingSafe && len(safe) > 0 {
		safeConfirmation = safe
	}
	return WorktreeSettingsPage{
		Loading:          state.WorktreeManagementLoading,
		Total:            float64(len(worktrees)),
		Project:          selected,
		Projects:         options,
		Available:        available,
		Missing:          missing,
		MissingOpen:      missingOpen,
		ExpandedThreads:  settings.ExpandedThreads,
		Confirmation:     confirmation,
		Safe:             safe,
		SafeConfirmation: safeConfirmation,
	}
}
