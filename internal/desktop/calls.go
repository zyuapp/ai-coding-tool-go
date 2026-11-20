package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"
	"os"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var errNotKept = errors.New("That image is not one this app is keeping.")

// arg reads the window's argument at index; an absent one reads as T's zero value.
func arg[T any](args []jsontext.Value, index int) (T, error) {
	var value T
	if index >= len(args) || args[index] == nil {
		return value, nil
	}
	err := jsonx.Unmarshal(args[index], &value)
	return value, err
}

// optional reads an argument the window may leave undefined, as nil.
func optionalArg[T any](args []jsontext.Value, index int) (*T, error) {
	if index >= len(args) || args[index] == nil {
		return nil, nil
	}
	value, err := arg[T](args, index)
	return &value, err
}

func answer(value any, err error) (jsontext.Value, error) {
	if err != nil {
		return nil, err
	}
	return jsonx.Marshal(value)
}

func settled(err error) (jsontext.Value, error) { return nil, err }

// Call answers every window call the window desktop does not answer itself (window-desktop.ts).
func (c *Controller) Call(ctx context.Context, name string, args []jsontext.Value) Reply {
	return Async(func() (jsontext.Value, error) { return c.call(ctx, name, args) })
}

func (c *Controller) keptAttachment(file string) (string, error) {
	saved, ok := c.services.Attachments.SavedAttachmentPath(file)
	if !ok {
		return "", errNotKept
	}
	return saved, nil
}

func (c *Controller) call(ctx context.Context, name string, args []jsontext.Value) (jsontext.Value, error) {
	s, desktop := c.services, c.desktop
	// The guards have checked every argument; the first is an id or path for most calls.
	id, _ := arg[string](args, 0)
	switch name {
	case CallOpenFolder:
		return answer(desktop.OpenFolder(ctx))
	case CallProjectlessWorkspace:
		return answer(desktop.ProjectlessWorkspace(ctx))
	case CallCommands:
		engine, err := arg[domain.AgentEngine](args, 1)
		if err != nil {
			return nil, err
		}
		return answer(s.Reads.Commands(ctx, id, engine))
	case CallBranches:
		return answer(s.Reads.Branches(ctx, id))
	case CallCommits:
		hashes, err := arg[[]string](args, 1)
		if err != nil {
			return nil, err
		}
		return answer(s.Reads.Commits(ctx, id, hashes))
	case CallDiffPatch:
		diffRange, err := arg[domain.DiffRange](args, 1)
		if err != nil {
			return nil, err
		}
		path, err := arg[string](args, 2)
		if err != nil {
			return nil, err
		}
		previousPath, err := optionalArg[string](args, 3)
		if err != nil {
			return nil, err
		}
		ignoreWhitespace, err := optionalArg[bool](args, 4)
		if err != nil {
			return nil, err
		}
		return answer(desktop.DiffPatch(ctx, id, diffRange, path, previousPath, ignoreWhitespace))
	case CallSaveAttachment:
		original, err := optionalArg[string](args, 1)
		if err != nil {
			return nil, err
		}
		return answer(desktop.SaveAttachment(ctx, id, original))
	case CallReadAttachment:
		saved, err := c.keptAttachment(id)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(saved)
		if err != nil {
			return nil, err
		}
		return answer(base64.StdEncoding.EncodeToString(data), nil)
	case CallReadAttachmentContext:
		saved, err := c.keptAttachment(id)
		if err != nil {
			return nil, err
		}
		if held := s.Attachments.ReadAttachmentContext(saved); len(held) > 0 {
			return held, nil
		}
		return jsontext.Value("null"), nil
	case CallSetBrowserBounds:
		bounds, err := optionalArg[domain.BrowserBounds](args, 0)
		if err != nil {
			return nil, err
		}
		return settled(c.browser.SetBrowserBounds(ctx, bounds))
	case CallFocusBrowserTab:
		return settled(desktop.FocusBrowserTab(ctx, id))
	case CallTerminalSnapshot:
		return answer(s.Terminals.TerminalSnapshot(id), nil)
	case CallReadRemoteTerminal:
		terminalID, err := arg[string](args, 1)
		if err != nil {
			return nil, err
		}
		after, err := optionalArg[float64](args, 2)
		if err != nil {
			return nil, err
		}
		return answer(s.Reads.TerminalOutput(ctx, &id, terminalID, after))
	case CallWatchSimulator:
		return settled(c.simulator.WatchSimulator(ctx, id))
	case CallUnwatchSimulator:
		return settled(c.simulator.UnwatchSimulator(ctx, id))
	}
	return nil, errUnknownCall
}

// Attachment reads a kept attachment by its file name, from whichever computer holds the thread.
func (c *Controller) Attachment(ctx context.Context, name, thread string) ([]byte, error) {
	if !c.waitReady(ctx) {
		return nil, ctx.Err()
	}
	return c.services.Reads.Attachment(ctx, name, threadOwner(thread))
}

// MessageImage reads an image a reply linked, or its thumbnail, from whichever computer holds the thread.
func (c *Controller) MessageImage(ctx context.Context, query contracts.ComputerQueryMessageImage, thread string) ([]byte, string, error) {
	if !c.waitReady(ctx) {
		return nil, "", ctx.Err()
	}
	image, err := c.services.Reads.MessageImage(ctx, query.Path, query.Root, query.Message, query.Thumbnail != nil && *query.Thumbnail, threadOwner(thread))
	return image.Bytes, image.ContentType, err
}

func threadOwner(thread string) *string {
	if thread == "" {
		return nil
	}
	return &thread
}
