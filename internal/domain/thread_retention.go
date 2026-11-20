package domain

import (
	"math"
	"slices"
)

const ArchiveRetentionMs = 5 * 24 * 60 * 60 * 1000

// ArchiveQuestion is the question asked before the user archives a thread, on every screen.
func ArchiveQuestion(title string) string {
	if title == "" {
		title = "Untitled thread"
	}
	return "Archive “" + title + "”?"
}

// ArchiveConsequence is what archiving does, said under ArchiveQuestion.
func ArchiveConsequence(running bool, phone bool) string {
	days := jsNumber(math.Floor(float64(ArchiveRetentionMs)/86_400_000 + 0.5))
	where := "Settings › " + SettingsPageLabels[SettingsSectionArchive]
	list, subject := "sidebar", "It"
	if phone {
		where += " on your computer"
		list = "list"
	}
	if running {
		subject = "Its run stops and it"
	}
	return subject + " leaves the " + list + ". Restore it from " + where + " within " + days + " days."
}

// RetainedThreads drops threads archived at least ArchiveRetentionMs before at. It returns threads
// itself when nothing is dropped.
func RetainedThreads(threads []*StoredThread, at float64) []*StoredThread {
	var retained []*StoredThread
	for index, thread := range threads {
		if thread.ArchivedAt != nil && at-*thread.ArchivedAt >= ArchiveRetentionMs {
			if retained == nil {
				retained = slices.Clone(threads[:index:index])
				if retained == nil {
					retained = []*StoredThread{}
				}
			}
		} else if retained != nil {
			retained = append(retained, thread)
		}
	}
	if retained == nil {
		return threads
	}
	return retained
}
