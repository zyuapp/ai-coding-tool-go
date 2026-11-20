package application

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const attachmentHeading = "Attached screenshots (numbered red boxes mark the areas in question):"

// The letter every mark on one screenshot carries, so a mark names itself across a send of several.
// A lone screenshot needs no letter, since its numbers are already unambiguous.
func MarkPrefix(index, total int) string {
	if total > 1 {
		return string(rune('A' + index%26))
	}
	return ""
}

func PromptWithAttachments(text string, attachments []*domain.RunAttachment) string {
	if len(attachments) == 0 {
		return text
	}
	blocks := make([]string, len(attachments))
	for at, attachment := range attachments {
		prefix := MarkPrefix(at, len(attachments))
		count := len(attachment.Labels)
		span := ""
		if prefix != "" && count > 0 {
			if count > 1 {
				span = fmt.Sprintf(" (marks %s1–%s%d)", prefix, prefix, count)
			} else {
				span = fmt.Sprintf(" (mark %s1)", prefix)
			}
		}
		lines := []string{attachment.Path + span}
		for index, label := range attachment.Labels {
			if label := threadsTrim(label); label != "" {
				lines = append(lines, fmt.Sprintf("  %s%d. %s", prefix, index+1, label))
			}
		}
		if attachment.Context != nil {
			lines = append(lines, domain.ScreenshotContextPrompt(attachment.Context))
		}
		blocks[at] = strings.Join(lines, "\n")
	}
	return threadsJoinNonEmpty([]string{text, attachmentHeading + "\n" + strings.Join(blocks, "\n")}, "\n\n")
}

func ThreadTitleFor(text string, attachments []*domain.RunAttachment) string {
	if text == "" {
		if len(attachments) == 1 {
			text = "Screenshot"
		} else {
			text = fmt.Sprintf("%d screenshots", len(attachments))
		}
	}
	return domain.ClampTitle(text)
}

const AttachmentScheme = "attachment"

// Attachments live in one flat directory, so the file name alone addresses them over the `attachment:` scheme.
func AttachmentName(filePath string) string {
	if at := strings.LastIndexAny(filePath, `\/`); at >= 0 {
		return filePath[at+1:]
	}
	return filePath
}

// The `attachment:` URL of a file; taskID nil adds no query.
func AttachmentURL(filePath string, taskID *string) string {
	query := ""
	if taskID != nil {
		query = "?" + url.Values{"taskId": {*taskID}}.Encode()
	}
	return AttachmentScheme + "://file/" + threadsEncodeURIComponent(AttachmentName(filePath)) + query
}

// threadsEncodeURIComponent is encodeURIComponent.
func threadsEncodeURIComponent(text string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			encoded.WriteByte(c)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hex[c>>4])
		encoded.WriteByte(hex[c&15])
	}
	return encoded.String()
}
