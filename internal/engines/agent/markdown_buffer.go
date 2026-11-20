package agent

import (
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

type MarkdownBuffer struct {
	// Text the stream has produced but not released yet.
	Text string
	Scan domain.BlockScan
}

func OpenMarkdownBuffer() *MarkdownBuffer {
	return &MarkdownBuffer{Scan: domain.EmptyScan()}
}

// Releases whole Markdown blocks and keeps the rest buffered, so a half-written fence never ships.
func AppendCompleteMarkdown(buffer *MarkdownBuffer, text string) string {
	buffer.Text += text
	scan := domain.ScanBlocks(buffer.Text, buffer.Scan)
	buffer.Scan = scan
	if scan.SafeEnd == 0 {
		return ""
	}
	safeEnd := int(scan.SafeEnd)
	complete := buffer.Text[:safeEnd]
	buffer.Text = buffer.Text[safeEnd:]
	buffer.Scan = domain.BlockScan{SafeEnd: 0, Scanned: scan.Scanned - scan.SafeEnd, Fence: scan.Fence}
	return complete
}

type emitter interface {
	Emit(event ProviderEvent)
}

// Anything with an Emit, such as a run's input.
type EmitFunc func(event ProviderEvent)

func (emit EmitFunc) Emit(event ProviderEvent) { emit(event) }

// Streams a delta of one message: its whole blocks as they complete, and the rest as its tail.
func StreamMarkdown(buffers map[string]*MarkdownBuffer, run emitter, messageID, delta string) {
	buffer := buffers[messageID]
	if buffer == nil {
		buffer = OpenMarkdownBuffer()
		buffers[messageID] = buffer
	}
	if complete := AppendCompleteMarkdown(buffer, delta); complete != "" {
		run.Emit(EventAssistant{MessageID: messageID, Text: complete, Append: true})
	}
	run.Emit(EventAssistantTail{MessageID: messageID, Text: buffer.Text})
}

// Ends one message. One that streamed is on screen but for its tail; one that did not arrives whole.
func EndMarkdown(buffers map[string]*MarkdownBuffer, run emitter, messageID, whole string) {
	if buffer := buffers[messageID]; buffer != nil {
		delete(buffers, messageID)
		if buffer.Text != "" {
			run.Emit(EventAssistant{MessageID: messageID, Text: buffer.Text, Append: true})
		}
	} else if strings.TrimSpace(whole) != "" {
		run.Emit(EventAssistant{MessageID: messageID, Text: whole})
	}
}
