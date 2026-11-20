package computers

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"unicode/utf16"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/store"
)

// WorkspaceResolver finds where a workspace id lives on this computer.
type WorkspaceResolver interface {
	Resolve(ctx context.Context, workspaceID string) (domain.WorkspaceResolution, error)
}

// TerminalReader reads a shell's output for a viewer on another computer. Nil is a terminal that is gone.
type TerminalReader interface {
	ReadTerminalOutput(ctx context.Context, terminalID string, after *float64) (*contracts.TerminalOutputRead, error)
}

// AttachmentReader reads a saved attachment by name, as base64. *store.Attachments is one.
type AttachmentReader interface {
	ReadSavedAttachment(name string) (string, error)
}

// MessageImages keeps the images replies link to. *store.MessageImageStore is one.
type MessageImages interface {
	PreserveMessageImage(file, root, messageID string, load func() ([]byte, error)) (string, error)
	ReadMessageImage(file, root, messageID string, thumbnail bool) (store.MessageImage, error)
}

// GitReads are the reads of a checkout another computer may ask for.
type GitReads interface {
	DiffPatch(ctx context.Context, workspaceID string, diffRange domain.DiffRange, path string, previousPath *string, ignoreWhitespace bool) (contracts.DiffPatchResult, error)
	ListBranches(ctx context.Context, root string) (*contracts.BranchesResultAvailable, error)
	KnownCommits(ctx context.Context, root string, hashes []string) ([]string, error)
}

// CommandLister lists what the "/" menu offers a thread on an engine in a checkout.
type CommandLister func(ctx context.Context, engine domain.AgentEngine, root string, projectless bool) ([]*contracts.AvailableCommand, error)

// ComputerQueryHost is what a read is answered from on this computer: the threads here, and the
// checkouts here.
type ComputerQueryHost struct {
	Threads       func(ctx context.Context, query contracts.ComputerThreadQuery) (jsontext.Value, error)
	Workspaces    WorkspaceResolver
	Terminals     TerminalReader
	Attachments   AttachmentReader
	MessageImages MessageImages
	Git           GitReads
	Commands      CommandLister
}

// PairedQueries is the line to the paired computers a read is passed on to.
type PairedQueries interface {
	Query(ctx context.Context, id string, query contracts.ComputerQuery) (jsontext.Value, error)
}

var _ PairedQueries = (*ComputerLinks)(nil)

// ComputerReadHost is where a read is answered: this computer, or the paired one holding what it names.
type ComputerReadHost struct {
	ComputerQueryHost
	State func() *application.WorkspaceState
	// The lines to the paired computers, or nil while there are none.
	Links func() PairedQueries
}

type ownerKind int

const (
	ownerThread ownerKind = iota
	ownerWorkspace
	ownerComputer
)

// ComputerReadOwner is what a read names: a thread or checkout, wherever it is held, or a computer
// by id ("this" or none for this one).
type ComputerReadOwner struct {
	kind ownerKind
	id   *string
}

func OwnerThread(threadID *string) ComputerReadOwner {
	return ComputerReadOwner{kind: ownerThread, id: threadID}
}

func OwnerWorkspace(workspaceID string) ComputerReadOwner {
	return ComputerReadOwner{kind: ownerWorkspace, id: &workspaceID}
}

func OwnerComputer(computerID *string) ComputerReadOwner {
	return ComputerReadOwner{kind: ownerComputer, id: computerID}
}

// MessageImageAnswer is an image read from whichever computer holds it.
type MessageImageAnswer struct {
	Bytes       []byte
	ContentType string
}

// read is one kind of read: how it is answered here, how it crosses the line, and how a failure reads.
// Answers are typed per kind:
//
//	thread-read, thread-list  jsontext.Value
//	terminal-output           *contracts.TerminalOutputRead (nil when the terminal is gone)
//	directories, commits      []string
//	attachment                []byte
//	message-image             MessageImageAnswer
//	diff-patch                contracts.DiffPatchResult
//	branches                  contracts.BranchesResult
//	commands                  contracts.CommandDiscoveryResult
type read struct {
	here func(ctx context.Context, query contracts.ComputerQuery, host *ComputerQueryHost) (any, error)
	// What the line carries, when that is not the answer itself.
	sent func(answer any) any
	// The holder's reply as this computer uses it; anything malformed is refused.
	received func(value jsontext.Value, query contracts.ComputerQuery) (any, error)
	// Asks the holder some other way than passing the query on.
	forward func(ctx context.Context, query contracts.ComputerQuery, host *ComputerQueryHost, ask func(contracts.ComputerQuery) (jsontext.Value, error)) (any, error)
	// What a failed read answers with, for reads whose result carries its own failure.
	failed func(message string) any
}

const (
	maxDirectories = 20
	maxPathLength  = 4_096
)

func refuse(what string) error { return errors.New("Invalid " + what + " response.") }

func jsLength(text string) int { return len(utf16.Encode([]rune(text))) }

func isStrings(value any, maxLength int) bool {
	list, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		text, ok := item.(string)
		if !ok || jsLength(text) > maxLength {
			return false
		}
	}
	return true
}

func isFailure(value any) bool {
	result, _ := value.(map[string]any)
	_, message := result["message"].(string)
	return result["status"] == "error" && message
}

func isCommand(value any) bool {
	command, _ := value.(map[string]any)
	_, name := command["name"].(string)
	_, description := command["description"].(string)
	_, hint := command["argumentHint"].(string)
	aliases, present := command["aliases"]
	return name && description && hint && (!present || isStrings(aliases, maxPathLength))
}

func decoded(value jsontext.Value) any {
	var parsed any
	if jsonx.Unmarshal(value, &parsed) != nil {
		return nil
	}
	return parsed
}

// asJSON reads what an answer encodes to, as JSON.parse would.
func asJSON(value any) any {
	encoded, err := jsonx.Marshal(value)
	if err != nil {
		return nil
	}
	return decoded(encoded)
}

func typed[T any](value jsontext.Value) (T, error) {
	var result T
	err := jsonx.Unmarshal(value, &result)
	return result, err
}

var errNoCheckouts = errors.New("This computer keeps no checkouts.")

func resolved(ctx context.Context, host *ComputerQueryHost, workspaceID string) (*domain.WorkspaceRecord, error) {
	if host.Workspaces == nil {
		return nil, errNoCheckouts
	}
	resolution, err := host.Workspaces.Resolve(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	switch resolution := resolution.(type) {
	case domain.WorkspaceResolutionAvailable:
		return &resolution.Workspace, nil
	case domain.WorkspaceResolutionUnavailable:
		return nil, errors.New("Workspace is unavailable (" + string(resolution.Reason) + ").")
	}
	return nil, errors.New("Workspace is unavailable.")
}

func failedRead(message string) any {
	return contracts.BranchesResultError{Message: message}
}

func threadRead() read {
	return read{
		here: func(ctx context.Context, query contracts.ComputerQuery, host *ComputerQueryHost) (any, error) {
			if host.Threads == nil {
				return nil, errors.New("This computer holds no threads to read.")
			}
			return host.Threads(ctx, query.(contracts.ComputerThreadQuery))
		},
		received: func(value jsontext.Value, _ contracts.ComputerQuery) (any, error) { return value, nil },
	}
}

var base64Pattern = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

// decodeBase64 reads base64 the way Buffer.from does, without requiring whole quanta.
func decodeBase64(text string) []byte {
	trimmed := strings.TrimRight(text, "=")
	if len(trimmed)%4 == 1 {
		trimmed = trimmed[:len(trimmed)-1]
	}
	bytes, err := base64.RawStdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil
	}
	return bytes
}

// reads holds every read a computer answers.
var reads = map[string]read{
	"thread-read": threadRead(),
	"thread-list": threadRead(),
	"terminal-output": {
		here: func(ctx context.Context, query contracts.ComputerQuery, host *ComputerQueryHost) (any, error) {
			q := query.(contracts.ComputerQueryTerminalOutput)
			if host.Terminals == nil {
				return (*contracts.TerminalOutputRead)(nil), nil
			}
			return host.Terminals.ReadTerminalOutput(ctx, q.TerminalID, q.After)
		},
		received: func(value jsontext.Value, _ contracts.ComputerQuery) (any, error) {
			if !contracts.IsTerminalOutputRead(decoded(value)) {
				return nil, refuse("terminal output")
			}
			if decoded(value) == nil {
				return (*contracts.TerminalOutputRead)(nil), nil
			}
			read, err := typed[contracts.TerminalOutputRead](value)
			if err != nil {
				return nil, refuse("terminal output")
			}
			return &read, nil
		},
	},
	"directories": {
		here: func(_ context.Context, query contracts.ComputerQuery, _ *ComputerQueryHost) (any, error) {
			return ListDirectories(query.(contracts.ComputerQueryDirectories).Prefix)
		},
		received: func(value jsontext.Value, _ contracts.ComputerQuery) (any, error) {
			parsed := decoded(value)
			list, _ := parsed.([]any)
			if !isStrings(parsed, maxPathLength) || len(list) > maxDirectories || slices.ContainsFunc(list, func(item any) bool { return strings.Contains(item.(string), "\x00") }) {
				return nil, refuse("directory")
			}
			return typed[[]string](value)
		},
	},
	"attachment": {
		here: func(_ context.Context, query contracts.ComputerQuery, host *ComputerQueryHost) (any, error) {
			if host.Attachments == nil {
				return nil, errors.New("This computer keeps no attachments.")
			}
			data, err := host.Attachments.ReadSavedAttachment(query.(contracts.ComputerQueryAttachment).Name)
			if err != nil {
				return nil, err
			}
			return decodeBase64(data), nil
		},
		sent: func(answer any) any { return base64.StdEncoding.EncodeToString(answer.([]byte)) },
		received: func(value jsontext.Value, _ contracts.ComputerQuery) (any, error) {
			text, ok := decoded(value).(string)
			if !ok || text == "" || len(text) > domain.MaxAttachmentEncodedBytes || !base64Pattern.MatchString(text) {
				return nil, refuse("attachment")
			}
			bytes := decodeBase64(text)
			if len(bytes) == 0 || len(bytes) > domain.MaxAttachmentBytes {
				return nil, refuse("attachment")
			}
			return bytes, nil
		},
	},
	"message-image": {
		here: func(_ context.Context, query contracts.ComputerQuery, host *ComputerQueryHost) (any, error) {
			q := query.(contracts.ComputerQueryMessageImage)
			if host.MessageImages == nil {
				return nil, errors.New("This computer keeps no message images.")
			}
			image, err := host.MessageImages.ReadMessageImage(q.Path, q.Root, q.Message, q.Thumbnail != nil && *q.Thumbnail)
			if err != nil {
				return nil, err
			}
			return MessageImageAnswer{Bytes: image.Bytes, ContentType: image.ContentType}, nil
		},
		sent: func(answer any) any {
			image := answer.(MessageImageAnswer)
			return messageImageWire{Data: base64.StdEncoding.EncodeToString(image.Bytes), ContentType: image.ContentType}
		},
		received: func(value jsontext.Value, query contracts.ComputerQuery) (any, error) {
			bytes, err := store.MessageImageBytes(value, query.(contracts.ComputerQueryMessageImage).Path)
			if err != nil {
				return nil, err
			}
			wire, _ := typed[messageImageWire](value)
			return MessageImageAnswer{Bytes: bytes, ContentType: wire.ContentType}, nil
		},
		// The holder's original is kept here, so its previews are cut here and it still opens once the
		// holder is gone.
		forward: func(_ context.Context, query contracts.ComputerQuery, host *ComputerQueryHost, ask func(contracts.ComputerQuery) (jsontext.Value, error)) (any, error) {
			q := query.(contracts.ComputerQueryMessageImage)
			if host.MessageImages == nil {
				return nil, errors.New("This computer keeps no message images.")
			}
			original := contracts.ComputerQueryMessageImage{Path: q.Path, Root: q.Root, Message: q.Message}
			_, err := host.MessageImages.PreserveMessageImage(q.Path, q.Root, q.Message, func() ([]byte, error) {
				answer, err := ask(original)
				if err != nil {
					return nil, err
				}
				return store.MessageImageBytes(answer, q.Path)
			})
			if err != nil {
				return nil, err
			}
			image, err := host.MessageImages.ReadMessageImage(q.Path, q.Root, q.Message, q.Thumbnail != nil && *q.Thumbnail)
			if err != nil {
				return nil, err
			}
			return MessageImageAnswer{Bytes: image.Bytes, ContentType: image.ContentType}, nil
		},
	},
	"diff-patch": {
		here: func(ctx context.Context, query contracts.ComputerQuery, host *ComputerQueryHost) (any, error) {
			q := query.(contracts.ComputerQueryDiffPatch)
			if host.Git == nil {
				return nil, errNoCheckouts
			}
			return host.Git.DiffPatch(ctx, q.WorkspaceID, q.Range, q.Path, q.PreviousPath, q.IgnoreWhitespace != nil && *q.IgnoreWhitespace)
		},
		received: func(value jsontext.Value, _ contracts.ComputerQuery) (any, error) {
			result, _ := decoded(value).(map[string]any)
			_, patch := result["patch"].(string)
			_, limit := result["limit"].(float64)
			switch {
			case result["status"] == "available" && patch:
				return typed[contracts.DiffPatchResultAvailable](value)
			case result["status"] == "too-large" && limit:
				return typed[contracts.DiffPatchResultTooLarge](value)
			case isFailure(result):
				return typed[contracts.BranchesResultError](value)
			}
			return nil, refuse("patch")
		},
		failed: failedRead,
	},
	"branches": {
		here: func(ctx context.Context, query contracts.ComputerQuery, host *ComputerQueryHost) (any, error) {
			workspace, err := resolved(ctx, host, query.(contracts.ComputerQueryBranches).WorkspaceID)
			if err != nil {
				return nil, err
			}
			if host.Git == nil {
				return nil, errNoCheckouts
			}
			branches, err := host.Git.ListBranches(ctx, workspace.Root)
			if err != nil {
				return nil, err
			}
			return *branches, nil
		},
		received: func(value jsontext.Value, _ contracts.ComputerQuery) (any, error) {
			result, _ := decoded(value).(map[string]any)
			current, hasCurrent := result["current"]
			_, currentText := current.(string)
			if result["status"] == "available" && isStrings(result["branches"], maxPathLength) && isStrings(result["remotes"], maxPathLength) && hasCurrent && (current == nil || currentText) {
				return typed[contracts.BranchesResultAvailable](value)
			}
			if isFailure(result) {
				return typed[contracts.BranchesResultError](value)
			}
			return nil, refuse("branch")
		},
		failed: failedRead,
	},
	"commits": {
		here: func(ctx context.Context, query contracts.ComputerQuery, host *ComputerQueryHost) (any, error) {
			q := query.(contracts.ComputerQueryCommits)
			if host.Workspaces == nil || host.Git == nil {
				return nil, errNoCheckouts
			}
			resolution, err := host.Workspaces.Resolve(ctx, q.WorkspaceID)
			if err != nil {
				return nil, err
			}
			available, ok := resolution.(domain.WorkspaceResolutionAvailable)
			// A checkout that is gone holds no commits.
			if !ok {
				return []string{}, nil
			}
			return host.Git.KnownCommits(ctx, available.Workspace.Root, q.Hashes)
		},
		received: func(value jsontext.Value, query contracts.ComputerQuery) (any, error) {
			hashes := query.(contracts.ComputerQueryCommits).Hashes
			parsed := decoded(value)
			list, _ := parsed.([]any)
			if !isStrings(parsed, maxPathLength) || slices.ContainsFunc(list, func(hash any) bool { return !slices.Contains(hashes, hash.(string)) }) {
				return nil, refuse("commit")
			}
			return typed[[]string](value)
		},
	},
	"commands": {
		here: func(ctx context.Context, query contracts.ComputerQuery, host *ComputerQueryHost) (any, error) {
			q := query.(contracts.ComputerQueryCommands)
			workspace, err := resolved(ctx, host, q.WorkspaceID)
			if err != nil {
				return nil, err
			}
			if host.Commands == nil {
				return nil, errors.New("This computer has no engines.")
			}
			commands, err := host.Commands(ctx, q.Engine, workspace.Root, workspace.Kind == domain.WorkspaceKindProjectless)
			if err != nil {
				return nil, err
			}
			if commands == nil {
				commands = []*contracts.AvailableCommand{}
			}
			return contracts.CommandDiscoveryResultAvailable{Commands: commands}, nil
		},
		received: func(value jsontext.Value, _ contracts.ComputerQuery) (any, error) {
			result, _ := decoded(value).(map[string]any)
			commands, isList := result["commands"].([]any)
			if result["status"] == "available" && isList && !slices.ContainsFunc(commands, func(command any) bool { return !isCommand(command) }) {
				return typed[contracts.CommandDiscoveryResultAvailable](value)
			}
			if isFailure(result) {
				return typed[contracts.BranchesResultError](value)
			}
			return nil, refuse("command")
		},
		failed: failedRead,
	},
}

type messageImageWire struct {
	Data        string `json:"data"`
	ContentType string `json:"contentType"`
}

func queryKind(query contracts.ComputerQuery) string {
	value, _ := asJSON(query).(map[string]any)
	kind, _ := value["kind"].(string)
	return kind
}

func readOf(kind string) (read, error) {
	held, ok := reads[kind]
	if !ok {
		if kind == "" {
			kind = "undefined"
		}
		return read{}, errors.New("Invalid " + kind + " query.")
	}
	return held, nil
}

func settle(kind string, work func() (any, error)) (any, error) {
	answer, err := work()
	if err == nil {
		return answer, nil
	}
	if held, ok := reads[kind]; ok && held.failed != nil {
		return held.failed(contracts.ErrorMessage(err)), nil
	}
	return nil, err
}

func checked(query contracts.ComputerQuery, kind string) error {
	if query == nil || !contracts.IsComputerQuery(asJSON(query)) {
		if kind == "" {
			kind = "undefined"
		}
		return errors.New("Invalid " + kind + " query.")
	}
	return nil
}

// AnswerComputerQuery is this computer's own answer to a read, in the form it is used here.
func AnswerComputerQuery(ctx context.Context, query contracts.ComputerQuery, host *ComputerQueryHost) (any, error) {
	kind := queryKind(query)
	return settle(kind, func() (any, error) {
		held, err := readOf(kind)
		if err != nil {
			return nil, err
		}
		if err := checked(query, kind); err != nil {
			return nil, err
		}
		return held.here(ctx, query, host)
	})
}

// ComputerReads answers reads from whichever computer holds what they name, in one shape whichever
// answers and however it fails.
type ComputerReads struct {
	host ComputerReadHost
}

func NewComputerReads(host ComputerReadHost) *ComputerReads { return &ComputerReads{host: host} }

func (r *ComputerReads) holder(owner ComputerReadOwner) (string, bool, error) {
	switch owner.kind {
	case ownerThread:
		if owner.id == nil {
			return "", false, nil
		}
		if computer := application.ComputerOfThread(r.host.State(), *owner.id); computer != nil {
			return computer.ID, true, nil
		}
		return "", false, nil
	case ownerWorkspace:
		if computer := application.ComputerOfWorkspace(r.host.State(), *owner.id); computer != nil {
			return computer.ID, true, nil
		}
		return "", false, nil
	}
	if owner.id == nil || *owner.id == "this" {
		return "", false, nil
	}
	if *owner.id == "" || jsLength(*owner.id) > 256 {
		return "", false, errors.New("Invalid computer.")
	}
	return *owner.id, true, nil
}

// Read answers query from the computer owner names, typed by its kind (see read).
func (r *ComputerReads) Read(ctx context.Context, query contracts.ComputerQuery, owner ComputerReadOwner) (any, error) {
	kind := queryKind(query)
	return settle(kind, func() (any, error) {
		held, err := readOf(kind)
		if err != nil {
			return nil, err
		}
		if err := checked(query, kind); err != nil {
			return nil, err
		}
		id, elsewhere, err := r.holder(owner)
		if err != nil {
			return nil, err
		}
		if !elsewhere {
			return held.here(ctx, query, &r.host.ComputerQueryHost)
		}
		var links PairedQueries
		if r.host.Links != nil {
			links = r.host.Links()
		}
		if links == nil {
			return nil, errors.New("That computer is unavailable.")
		}
		ask := func(asked contracts.ComputerQuery) (jsontext.Value, error) { return links.Query(ctx, id, asked) }
		if held.forward != nil {
			return held.forward(ctx, query, &r.host.ComputerQueryHost, ask)
		}
		answer, err := ask(query)
		if err != nil {
			return nil, err
		}
		return held.received(answer, query)
	})
}

// Answer answers a paired computer's read from this computer, in the form the line carries.
func (r *ComputerReads) Answer(ctx context.Context, query contracts.ComputerQuery) (any, error) {
	answer, err := AnswerComputerQuery(ctx, query, &r.host.ComputerQueryHost)
	if err != nil {
		return nil, err
	}
	if held, ok := reads[queryKind(query)]; ok && held.sent != nil {
		return held.sent(answer), nil
	}
	return answer, nil
}

// Directories lists the folders a path being typed may continue into, on the computer named.
func (r *ComputerReads) Directories(ctx context.Context, prefix string, computerID *string) ([]string, error) {
	answer, err := r.Read(ctx, contracts.ComputerQueryDirectories{Prefix: prefix}, OwnerComputer(computerID))
	if err != nil {
		return nil, err
	}
	return answer.([]string), nil
}

// Commands is what the "/" menu offers in a workspace, wherever it is held.
func (r *ComputerReads) Commands(ctx context.Context, workspaceID string, engine domain.AgentEngine) (contracts.CommandDiscoveryResult, error) {
	answer, err := r.Read(ctx, contracts.ComputerQueryCommands{WorkspaceID: workspaceID, Engine: engine}, OwnerWorkspace(workspaceID))
	if err != nil {
		return nil, err
	}
	return answer.(contracts.CommandDiscoveryResult), nil
}

// DiffPatch is one file's patch, wherever the workspace is held.
func (r *ComputerReads) DiffPatch(ctx context.Context, workspaceID string, diffRange domain.DiffRange, path string, previousPath *string, ignoreWhitespace bool) (contracts.DiffPatchResult, error) {
	query := contracts.ComputerQueryDiffPatch{WorkspaceID: workspaceID, Range: diffRange, Path: path, PreviousPath: previousPath, IgnoreWhitespace: &ignoreWhitespace}
	answer, err := r.Read(ctx, query, OwnerWorkspace(workspaceID))
	if err != nil {
		return nil, err
	}
	return answer.(contracts.DiffPatchResult), nil
}

// Branches lists a workspace's branches, wherever it is held.
func (r *ComputerReads) Branches(ctx context.Context, workspaceID string) (contracts.BranchesResult, error) {
	answer, err := r.Read(ctx, contracts.ComputerQueryBranches{WorkspaceID: workspaceID}, OwnerWorkspace(workspaceID))
	if err != nil {
		return nil, err
	}
	return answer.(contracts.BranchesResult), nil
}

// Commits is which of hashes a workspace holds, wherever it is held.
func (r *ComputerReads) Commits(ctx context.Context, workspaceID string, hashes []string) ([]string, error) {
	answer, err := r.Read(ctx, contracts.ComputerQueryCommits{WorkspaceID: workspaceID, Hashes: hashes}, OwnerWorkspace(workspaceID))
	if err != nil {
		return nil, err
	}
	return answer.([]string), nil
}

// Attachment reads a saved attachment's bytes from the computer holding the thread.
func (r *ComputerReads) Attachment(ctx context.Context, name string, threadID *string) ([]byte, error) {
	answer, err := r.Read(ctx, contracts.ComputerQueryAttachment{Name: name}, OwnerThread(threadID))
	if err != nil {
		return nil, err
	}
	return answer.([]byte), nil
}

// MessageImage reads a reply's linked image, or its preview, from the computer holding the thread.
func (r *ComputerReads) MessageImage(ctx context.Context, file, root, messageID string, thumbnail bool, threadID *string) (MessageImageAnswer, error) {
	query := contracts.ComputerQueryMessageImage{Path: file, Root: root, Message: messageID, Thumbnail: &thumbnail}
	answer, err := r.Read(ctx, query, OwnerThread(threadID))
	if err != nil {
		return MessageImageAnswer{}, err
	}
	return answer.(MessageImageAnswer), nil
}

// TerminalOutput reads a shell on the computer named, for a viewer here. Nil is a terminal that is gone.
func (r *ComputerReads) TerminalOutput(ctx context.Context, computerID *string, terminalID string, after *float64) (*contracts.TerminalOutputRead, error) {
	answer, err := r.Read(ctx, contracts.ComputerQueryTerminalOutput{TerminalID: terminalID, After: after}, OwnerComputer(computerID))
	if err != nil {
		return nil, err
	}
	return answer.(*contracts.TerminalOutputRead), nil
}

// Threads reads threads on the computer named; these reads never follow that computer's own links.
func (r *ComputerReads) Threads(ctx context.Context, computerID *string, query contracts.ComputerThreadQuery) (jsontext.Value, error) {
	answer, err := r.Read(ctx, query, OwnerComputer(computerID))
	if err != nil {
		return nil, err
	}
	return answer.(jsontext.Value), nil
}

const (
	maxScanned = 4096
	maxLinks   = 40
)

// ListDirectories is a bounded directory scan: the folders a typed path may continue into.
func ListDirectories(prefix string) ([]string, error) {
	if !contracts.IsComputerQuery(map[string]any{"kind": "directories", "prefix": prefix}) {
		return nil, errors.New("Invalid directory prefix.")
	}
	home, _ := os.UserHomeDir()
	expanded := prefix
	if prefix == "~" || strings.HasPrefix(prefix, "~/") {
		expanded = filepath.Join(home, prefix[1:])
		if strings.HasSuffix(prefix, "/") {
			expanded += string(filepath.Separator)
		}
	}
	if expanded == "" || !filepath.IsAbs(expanded) {
		return []string{}, nil
	}
	whole := prefix == "~" || strings.HasSuffix(expanded, string(filepath.Separator))
	directory, partial := expanded, ""
	if !whole {
		directory, partial = filepath.Dir(expanded), filepath.Base(expanded)
	}
	handle, err := os.Open(directory)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || isNotDirectory(err) {
			return []string{}, nil
		}
		return nil, err
	}
	defer handle.Close()
	matches := []string{}
	scanned, links := 0, 0
scan:
	for {
		entries, err := handle.ReadDir(256)
		for _, entry := range entries {
			scanned++
			if scanned > maxScanned {
				break scan
			}
			name := entry.Name()
			if !strings.HasPrefix(name, partial) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(partial, ".")) {
				continue
			}
			root := filepath.Join(directory, name)
			folder := entry.IsDir()
			if !folder && entry.Type()&fs.ModeSymlink != 0 {
				links++
				if links <= maxLinks {
					info, err := os.Stat(root)
					folder = err == nil && info.IsDir()
				}
			}
			if folder && jsLength(root) < maxPathLength {
				matches = append(matches, root+string(filepath.Separator))
			}
			if len(matches) == maxDirectories {
				break scan
			}
		}
		if err != nil {
			if isNotDirectory(err) && scanned == 0 {
				return []string{}, nil
			}
			break
		}
	}
	slices.SortFunc(matches, localeCompare)
	return matches, nil
}

func isNotDirectory(err error) bool { return errors.Is(err, syscall.ENOTDIR) }
