package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// SidebarListing is every list the sidebar draws (TS: sidebarLists' return).
type SidebarListing struct {
	Projects             []*domain.Project
	OrderedThreads       []*domain.Thread
	ThreadsByCoordinator map[string][]*domain.Thread
	WaitingLines         map[string]string
	ActivityThreads      ActivitySections
	ActivityProject      *string
	// The threads ⌘1 through ⌘9 reach, in the order they are drawn.
	ThreadSlots []string
}

// orderSidebarProjects: each computer owns its project order. Keep this computer first and paired
// computers together. `hosts` may be nil.
func orderSidebarProjects(projects []*domain.Project, hosts map[string]*ThreadHost) []*domain.Project {
	if len(hosts) == 0 {
		return OrderProjects(projects)
	}
	local := []*domain.Project{}
	var computers []string
	groups := map[string][]*domain.Project{}
	for _, project := range projects {
		host := hosts[project.ID]
		if host == nil {
			local = append(local, project)
			continue
		}
		if _, seen := groups[host.ID]; !seen {
			computers = append(computers, host.ID)
		}
		groups[host.ID] = append(groups[host.ID], project)
	}
	ordered := OrderProjects(local)
	for _, computer := range computers {
		ordered = append(ordered, OrderProjects(groups[computer])...)
	}
	return ordered
}

// SidebarLists is every list the sidebar draws, in the order it draws them. `projectHosts` may be nil.
func SidebarLists(preferences SidebarPreferences, projects []*domain.Project, visibleThreads []*domain.Thread, busy, blocked jsonx.Set[string], projectHosts map[string]*ThreadHost) SidebarListing {
	orderedThreads := OrderThreads(visibleThreads)
	// A thread working under a coordinator is drawn under that coordinator's row, in no list of its own,
	// as is a paired computer's thread a coordinator here started.
	members := domain.CoordinatedThreadIds(visibleThreads)
	threadsByCoordinator := domain.ThreadsByCoordinator(orderedThreads)
	listed := make([]*domain.Project, 0, len(projects))
	for _, project := range projects {
		if project.Unlisted == nil || !*project.Unlisted {
			listed = append(listed, project)
		}
	}
	ordered := orderSidebarProjects(listed, projectHosts)
	// A project no longer listed narrows nothing, so a removed or unreachable one never empties the lists.
	var activityProject *string
	if preferences.ActivityProject != nil && slices.ContainsFunc(ordered, func(project *domain.Project) bool { return project.ID == *preferences.ActivityProject }) {
		activityProject = preferences.ActivityProject
	}
	ranked := CoordinationSections(visibleThreads, busy, blocked, preferences.StartingProposals)
	// What each row says waits on the user: a coordinator's for its threads as well as itself, and a thread drawn under one for itself.
	waitingLines := map[string]string{}
	for _, thread := range visibleThreads {
		var under []*domain.Thread
		if !members.Has(thread.ID) {
			under = threadsByCoordinator[thread.ID]
		}
		if line := WaitingLine(domain.CoordinationWaitingOnUser(thread, under, preferences.StartingProposals), ""); line != nil && *line != "" {
			waitingLines[thread.ID] = *line
		}
	}
	activityThreads := ranked
	if activityProject != nil {
		activityThreads = ProjectActivity(ranked, visibleThreads, *activityProject)
	}
	return SidebarListing{
		Projects:             ordered,
		OrderedThreads:       orderedThreads,
		ThreadsByCoordinator: threadsByCoordinator,
		WaitingLines:         waitingLines,
		ActivityThreads:      activityThreads,
		ActivityProject:      activityProject,
		ThreadSlots:          SlotThreadIds(preferences.Sections, activityThreads, domain.SlotCount),
	}
}
