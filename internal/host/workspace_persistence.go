package host

import (
	"context"
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// recast copies a value into a type holding the same JSON, leaving out what the target does not hold.
func recast[T any](value any) T {
	var out T
	raw, err := jsonx.Marshal(value)
	if err == nil {
		err = jsonx.Unmarshal(raw, &out)
	}
	if err != nil {
		panic("host: " + err.Error())
	}
	return out
}

func persistedTask(thread *domain.Thread) contracts.PersistedTask {
	record := *thread
	record.Messages, record.HistorySummary = nil, nil
	return recast[contracts.PersistedTask](&record)
}

func persistedSubagent(subagent *domain.Subagent) contracts.PersistedSubagent {
	record := *subagent
	record.Activity = nil
	return recast[contracts.PersistedSubagent](&record)
}

// subagentDelta is only the subagents and activity items the last write did not already hold.
func subagentDelta(before, held []*domain.Subagent) ([]*contracts.TaskStoreDeltaTasksSubagents, []*contracts.TaskStoreDeltaTasksActivity) {
	subagents := []*contracts.TaskStoreDeltaTasksSubagents{}
	activity := []*contracts.TaskStoreDeltaTasksActivity{}
	if jsonx.SameSlice(before, held) && (before == nil) == (held == nil) {
		return subagents, activity
	}
	previous := make(map[string]*domain.Subagent, len(before))
	for _, subagent := range before {
		previous[subagent.ID] = subagent
	}
	for index, subagent := range held {
		stored := previous[subagent.ID]
		if stored == subagent {
			continue
		}
		subagents = append(subagents, &contracts.TaskStoreDeltaTasksSubagents{Index: float64(index), Subagent: persistedSubagent(subagent)})
		if stored != nil && jsonx.SameSlice(stored.Activity, subagent.Activity) {
			continue
		}
		for position, item := range subagent.Activity {
			if stored != nil && position < len(stored.Activity) && stored.Activity[position] == item {
				continue
			}
			activity = append(activity, &contracts.TaskStoreDeltaTasksActivity{SubagentID: subagent.ID, Index: float64(position), Item: *item})
		}
	}
	return subagents, activity
}

// threadIndex maps each thread by id.
func threadIndex(threads []*domain.Thread) map[string]*domain.Thread {
	index := make(map[string]*domain.Thread, len(threads))
	for _, thread := range threads {
		index[thread.ID] = thread
	}
	return index
}

// sideChatIDs names the threads that are side chats, which are never stored.
func sideChatIDs(chats []*application.SideChat) map[string]bool {
	ids := make(map[string]bool, len(chats))
	for _, chat := range chats {
		ids[chat.ID] = true
	}
	return ids
}

// persistenceMu guards every PersistenceQueue's fields and activeSnapshots.
var persistenceMu sync.Mutex

// activeSnapshots holds, per *PersistenceQueue, the snapshot being written right now.
var activeSnapshots = map[*PersistenceQueue]*PersistenceState{}

// AdoptPersistedMessages updates every durability snapshot that still carries the unloaded thread
// summary with what a disk read loaded.
func AdoptPersistedMessages(queue *PersistenceQueue, taskID string, messages []*domain.ConversationMessage) {
	hydrate := func(snapshot *PersistenceState) *PersistenceState {
		if snapshot == nil {
			return nil
		}
		carries := false
		for _, thread := range snapshot.Threads {
			if thread.ID == taskID && thread.HistorySummary != nil {
				carries = true
				break
			}
		}
		if !carries {
			return snapshot
		}
		next := *snapshot
		next.Threads = make([]*domain.Thread, len(snapshot.Threads))
		for index, thread := range snapshot.Threads {
			if thread.ID != taskID || thread.HistorySummary == nil {
				next.Threads[index] = thread
				continue
			}
			loaded := *thread
			loaded.HistorySummary = nil
			loaded.Messages = messages
			next.Threads[index] = &loaded
		}
		return &next
	}
	persistenceMu.Lock()
	defer persistenceMu.Unlock()
	queue.Persisted = hydrate(queue.Persisted)
	queue.Pending = hydrate(queue.Pending)
	if active := activeSnapshots[queue]; active != nil {
		activeSnapshots[queue] = hydrate(active)
	}
}

func WorkspacePersistencePersistenceState(state *application.WorkspaceState) *PersistenceState {
	return &PersistenceState{
		Projects:   state.Projects,
		Worktrees:  state.Worktrees,
		Threads:    state.Threads,
		SideChats:  state.SideChats,
		Subagents:  state.Subagents,
		LastFolder: state.LastFolder,
	}
}

// HasPersistenceChanges compares by identity which persisted collections a transition changed.
func HasPersistenceChanges(previous, next *PersistenceState) bool {
	return !jsonx.SameSlice(previous.Threads, next.Threads) || !jsonx.SameMap(previous.Subagents, next.Subagents) || !jsonx.SameSlice(previous.SideChats, next.SideChats) ||
		!jsonx.SameSlice(previous.Projects, next.Projects) || !jsonx.SameSlice(previous.Worktrees, next.Worktrees) || !sameText(previous.LastFolder, next.LastFolder)
}

// PersistenceDelta is what to write to go from previous (nil before the first write) to next. A
// collection it includes is never nil, since nil means left out.
func PersistenceDelta(previous, next *PersistenceState) contracts.TaskStoreDelta {
	delta := contracts.TaskStoreDelta{Tasks: []*contracts.TaskStoreDeltaTasks{}}
	if previous == nil || !jsonx.SameSlice(previous.Projects, next.Projects) {
		delta.Projects = nonNil(next.Projects)
	}
	if previous == nil || !jsonx.SameSlice(previous.Worktrees, next.Worktrees) {
		delta.Worktrees = nonNil(next.Worktrees)
	}
	if previous == nil || !sameText(previous.LastFolder, next.LastFolder) {
		if next.LastFolder == nil {
			delta.LastFolder = jsonx.Null[string]()
		} else {
			delta.LastFolder = jsonx.Some(*next.LastFolder)
		}
	}
	threadsChanged := previous == nil || !jsonx.SameSlice(previous.Threads, next.Threads) || !jsonx.SameSlice(previous.SideChats, next.SideChats)
	if !threadsChanged && jsonx.SameMap(previous.Subagents, next.Subagents) {
		return delta
	}
	previousThreads := map[string]*domain.Thread{}
	previousSideChats := map[string]bool{}
	var previousSubagents map[string][]*domain.Subagent
	if previous != nil {
		previousThreads = threadIndex(previous.Threads)
		previousSideChats = sideChatIDs(previous.SideChats)
		previousSubagents = previous.Subagents
	}
	nextSideChats := sideChatIDs(next.SideChats)
	nextThreads := threadIndex(next.Threads)
	var candidates []*domain.Thread
	if threadsChanged {
		candidates = next.Threads
		var removedTasks []string
		if previous != nil {
			seen := map[string]bool{}
			for _, thread := range previous.Threads {
				id := thread.ID
				if seen[id] {
					continue
				}
				seen[id] = true
				if _, held := nextThreads[id]; !previousSideChats[id] && (!held || nextSideChats[id]) {
					removedTasks = append(removedTasks, id)
				}
			}
		}
		if len(removedTasks) > 0 {
			delta.RemovedTasks = removedTasks
		}
	} else {
		for _, id := range sortedKeys(next.Subagents) {
			if jsonx.SameSlice(previous.Subagents[id], next.Subagents[id]) && (previous.Subagents[id] == nil) == (next.Subagents[id] == nil) {
				continue
			}
			if thread := nextThreads[id]; thread != nil {
				candidates = append(candidates, thread)
			}
		}
	}
	for _, thread := range candidates {
		if nextSideChats[thread.ID] {
			continue
		}
		var before *domain.Thread
		if !previousSideChats[thread.ID] {
			before = previousThreads[thread.ID]
		}
		heldBefore := previousSubagents[thread.ID]
		held := next.Subagents[thread.ID]
		if before == thread && jsonx.SameSlice(heldBefore, held) && (heldBefore == nil) == (held == nil) {
			continue
		}
		messages := []*contracts.TaskStoreDeltaTasksMessages{}
		var beforeMessages []*domain.ConversationMessage
		if before != nil {
			beforeMessages = before.Messages
		}
		if before == nil || !jsonx.SameSlice(beforeMessages, thread.Messages) {
			for index := domain.FirstChangedMessage(beforeMessages, thread.Messages); index < len(thread.Messages); index++ {
				message := thread.Messages[index]
				if index < len(beforeMessages) && beforeMessages[index] == message {
					continue
				}
				messages = append(messages, &contracts.TaskStoreDeltaTasksMessages{Index: float64(index), Message: *message})
			}
		}
		subagents, activity := subagentDelta(heldBefore, held)
		if before == thread && len(subagents) == 0 && len(activity) == 0 {
			continue
		}
		taskDelta := &contracts.TaskStoreDeltaTasks{Task: persistedTask(thread), Messages: messages}
		if len(subagents) > 0 {
			taskDelta.Subagents = subagents
		}
		if len(activity) > 0 {
			taskDelta.Activity = activity
		}
		delta.Tasks = append(delta.Tasks, taskDelta)
	}
	return delta
}

// sortedKeys is a record's keys in the order JavaScript's Object.keys gives a record keyed by ids.
func sortedKeys[V any](record map[string]V) []string {
	keys := make([]string, 0, len(record))
	for key := range record {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// PersistedStoreState is the durable baseline: the data read from disk, never a later workspace snapshot.
func PersistedStoreState(stored *domain.ThreadStoreData) *PersistenceState {
	subagents := map[string][]*domain.Subagent{}
	threads := make([]*domain.Thread, len(stored.Tasks))
	for index, task := range stored.Tasks {
		if len(task.Subagents) > 0 {
			subagents[task.ID] = task.Subagents
		}
		threads[index] = domain.RankedThread(task)
	}
	return &PersistenceState{
		Threads:    threads,
		Subagents:  subagents,
		SideChats:  []*application.SideChat{},
		Projects:   stored.Projects,
		Worktrees:  stored.Worktrees,
		LastFolder: stored.LastFolder,
	}
}

func HasPersistenceDelta(delta contracts.TaskStoreDelta) bool {
	return len(delta.Tasks) > 0 || delta.RemovedTasks != nil || delta.Projects != nil || delta.Worktrees != nil || !delta.LastFolder.IsAbsent()
}

// DrainLatestPersistence writes the newest pending snapshot. Every caller waits for the active write
// and every newer snapshot queued before the drain settles.
func DrainLatestPersistence(ctx context.Context, queue *PersistenceQueue, persist func(ctx context.Context, delta contracts.TaskStoreDelta) error) error {
	for {
		persistenceMu.Lock()
		if queue.InFlight == nil && queue.Pending == nil {
			persistenceMu.Unlock()
			return nil
		}
		if queue.InFlight == nil {
			done := make(chan struct{})
			var failure error
			queue.InFlight = func(ctx context.Context) error {
				select {
				case <-done:
					return failure
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			writing := context.WithoutCancel(ctx)
			go func() {
				failure = writeQueued(writing, queue, persist)
				persistenceMu.Lock()
				queue.InFlight = nil
				persistenceMu.Unlock()
				close(done)
			}()
		}
		inFlight := queue.InFlight
		persistenceMu.Unlock()
		if err := inFlight(ctx); err != nil {
			return err
		}
	}
}

// writeQueued writes pending snapshots until none is left, keeping the failed one pending.
func writeQueued(ctx context.Context, queue *PersistenceQueue, persist func(ctx context.Context, delta contracts.TaskStoreDelta) error) error {
	for {
		persistenceMu.Lock()
		next := queue.Pending
		if next == nil {
			persistenceMu.Unlock()
			return nil
		}
		queue.Pending = nil
		activeSnapshots[queue] = next
		delta := PersistenceDelta(queue.Persisted, next)
		persistenceMu.Unlock()
		var err error
		if HasPersistenceDelta(delta) {
			err = persist(ctx, delta)
		}
		persistenceMu.Lock()
		active := activeSnapshots[queue]
		delete(activeSnapshots, queue)
		if err != nil {
			if queue.Pending == nil {
				queue.Pending = active
			}
			persistenceMu.Unlock()
			return err
		}
		queue.Persisted = active
		persistenceMu.Unlock()
	}
}
