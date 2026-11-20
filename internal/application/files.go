package application

import (
	"fmt"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const fileHeading = "Files and folders I attached to my message, by where they are on this machine:"

// How attached files reach the agent: as locations to open, never as bytes the app read for it.
func PromptWithFiles(text string, files []*domain.AttachedFile) string {
	if len(files) == 0 {
		return text
	}
	lines := make([]string, len(files))
	for i, file := range files {
		lines[i] = file.Path
		if file.Folder != nil && *file.Folder {
			lines[i] += " (folder)"
		}
	}
	parts := make([]string, 0, 2)
	for _, part := range []string{text, fileHeading + "\n" + strings.Join(lines, "\n")} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n\n")
}

// What a thread dropped into with nothing typed is called.
func FileTitle(files []*domain.AttachedFile) string {
	if len(files) == 0 {
		return ""
	}
	if len(files) == 1 {
		return domain.ClampTitle(files[0].Name)
	}
	return domain.ClampTitle(fmt.Sprintf("%d files", len(files)))
}
