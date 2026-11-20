package application

import (
	"cmp"
	"slices"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// `host` may be nil for this computer's own projects.
func rowsOf(projects []*domain.Project, threads []*domain.Thread, host *ThreadHost, collator *collate.Collator) []*ProjectSettingsRow {
	counts := map[string]float64{}
	for _, thread := range threads {
		if thread.ProjectID != nil && *thread.ProjectID != "" && thread.ArchivedAt == nil {
			counts[*thread.ProjectID]++
		}
	}
	rows := []*ProjectSettingsRow{}
	for _, project := range projects {
		if project.Unlisted != nil && *project.Unlisted {
			continue
		}
		rows = append(rows, &ProjectSettingsRow{Project: *project, Host: host, Threads: counts[project.ID]})
	}
	slices.SortStableFunc(rows, func(a, b *ProjectSettingsRow) int {
		return cmp.Or(
			collator.CompareString(domain.ProjectName(&a.Project), domain.ProjectName(&b.Project)),
			collator.CompareString(a.Project.Root, b.Project.Root),
		)
	})
	return rows
}

// Every computer's listed projects, whatever the sidebar is narrowed to: this computer's first, then
// each paired computer's by name. A side chat counts, since removing its project archives it too.
func ProjectSettingsRows(state *WorkspaceState) []*ProjectSettingsRow {
	collator := collate.New(language.Und)
	paired := slices.Clone(state.Computers.Paired)
	slices.SortStableFunc(paired, func(a, b *PairedComputer) int {
		return cmp.Or(collator.CompareString(a.Name, b.Name), collator.CompareString(a.ID, b.ID))
	})
	rows := rowsOf(state.Projects, state.Threads, nil, collator)
	for _, computer := range paired {
		if computer.State != nil {
			rows = append(rows, rowsOf(computer.State.Projects, computer.State.Threads, ThreadHostOf(&computer.ComputerLink), collator)...)
		}
	}
	return rows
}
