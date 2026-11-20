package domain

import "strings"

func approvalPreviewText(input map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := input[key].(string); ok && value != "" {
			return value, true
		}
	}
	return "", false
}

// approvalPreviewLines splits a text into lines, without the empty one a closing newline would leave after the last.
func approvalPreviewLines(value string) []string {
	split := strings.Split(value, "\n")
	if len(split) > 1 && split[len(split)-1] == "" {
		split = split[:len(split)-1]
	}
	return split
}

func ApprovalPreviewApprovalPreview(input map[string]any) ApprovalPreview {
	path, hasPath := approvalPreviewText(input, "file_path", "filePath", "notebook_path", "path")
	removed, hasRemoved := approvalPreviewText(input, "old_string", "oldString")
	added, hasAdded := input["new_string"]
	if added == nil {
		added, hasAdded = input["newString"]
	}
	if text, ok := added.(string); hasPath && hasRemoved && hasAdded && ok {
		lines := []string{}
		if text != "" {
			lines = approvalPreviewLines(text)
		}
		return ApprovalPreviewEdit{Path: path, Removed: approvalPreviewLines(removed), Added: lines}
	}
	if content, ok := approvalPreviewText(input, "content"); hasPath && ok {
		return ApprovalPreviewFile{Path: path, Content: content}
	}
	if command, ok := approvalPreviewText(input, "command"); ok {
		return ApprovalPreviewCommand{Command: command}
	}
	if hasPath {
		return ApprovalPreviewSubject{Text: path}
	}
	if url, ok := approvalPreviewText(input, "url"); ok {
		return ApprovalPreviewSubject{Text: url}
	}
	return ApprovalPreviewRaw{}
}
