package domain

// MaxFindings is how many findings a thread keeps. Past this the oldest is dropped.
const MaxFindings = 10

// MaxHandledIssues is the handled issues held per thread.
const MaxHandledIssues = 50

// What one finding may carry: a line for the sidebar, a body for the thread, and a name to match it on.
const (
	MaxHeadline   = 200
	MaxDetail     = 10_000
	MaxFindingKey = 200
)
