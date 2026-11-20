package domain

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// FindingReport is what a run reports to WithFinding. An empty Detail or Key is the same as none.
type FindingReport struct {
	Headline string
	Detail   string
	Key      string
}

func UnreadFindings(thread *Thread) []*AutomationFinding {
	unread := []*AutomationFinding{}
	for _, finding := range thread.Findings {
		if finding.Read == nil || !*finding.Read {
			unread = append(unread, finding)
		}
	}
	return unread
}

func NewestUnreadFinding(thread *Thread) *AutomationFinding {
	for i := len(thread.Findings) - 1; i >= 0; i-- {
		if finding := thread.Findings[i]; finding.Read == nil || !*finding.Read {
			return finding
		}
	}
	return nil
}

func hasFindings(thread *Thread) bool {
	return len(thread.Findings) > 0
}

func WantsAttention(thread *Thread) bool {
	return thread.Outcome != nil || hasFindings(thread)
}

func HasUnreadAttention(thread *Thread) bool {
	return NewestUnreadFinding(thread) != nil || thread.Outcome != nil && attentionOutcomeUnread(thread)
}

func attentionOutcomeUnread(thread *Thread) bool {
	return thread.OutcomeUnread != nil && *thread.OutcomeUnread
}

func findingKeys(thread *Thread) []string {
	keys := []string{}
	for _, finding := range thread.Findings {
		if finding.Key != nil {
			keys = append(keys, *finding.Key)
		}
	}
	return keys
}

func handledKeys(thread *Thread) []string {
	return thread.HandledIssues
}

// IssueState is "unknown", "carried" or "handled". An empty key is no key.
func IssueState(thread *Thread, key string) string {
	if key == "" {
		return "unknown"
	}
	if slices.Contains(findingKeys(thread), key) {
		return "carried"
	}
	if slices.Contains(handledKeys(thread), key) {
		return "handled"
	}
	return "unknown"
}

func IsNews(thread *Thread, key string) bool {
	return IssueState(thread, key) == "unknown"
}

func WithFinding(thread *Thread, report FindingReport, at float64, seen bool) *Thread {
	if !IsNews(thread, report.Key) {
		return thread
	}
	finding := &AutomationFinding{ID: NewID(), Headline: report.Headline, At: at}
	var detail *string
	if report.Detail != "" {
		finding.Detail = new(report.Detail)
		detail = new(report.Detail)
	}
	if report.Key != "" {
		finding.Key = new(report.Key)
	}
	if seen {
		finding.Read = new(true)
	}
	findings := append(slices.Clone(thread.Findings), finding)
	if len(findings) > MaxFindings {
		findings = findings[len(findings)-MaxFindings:]
	}
	next := *thread
	next.Findings = findings
	next.LastFindingAt = new(at)
	next.Messages = append(slices.Clone(thread.Messages), CreateConversationMessage(ConversationMessageKindSystem, report.Headline, detail, nil, nil, nil, nil))
	next.UpdatedAt = at
	return &next
}

func WithReadFindings(thread *Thread) *Thread {
	if NewestUnreadFinding(thread) == nil {
		return thread
	}
	findings := make([]*AutomationFinding, len(thread.Findings))
	for i, finding := range thread.Findings {
		if finding.Read != nil && *finding.Read {
			findings[i] = finding
			continue
		}
		read := *finding
		read.Read = new(true)
		findings[i] = &read
	}
	next := *thread
	next.Findings = findings
	return &next
}

func WithoutFindings(thread *Thread) *Thread {
	if thread.Findings == nil {
		return thread
	}
	next := *thread
	next.Findings = nil
	return &next
}

// ReadAttention takes the marks off the thread threadID names ("" for none), returning threads itself when nothing changes.
func ReadAttention(threads []*Thread, threadID string) []*Thread {
	if threadID == "" {
		return threads
	}
	index := slices.IndexFunc(threads, func(thread *Thread) bool { return thread.ID == threadID })
	if index < 0 {
		return threads
	}
	seen := threads[index]
	if !attentionOutcomeUnread(seen) && NewestUnreadFinding(seen) == nil {
		return threads
	}
	rest := *seen
	rest.OutcomeUnread = nil
	next := slices.Clone(threads)
	for i, thread := range threads {
		if thread == seen {
			next[i] = WithReadFindings(&rest)
		}
	}
	return next
}

func WithAllRead(threads []*Thread) []*Thread {
	if !slices.ContainsFunc(threads, HasUnreadAttention) {
		return threads
	}
	next := make([]*Thread, len(threads))
	for i, thread := range threads {
		if !HasUnreadAttention(thread) {
			next[i] = thread
			continue
		}
		rest := *thread
		rest.OutcomeUnread = nil
		next[i] = WithReadFindings(&rest)
	}
	return next
}

func WithoutOutcome(threads []*Thread, dismissing jsonx.Set[string]) []*Thread {
	carries := func(thread *Thread) bool { return dismissing.Has(thread.ID) && thread.Outcome != nil }
	if !slices.ContainsFunc(threads, carries) {
		return threads
	}
	next := make([]*Thread, len(threads))
	for i, thread := range threads {
		if !carries(thread) {
			next[i] = thread
			continue
		}
		rest := *thread
		rest.Outcome = nil
		rest.OutcomeUnread = nil
		next[i] = &rest
	}
	return next
}

func Dismissed(threads []*Thread, dismissing jsonx.Set[string]) []*Thread {
	filed := WithoutOutcome(threads, dismissing)
	if !slices.ContainsFunc(filed, func(thread *Thread) bool { return dismissing.Has(thread.ID) && thread.Findings != nil }) {
		return filed
	}
	next := make([]*Thread, len(filed))
	for i, thread := range filed {
		if dismissing.Has(thread.ID) {
			next[i] = WithoutFindings(withHandledIssues(thread))
		} else {
			next[i] = thread
		}
	}
	return next
}

func withHandledIssues(thread *Thread) *Thread {
	keys := findingKeys(thread)
	if len(keys) == 0 {
		return thread
	}
	handled := []string{}
	for _, key := range handledKeys(thread) {
		if IssueState(thread, key) != "carried" {
			handled = append(handled, key)
		}
	}
	handled = append(handled, keys...)
	if len(handled) > MaxHandledIssues {
		handled = handled[len(handled)-MaxHandledIssues:]
	}
	next := *thread
	next.HandledIssues = handled
	return &next
}

func WithClosedIssues(thread *Thread, reportedIssues []string) *Thread {
	held := handledKeys(thread)
	still := []string{}
	for _, key := range held {
		if slices.Contains(reportedIssues, key) {
			still = append(still, key)
		}
	}
	if len(still) == len(held) {
		return thread
	}
	next := *thread
	if len(still) == 0 {
		next.HandledIssues = nil
	} else {
		next.HandledIssues = still
	}
	return &next
}

const MemberBlockedLabel = "A thread needs your approval"

const MemberUnreadLabel = "A thread has something new"
