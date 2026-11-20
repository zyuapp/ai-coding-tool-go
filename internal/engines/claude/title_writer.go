package claude

import (
	"context"
	"encoding/base64"
	"os"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/claude/protocol"
)

func titleImageBlocks(attachments []string) []protocol.ContentBlock {
	blocks := []protocol.ContentBlock{}
	for _, file := range agent.ReadableImages(attachments) {
		bytes, err := os.ReadFile(file)
		if err != nil || len(bytes) == 0 || len(bytes) > agent.ImageByteLimit {
			continue
		}
		blocks = append(blocks, protocol.Base64ImageBlock("image/png", base64.StdEncoding.EncodeToString(bytes)))
	}
	return blocks
}

// suggestTaskTitle names a thread from its first message and the screenshots sent with it. The
// session carries no settings, no CLAUDE.md, no tools and no project directory, so a title costs one
// small Haiku turn; images arrive as bytes, so nothing on the machine is reachable from it.
func suggestTaskTitle(ctx context.Context, open QueryFactory, text string, attachments []string) *string {
	images := titleImageBlocks(attachments)
	question := agent.TitleQuestion(text)
	options := claudeProcess()
	options.Model = "haiku"
	options.Cwd = os.TempDir()
	options.SettingSources = []string{}
	options.SystemPrompt = protocol.SystemPrompt{Kind: protocol.SystemPromptCustom, Text: agent.TitleInstructions}
	options.Tools = []string{}
	options.JSONSchema = agent.TitleSchema
	options.MaxTurns = 1
	session, err := open(ctx, options)
	if err != nil {
		return nil
	}
	defer session.Close()

	content := protocol.TextContent(question)
	if len(images) > 0 {
		content = protocol.BlockContent(append(images, protocol.TextBlock(question))...)
	}
	if session.SendUser(protocol.NewUserMessage(content)) != nil {
		return nil
	}
	for message := range session.Messages() {
		result, ok := message.(*protocol.ResultMessage)
		if !ok {
			continue
		}
		session.EndInput()
		if result.Subtype != protocol.ResultSubtypeSuccess || result.IsError {
			return nil
		}
		if title, ok := agent.TitleOf(result.StructuredOutput); ok {
			return &title
		}
		return nil
	}
	return nil
}
