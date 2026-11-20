package domain

import "slices"

var SidebarSidebarSections = []SidebarSection{SidebarSectionPriority, SidebarSectionRunning, SidebarSectionThreads, SidebarSectionSnoozed}

// OpenSidebarSections is every list unfolded. Shared: never write into it.
var OpenSidebarSections = SidebarSections{SidebarSectionPriority: true, SidebarSectionRunning: true, SidebarSectionThreads: true, SidebarSectionSnoozed: true}

func IsSidebarSection(value any) bool {
	switch v := value.(type) {
	case SidebarSection:
		return slices.Contains(SidebarSidebarSections, v)
	case string:
		return slices.Contains(SidebarSidebarSections, SidebarSection(v))
	}
	return false
}
