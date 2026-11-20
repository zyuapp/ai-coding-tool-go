package computers

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/store"
	"github.com/zyuapp/ai-coding-tool-go/internal/workspace"
)

func thread(id string) *domain.Thread {
	return &domain.Thread{ID: id, Title: id, Engine: domain.AgentEngineClaude}
}

func withThreads(ids ...string) *application.WorkspaceState {
	state := application.EmptyWorkspaceState(nil)
	next := *state
	next.Threads = nil
	for _, id := range ids {
		next.Threads = append(next.Threads, thread(id))
	}
	return &next
}

// heldElsewhere: the thread "remote" and the checkouts "repo" and "gone" are held by the paired computer "holder".
func heldElsewhere() *application.WorkspaceState {
	state := withThreads("local")
	remote := withThreads("remote")
	held := *remote
	held.Projects = []*domain.Project{{ID: "repo", Root: "/repo", WorkspaceID: new("repo")}, {ID: "gone", Root: "/gone", WorkspaceID: new("gone")}}
	computers := state.Computers
	computers.Paired = []*application.PairedComputer{{
		ComputerLink: domain.ComputerLink{ID: "holder", Name: "Linux", Host: "linux", PairedAt: 1, Status: domain.ComputerStatusConnected},
		State:        &held,
	}}
	state.Computers = computers
	return state
}

type linkFunc func(ctx context.Context, id string, query contracts.ComputerQuery) (jsontext.Value, error)

func (f linkFunc) Query(ctx context.Context, id string, query contracts.ComputerQuery) (jsontext.Value, error) {
	return f(ctx, id, query)
}

func readsOver(link PairedQueries, local ComputerQueryHost) *ComputerReads {
	return NewComputerReads(ComputerReadHost{ComputerQueryHost: local, State: heldElsewhere, Links: func() PairedQueries { return link }})
}

func readsHere(local ComputerQueryHost) *ComputerReads {
	return NewComputerReads(ComputerReadHost{ComputerQueryHost: local, State: func() *application.WorkspaceState { return withThreads() }, Links: func() PairedQueries { return nil }})
}

// wire is an answer as the line carries it.
func wire(t *testing.T, answer any) jsontext.Value {
	t.Helper()
	data, err := jsonx.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func expectError(t *testing.T, err error, pattern string) {
	t.Helper()
	if err == nil || !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Fatalf("expected an error matching %q, got %v", pattern, err)
	}
}

func TestPairedThreadQueriesValidateBoundedInputs(t *testing.T) {
	var queries []contracts.ComputerThreadQuery
	reader := ComputerQueryHost{Threads: func(_ context.Context, query contracts.ComputerThreadQuery) (jsontext.Value, error) {
		queries = append(queries, query)
		return jsontext.Value(`"transcript"`), nil
	}}
	limit := 30.0
	if answer, err := AnswerComputerQuery(background(), contracts.ComputerThreadQueryThreadRead{ThreadID: "thread", Limit: &limit}, &reader); err != nil || string(answer.(jsontext.Value)) != `"transcript"` {
		t.Fatal(answer, err)
	}
	if _, err := AnswerComputerQuery(background(), contracts.ComputerThreadQueryThreadList{Project: new("all"), Search: new("needle"), Limit: new(20.0)}, &reader); err != nil {
		t.Fatal(err)
	}
	for _, query := range []map[string]any{
		{"kind": "thread-read", "threadId": "", "limit": 30.0},
		{"kind": "thread-read", "threadId": "thread", "limit": 201.0},
		{"kind": "thread-list", "search": strings.Repeat("a", 1_001)},
		{"kind": "thread-list", "limit": -1.0},
		{"kind": "thread-list", "limit": 0.5},
		{"kind": "thread-list", "archived": "yes"},
	} {
		if contracts.IsComputerQuery(query) {
			t.Fatalf("accepted %v", query)
		}
	}
	_, err := AnswerComputerQuery(background(), contracts.ComputerThreadQueryThreadRead{ThreadID: "thread", Limit: new(201.0)}, &reader)
	expectError(t, err, "Invalid thread-read query")
	if len(queries) != 2 {
		t.Fatal(queries)
	}
}

func TestAttachmentReadsReturnSavedBytesAndRejectArbitraryPaths(t *testing.T) {
	folder := t.TempDir()
	attachments := store.NewAttachments(folder)
	saved, err := attachments.WriteAttachment("AQID", nil)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(saved)
	here := readsHere(ComputerQueryHost{Attachments: attachments})
	if !contracts.IsComputerQuery(map[string]any{"kind": "attachment", "name": name}) {
		t.Fatal("refused a saved name")
	}
	if answer, err := here.Answer(background(), contracts.ComputerQueryAttachment{Name: name}); err != nil || answer != "AQID" {
		t.Fatal(answer, err)
	}
	if err := os.WriteFile(filepath.Join(folder, "outside.png"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"../outside.png", `..\outside.png`, saved, "shot.png.context.json", "", "%2e%2e%2foutside.png"} {
		if contracts.IsComputerQuery(map[string]any{"kind": "attachment", "name": invalid}) {
			t.Fatalf("accepted %q", invalid)
		}
		_, err := here.Answer(background(), contracts.ComputerQueryAttachment{Name: invalid})
		expectError(t, err, "Invalid attachment query")
	}
	if _, err := here.Answer(background(), contracts.ComputerQueryAttachment{Name: "missing.png"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}

	var asked [][2]any
	reads := readsOver(linkFunc(func(ctx context.Context, id string, query contracts.ComputerQuery) (jsontext.Value, error) {
		asked = append(asked, [2]any{id, query})
		answer, err := here.Answer(ctx, query)
		if err != nil {
			return nil, err
		}
		return wire(t, answer), nil
	}), ComputerQueryHost{Attachments: attachments})
	bytes, err := reads.Attachment(background(), name, new("remote"))
	if err != nil || !slices.Equal(bytes, []byte{1, 2, 3}) {
		t.Fatal(bytes, err)
	}
	if len(asked) != 1 || asked[0][0] != "holder" {
		t.Fatal(asked)
	}
	local, err := reads.Attachment(background(), name, new("local"))
	if err != nil || !slices.Equal(local, []byte{1, 2, 3}) || len(asked) != 1 {
		t.Fatal(local, err, asked)
	}
	offline := readsOver(linkFunc(func(context.Context, string, contracts.ComputerQuery) (jsontext.Value, error) {
		return nil, errors.New("Offline")
	}), ComputerQueryHost{Attachments: attachments})
	if _, err := offline.Attachment(background(), name, new("remote")); err == nil {
		t.Fatal("an offline holder never falls back to a local image of the same name")
	}
}

func TestAttachmentReadsUseTheDecodedByteLimit(t *testing.T) {
	data := strings.Repeat("BwcH", domain.MaxAttachmentBytes/3) + "Bw=="
	reads := readsOver(linkFunc(func(context.Context, string, contracts.ComputerQuery) (jsontext.Value, error) {
		return wire(t, data), nil
	}), ComputerQueryHost{})
	bytes, err := reads.Attachment(background(), "shot.png", new("remote"))
	if err != nil || len(bytes) != domain.MaxAttachmentBytes {
		t.Fatal(len(bytes), err)
	}
	over := readsOver(linkFunc(func(context.Context, string, contracts.ComputerQuery) (jsontext.Value, error) {
		return wire(t, data+"BwcH"), nil
	}), ComputerQueryHost{})
	_, err = over.Attachment(background(), "shot.png", new("remote"))
	expectError(t, err, "Invalid attachment response")
}

func TestDirectoryReadsShareLocalAndRemoteResults(t *testing.T) {
	folder := t.TempDir()
	for _, name := range []string{"work", "world", ".hidden"} {
		if err := os.Mkdir(filepath.Join(folder, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(folder, "word.txt"), []byte("file"), 0o600)
	_ = os.Symlink(filepath.Join(folder, "work"), filepath.Join(folder, "work-link"))
	_ = os.Symlink(filepath.Join(folder, "missing"), filepath.Join(folder, "wrong-link"))
	here := readsHere(ComputerQueryHost{})
	directories := func(prefix string, computer *string) []string {
		t.Helper()
		found, err := here.Directories(background(), prefix, computer)
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	prefix := folder + "/wo"
	expected := []string{folder + "/work-link/", folder + "/work/", folder + "/world/"}
	if answer, err := here.Answer(background(), contracts.ComputerQueryDirectories{Prefix: prefix}); err != nil || !reflect.DeepEqual(answer, expected) {
		t.Fatal(answer, err)
	}
	if found := directories(prefix, new("this")); !reflect.DeepEqual(found, expected) {
		t.Fatal(found)
	}
	var asked []string
	reads := readsOver(linkFunc(func(ctx context.Context, id string, query contracts.ComputerQuery) (jsontext.Value, error) {
		asked = append(asked, id)
		answer, err := here.Answer(ctx, query)
		return wire(t, answer), err
	}), ComputerQueryHost{})
	if found, err := reads.Directories(background(), prefix, new("linux")); err != nil || !reflect.DeepEqual(found, expected) || !reflect.DeepEqual(asked, []string{"linux"}) {
		t.Fatal(found, err, asked)
	}
	if found := directories(folder+"/.", nil); !reflect.DeepEqual(found, []string{folder + "/.hidden/"}) {
		t.Fatal(found)
	}
	if slices.Contains(directories(folder+"/", nil), folder+"/.hidden/") {
		t.Fatal("an unrequested hidden folder")
	}
	for _, empty := range []string{folder + "/missing/", folder + "/word.txt/", "relative/path"} {
		if found := directories(empty, nil); len(found) != 0 || found == nil {
			t.Fatalf("%s: %v", empty, found)
		}
	}
	home, _ := os.UserHomeDir()
	if !reflect.DeepEqual(directories("~/", nil), directories(home+"/", nil)) || !reflect.DeepEqual(directories("~", nil), directories(home+"/", nil)) {
		t.Fatal("home is not expanded")
	}
}

func TestDirectoryScansAndUntrustedAnswersAreBounded(t *testing.T) {
	folder := t.TempDir()
	for i := range 30 {
		_ = os.Mkdir(filepath.Join(folder, "dir-"+itoa(i)), 0o755)
	}
	here := readsHere(ComputerQueryHost{})
	if found, _ := here.Directories(background(), folder+"/", nil); len(found) != 20 {
		t.Fatal(len(found))
	}
	for _, prefix := range []any{42.0, strings.Repeat("x", 4097), "bad\x00path"} {
		if contracts.IsComputerQuery(map[string]any{"kind": "directories", "prefix": prefix}) {
			t.Fatalf("accepted %v", prefix)
		}
	}
	_, err := here.Directories(background(), "bad\x00path", nil)
	expectError(t, err, "Invalid directories query")
	_, err = here.Directories(background(), "/", new("linux"))
	expectError(t, err, "unavailable")
	over := func(answer jsontext.Value, failure error) error {
		_, err := readsOver(linkFunc(func(context.Context, string, contracts.ComputerQuery) (jsontext.Value, error) {
			return answer, failure
		}), ComputerQueryHost{}).Directories(background(), "/", new("linux"))
		return err
	}
	expectError(t, over(nil, errors.New("Connection refused")), "Connection refused")
	expectError(t, over(wire(t, slices.Repeat([]string{"/"}, 21)), nil), "Invalid directory response")
	expectError(t, over(jsontext.Value(`[42]`), nil), "Invalid directory response")
}

// gitServices answers checkout reads from the real git and workspace services.
type gitServices struct {
	git        *workspace.Git
	workspaces *workspace.WorkspaceService
}

func convert[T any](t *testing.T, value any) T {
	t.Helper()
	var result T
	if err := jsonx.Unmarshal(wire(t, value), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

type resolverFunc func(id string) (workspace.WorkspaceResolution, error)

func (f resolverFunc) Resolve(id string) (workspace.WorkspaceResolution, error) { return f(id) }

// Resolve answers the registered checkout, and any other id as a checkout that is gone.
func (g gitServices) Resolve(_ context.Context, id string) (domain.WorkspaceResolution, error) {
	resolution, err := g.workspaces.Resolve(id)
	if err != nil {
		return domain.WorkspaceResolutionUnavailable{Workspace: domain.WorkspaceRecord{ID: id, Kind: domain.WorkspaceKindProject, Root: "/gone"}, Reason: domain.WorkspaceResolutionUnavailableReasonMissing}, nil
	}
	var result domain.WorkspaceResolution
	data, _ := jsonx.Marshal(resolution)
	return result, jsonx.Unmarshal(data, &result)
}

func (g gitServices) DiffPatch(ctx context.Context, id string, diffRange domain.DiffRange, path string, previousPath *string, ignoreWhitespace bool) (contracts.DiffPatchResult, error) {
	var local workspace.DiffRange
	data, _ := jsonx.Marshal(diffRange)
	if err := jsonx.Unmarshal(data, &local); err != nil {
		return nil, err
	}
	result := g.git.DiffPatch(ctx, id, local, path, g.workspaces, previousPath, ignoreWhitespace)
	var typed contracts.DiffPatchResult
	data, _ = jsonx.Marshal(result)
	return typed, jsonx.Unmarshal(data, &typed)
}

func (g gitServices) ListBranches(ctx context.Context, root string) (*contracts.BranchesResultAvailable, error) {
	branches, err := g.git.ListBranches(ctx, root)
	if err != nil {
		return nil, err
	}
	return &contracts.BranchesResultAvailable{Branches: branches.Branches, Remotes: branches.Remotes, Current: branches.Current}, nil
}

func (g gitServices) KnownCommits(ctx context.Context, root string, hashes []string) ([]string, error) {
	return g.git.KnownCommits(ctx, root, hashes)
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return strings.TrimSpace(string(output))
}

type row struct {
	query   contracts.ComputerQuery
	owner   ComputerReadOwner
	settles bool
	fresh   func()
}

// swappableImages lets a row start from an empty store.
type swappableImages struct{ current *store.MessageImageStore }

func (s *swappableImages) PreserveMessageImage(file, root, messageID string, load func() ([]byte, error)) (string, error) {
	return s.current.PreserveMessageImage(file, root, messageID, load)
}

func (s *swappableImages) ReadMessageImage(file, root, messageID string, thumbnail bool) (store.MessageImage, error) {
	return s.current.ReadMessageImage(file, root, messageID, thumbnail)
}

func TestEveryReadAnswersInOneShapeHereOverALinkAndWhenItDrops(t *testing.T) {
	folder := t.TempDir()
	checkout := filepath.Join(folder, "checkout")
	_ = os.Mkdir(checkout, 0o755)
	run(t, checkout, "git", "init", "-b", "main")
	run(t, checkout, "git", "config", "user.email", "tests@example.com")
	run(t, checkout, "git", "config", "user.name", "AI Coding Tool Tests")
	run(t, checkout, "git", "config", "commit.gpgsign", "false")
	_ = os.WriteFile(filepath.Join(checkout, "tracked.txt"), []byte("one\n"), 0o644)
	run(t, checkout, "git", "add", "tracked.txt")
	run(t, checkout, "git", "commit", "-m", "initial")
	run(t, checkout, "git", "branch", "feature")
	_ = os.WriteFile(filepath.Join(checkout, "tracked.txt"), []byte("one\ntwo\n"), 0o644)
	_ = os.Mkdir(filepath.Join(folder, "docs"), 0o755)

	services := workspace.NewWorkspaceService(workspace.WorkspaceServiceOptions{RegistryPath: filepath.Join(folder, "workspaces.v1.json"), ProjectlessRoot: filepath.Join(folder, "projectless")})
	registered, err := services.RegisterProject(checkout)
	if err != nil {
		t.Fatal(err)
	}
	repo := registered.Workspace.ID
	attachments := store.NewAttachments(folder)
	saved, _ := attachments.WriteAttachment("AQID", nil)
	attachment := filepath.Base(saved)
	stores := 0
	images := &swappableImages{}
	freshStore := func() {
		images.current = store.NewMessageImageStore(filepath.Join(folder, "images-"+itoa(stores)), func([]byte) ([]byte, error) { return []byte("preview"), nil }, workspace.OpenableFile)
		stores++
	}
	freshStore()
	_ = os.WriteFile(filepath.Join(folder, "shot.jpg"), []byte{255, 216, 255, 1}, 0o644)
	git := gitServices{git: workspace.NewGit(nil), workspaces: services}
	local := ComputerQueryHost{
		Threads: func(_ context.Context, query contracts.ComputerThreadQuery) (jsontext.Value, error) {
			return wire(t, map[string]any{"answered": query}), nil
		},
		Workspaces:    git,
		Attachments:   attachments,
		MessageImages: images,
		Git:           git,
		Commands: func(context.Context, domain.AgentEngine, string, bool) ([]*contracts.AvailableCommand, error) {
			return nil, errors.New("no engines in this test")
		},
	}
	// The project "repo" is held by "holder" in heldElsewhere; the checkout here answers to its own id.
	holder := func(state *application.WorkspaceState) *application.WorkspaceState {
		paired := *state.Computers.Paired[0].State
		paired.Projects = []*domain.Project{{ID: "repo", Root: checkout, WorkspaceID: &repo}, {ID: "gone", Root: "/gone", WorkspaceID: new("gone")}}
		computers := state.Computers
		computers.Paired = []*application.PairedComputer{{ComputerLink: state.Computers.Paired[0].ComputerLink, State: &paired}}
		next := *state
		next.Computers = computers
		return &next
	}
	over := func(link PairedQueries) *ComputerReads {
		return NewComputerReads(ComputerReadHost{ComputerQueryHost: local, State: func() *application.WorkspaceState { return holder(heldElsewhere()) }, Links: func() PairedQueries { return link }})
	}
	here := readsHere(local)
	image := contracts.ComputerQueryMessageImage{Path: "shot.jpg", Root: folder, Message: "reply"}
	head := run(t, checkout, "git", "rev-parse", "--short=9", "HEAD")
	blob := run(t, checkout, "git", "rev-parse", "--short=9", "HEAD:tracked.txt")
	commits := contracts.ComputerQueryCommits{WorkspaceID: repo, Hashes: []string{head, "35995b7", blob}}
	if known, err := here.Commits(background(), repo, commits.Hashes); err != nil || !reflect.DeepEqual(known, []string{head}) {
		t.Fatal("only a commit this repository holds is known", known, err)
	}
	if known, err := here.Commits(background(), "gone", []string{head}); err != nil || len(known) != 0 {
		t.Fatal("a missing checkout holds no commits", known, err)
	}
	rows := []row{
		{query: contracts.ComputerThreadQueryThreadRead{ThreadID: "remote", Limit: new(5.0)}, owner: OwnerThread(new("remote"))},
		{query: contracts.ComputerThreadQueryThreadList{Search: new("needle")}, owner: OwnerComputer(new("holder"))},
		{query: contracts.ComputerQueryTerminalOutput{TerminalID: "no-such-terminal"}, owner: OwnerComputer(new("holder"))},
		{query: contracts.ComputerQueryDirectories{Prefix: folder + "/do"}, owner: OwnerComputer(new("holder"))},
		{query: contracts.ComputerQueryAttachment{Name: attachment}, owner: OwnerThread(new("remote"))},
		{query: image, owner: OwnerThread(new("remote")), fresh: freshStore},
		{query: contracts.ComputerQueryMessageImage{Path: "shot.jpg", Root: folder, Message: "reply", Thumbnail: new(true)}, owner: OwnerThread(new("remote")), fresh: freshStore},
		{query: contracts.ComputerQueryBranches{WorkspaceID: repo}, owner: OwnerWorkspace(repo), settles: true},
		{query: contracts.ComputerQueryBranches{WorkspaceID: "gone"}, owner: OwnerWorkspace("gone"), settles: true},
		{query: contracts.ComputerQueryDiffPatch{WorkspaceID: repo, Range: domain.DiffRangeUncommitted{}, Path: "tracked.txt"}, owner: OwnerWorkspace(repo), settles: true},
		{query: contracts.ComputerQueryCommands{WorkspaceID: "gone", Engine: domain.AgentEngineClaude}, owner: OwnerWorkspace("gone"), settles: true},
		{query: commits, owner: OwnerWorkspace(repo)},
	}
	for _, row := range rows {
		label := string(wire(t, row.query))
		sent := map[string]jsontext.Value{}
		queries := []contracts.ComputerQuery{row.query}
		if _, ok := row.query.(contracts.ComputerQueryMessageImage); ok {
			queries = append(queries, image)
		}
		for _, query := range queries {
			answer, err := here.Answer(background(), query)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			sent[string(wire(t, query))] = wire(t, answer)
		}
		owner := row.owner
		if owner.kind == ownerComputer {
			owner = OwnerComputer(new("this"))
		}
		answered, err := here.Read(background(), row.query, owner)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if row.settles {
			status := "available"
			if strings.Contains(label, "gone") {
				status = "error"
			}
			if got := asJSON(answered).(map[string]any)["status"]; got != status {
				t.Fatalf("%s: status %v", label, got)
			}
		}

		if row.fresh != nil {
			row.fresh()
		}
		var asked []string
		linked := over(linkFunc(func(_ context.Context, id string, query contracts.ComputerQuery) (jsontext.Value, error) {
			asked = append(asked, id)
			return sent[string(wire(t, query))], nil
		}))
		got, err := linked.Read(background(), row.query, row.owner)
		if err != nil || !reflect.DeepEqual(got, answered) {
			t.Fatalf("%s: linked %#v %v, here %#v", label, got, err, answered)
		}
		if !reflect.DeepEqual(asked, []string{"holder"}) {
			t.Fatalf("%s: asked %v", label, asked)
		}

		if row.fresh != nil {
			row.fresh()
		}
		dropped := over(linkFunc(func(context.Context, string, contracts.ComputerQuery) (jsontext.Value, error) {
			return nil, errors.New("The line dropped.")
		}))
		got, err = dropped.Read(background(), row.query, row.owner)
		if row.settles {
			if err != nil || !reflect.DeepEqual(got, contracts.BranchesResultError{Message: "The line dropped."}) {
				t.Fatalf("%s: dropped %#v %v", label, got, err)
			}
		} else {
			expectError(t, err, "The line dropped")
		}

		switch row.query.(type) {
		case contracts.ComputerThreadQueryThreadRead, contracts.ComputerThreadQueryThreadList:
			continue
		}
		if row.fresh != nil {
			row.fresh()
		}
		malformed := over(linkFunc(func(context.Context, string, contracts.ComputerQuery) (jsontext.Value, error) {
			return jsontext.Value(`{"status":"available","unexpected":true}`), nil
		}))
		got, err = malformed.Read(background(), row.query, row.owner)
		if row.settles {
			failure, _ := got.(contracts.BranchesResultError)
			if err != nil || !regexp.MustCompile(`Invalid \w+ response`).MatchString(failure.Message) {
				t.Fatalf("%s: malformed %#v %v", label, got, err)
			}
		} else {
			expectError(t, err, "Invalid")
		}
	}
	unlinked := NewComputerReads(ComputerReadHost{ComputerQueryHost: local, State: func() *application.WorkspaceState { return holder(heldElsewhere()) }, Links: func() PairedQueries { return nil }})
	if got, _ := unlinked.Branches(background(), repo); !reflect.DeepEqual(got, contracts.BranchesResultError{Message: "That computer is unavailable."}) {
		t.Fatal(got)
	}
	if got, _ := unlinked.Branches(background(), ""); !reflect.DeepEqual(got, contracts.BranchesResultError{Message: "Invalid branches query."}) {
		t.Fatal(got)
	}
}

func TestMessageImageQueriesPreserveOriginalsAndAnswerBoundedReads(t *testing.T) {
	folder := t.TempDir()
	images := &swappableImages{current: store.NewMessageImageStore(filepath.Join(folder, "store"), func([]byte) ([]byte, error) { return []byte("preview"), nil }, workspace.OpenableFile)}
	answer := readsHere(ComputerQueryHost{MessageImages: images}).Answer
	file := filepath.Join(folder, "shot.jpg")
	bytes := []byte{255, 216, 255, 1}
	_ = os.WriteFile(file, bytes, 0o644)
	query := contracts.ComputerQueryMessageImage{Path: "shot.jpg", Root: folder, Message: "reply"}
	original := messageImageWire{Data: "/9j/AQ==", ContentType: "image/jpeg"}
	if got, err := answer(background(), query); err != nil || got != original {
		t.Fatal(got, err)
	}
	_ = os.Remove(file)
	if got, err := answer(background(), query); err != nil || got != original {
		t.Fatal("the original is kept", got, err)
	}
	thumbnail := query
	thumbnail.Thumbnail = new(true)
	if got, err := answer(background(), thumbnail); err != nil || got != (messageImageWire{Data: "cHJldmlldw==", ContentType: "image/png"}) {
		t.Fatal(got, err)
	}
	later := query
	later.Message = "later"
	if _, err := answer(background(), later); err == nil {
		t.Fatal("a reply that never linked the image reads it")
	}
	invalid := query
	invalid.Path = "/etc/passwd"
	if _, err := answer(background(), invalid); err == nil {
		t.Fatal("an arbitrary path was read")
	}

	images.current = store.NewMessageImageStore(filepath.Join(folder, "headless"), nil, workspace.OpenableFile)
	_ = os.WriteFile(file, bytes, 0o644)
	if got, err := answer(background(), thumbnail); err != nil || got != original {
		t.Fatal("a headless host answers a preview with the original", got, err)
	}
}
