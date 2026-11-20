package codex

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
	"github.com/zyuapp/ai-coding-tool-go/internal/workspace"
)

// Where a thread's work belongs, as Codex records it beside the thread. Nil fields are unknown.
type GitOrigin struct {
	OriginURL *string `json:"originUrl"`
	Branch    *string `json:"branch"`
	Sha       *string `json:"sha"`
}

type ReadOrigin func(ctx context.Context, root string) GitOrigin

type metadataUpdate struct {
	ThreadID string    `json:"threadId"`
	GitInfo  GitOrigin `json:"gitInfo"`
}

// Where a thread's work belongs, read from the checkout it runs in.
func ReadGitOrigin(ctx context.Context, root string) GitOrigin {
	git := workspace.NewGit(platform.Command)
	origin := GitOrigin{OriginURL: git.OriginURL(ctx, root), Branch: git.CurrentBranch(ctx, root)}
	if sha, err := git.HeadCommit(ctx, root, ""); err == nil {
		origin.Sha = &sha
	}
	return origin
}

func noOrigin(context.Context, string) GitOrigin { return GitOrigin{} }

// What Codex keeps on disk about a thread, as this app leaves it: the title the thread carries here,
// and the checkout its work belongs to. Codex writes the rollout these are recorded against lazily at
// the first turn, so everything is held until Began says the thread has one.
type CodexThreadRecord struct {
	readOrigin ReadOrigin

	mu       sync.Mutex
	client   CodexClient
	threadID string
	root     string
	recorded bool
	stamped  bool
	// The title waiting to reach Codex, and the last one that did, so a repeat costs no request.
	pending *string
	written *string
}

func NewCodexThreadRecord(readOrigin ReadOrigin) *CodexThreadRecord {
	if readOrigin == nil {
		readOrigin = noOrigin
	}
	return &CodexThreadRecord{readOrigin: readOrigin}
}

// The thread now exists on the server, though Codex may still have nothing on disk for it.
func (record *CodexThreadRecord) Opened(client CodexClient, threadID, root string) {
	record.mu.Lock()
	record.client, record.threadID, record.root = client, threadID, root
	record.mu.Unlock()
}

// Codex has written the rollout, which is what the requests below are recorded against.
func (record *CodexThreadRecord) Began() {
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.recorded {
		return
	}
	record.recorded = true
	record.write()
	record.stamp()
}

// Names the thread in Codex's history. A title that lands before the rollout waits for it.
func (record *CodexThreadRecord) Label(title string) {
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.written != nil && *record.written == title {
		return
	}
	record.pending = &title
	record.write()
}

func (record *CodexThreadRecord) write() {
	client, threadID, pending := record.client, record.threadID, record.pending
	if client == nil || threadID == "" || !record.recorded || pending == nil {
		return
	}
	record.pending = nil
	record.written = pending
	go func() {
		if _, err := client.Request(context.Background(), "thread/name/set", protocol.ThreadSetNameParams{ThreadID: threadID, Name: *pending}); err != nil {
			// A name is a courtesy to the other app; failing to leave one changes nothing here.
			record.mu.Lock()
			if record.written == pending {
				record.written = nil
			}
			record.mu.Unlock()
		}
	}()
}

// Records where the work belongs, so Codex reads a worktree thread as the repository's rather than
// as a stray folder's. Written once: the checkout does not move under a thread.
func (record *CodexThreadRecord) stamp() {
	client, threadID, root := record.client, record.threadID, record.root
	if client == nil || threadID == "" || root == "" || record.stamped {
		return
	}
	record.stamped = true
	go func() {
		origin := record.readOrigin(context.Background(), root)
		if deref(origin.OriginURL) == "" && deref(origin.Branch) == "" && deref(origin.Sha) == "" {
			return
		}
		_, _ = client.Request(context.Background(), "thread/metadata/update", metadataUpdate{ThreadID: threadID, GitInfo: origin})
	}()
}

// Continues a thread from disk. A thread archived in another Codex client is out of the live
// sessions a resume looks in, so an unarchive is offered once before the refusal is passed on.
func resumeThread(ctx context.Context, client CodexClient, params protocol.ThreadResumeParams) (protocol.ThreadSessionResponse, error) {
	params.ExcludeTurns = ptr(true)
	resume := func() (protocol.ThreadSessionResponse, error) {
		return call[protocol.ThreadSessionResponse](ctx, client, "thread/resume", params)
	}
	started, err := resume()
	if err == nil || !isAppServerError(err) {
		return started, err
	}
	if _, unarchiveErr := client.Request(ctx, "thread/unarchive", protocol.ThreadIDParams{ThreadID: params.ThreadID}); unarchiveErr != nil {
		return started, err
	}
	return resume()
}

var fileTimeout = 60 * time.Second

// Files a thread away in Codex's history, or brings it back, on a server of its own. Codex refuses
// while another process still writes the thread, so its session has to be gone first.
func FileThread(ctx context.Context, connect CodexConnect, threadID string, archived bool) error {
	command, err := CodexAppServer(bareServer, AppServerOptions{})
	if err != nil {
		return err
	}
	client := connect(command)
	defer client.Close()
	method := "thread/unarchive"
	if archived {
		method = "thread/archive"
	}
	_, err = withDeadline(ctx, fileTimeout, fmt.Errorf("Codex did not file thread %s away in time.", threadID), func(ctx context.Context) (struct{}, error) {
		if err := initialize(ctx, client); err != nil {
			return struct{}{}, err
		}
		_, err := client.Request(ctx, method, protocol.ThreadIDParams{ThreadID: threadID})
		return struct{}{}, err
	})
	return err
}
