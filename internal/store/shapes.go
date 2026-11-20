package store

import (
	"encoding/json/jsontext"
	"math"
	"regexp"
	"slices"
	"strings"
)

// Local copies of the domain checks the store applies to its own rows (src/domain/automation.ts,
// worktree.ts, screenshot-context.ts, message-artifacts.ts, conversation.ts, and
// src/application/attachments.ts), read straight off the stored JSON.

const (
	maxAutomationPrompt   = 100_000
	maxAutomationSchedule = 200
	maxAutomationTimezone = 100
	maxSurfaceWhen        = 500
	maxEndsWhen           = 500

	// MaxAttachmentBytes is domain MAX_ATTACHMENT_BYTES.
	MaxAttachmentBytes = 25 * 1024 * 1024
	// MaxAttachmentEncodedBytes is domain MAX_ATTACHMENT_ENCODED_BYTES.
	MaxAttachmentEncodedBytes = (MaxAttachmentBytes + 2) / 3 * 4

	// MaxMessageImages is domain MAX_MESSAGE_IMAGES.
	MaxMessageImages = 20
	// MessageImageScheme is domain MESSAGE_IMAGE_SCHEME.
	MessageImageScheme = "message-image"

	maxScreenshotContextText = 12_000
	maxScreenshotContextName = 512
)

var executionPolicies = []string{"confirm", "plan", "allow-edits", "autonomous", "bypass"}

func isText(value jsontext.Value, maxLength int) bool {
	text, ok := stringValue(value)
	if !ok {
		return false
	}
	length := utf16Length(text)
	return length > 0 && length <= maxLength
}

func isTimestamp(value jsontext.Value) bool {
	number, ok := numberValue(value)
	return ok && isSafeInteger(number) && number >= 0
}

func isSafeInteger(number float64) bool {
	return number == math.Trunc(number) && math.Abs(number) <= 1<<53-1
}

func optional(value jsontext.Value, check func(jsontext.Value) bool) bool {
	return value == nil || check(value)
}

func isExecutionPolicy(value jsontext.Value) bool {
	text, ok := stringValue(value)
	return ok && slices.Contains(executionPolicies, text)
}

func isAutomationRunStatus(value jsontext.Value) bool {
	text, ok := stringValue(value)
	return ok && slices.Contains([]string{"succeeded", "failed", "cancelled", "skipped", "missed"}, text)
}

func textUpTo(maxLength int) func(jsontext.Value) bool {
	return func(value jsontext.Value) bool { return isText(value, maxLength) }
}

// isAutomation is domain isAutomation.
func isAutomation(record object) bool {
	paused := record.get("paused")
	return isText(record.get("id"), 256) &&
		isText(record.get("taskId"), 256) &&
		isText(record.get("prompt"), maxAutomationPrompt) &&
		isText(record.get("schedule"), maxAutomationSchedule) &&
		optional(record.get("timezone"), textUpTo(maxAutomationTimezone)) &&
		optional(record.get("policy"), isExecutionPolicy) &&
		optional(record.get("surfaceWhen"), textUpTo(maxSurfaceWhen)) &&
		optional(record.get("endsWhen"), textUpTo(maxEndsWhen)) &&
		(string(paused) == "true" || string(paused) == "false") &&
		isTimestamp(record.get("createdAt")) &&
		isTimestamp(record.get("updatedAt")) &&
		isTimestamp(record.get("runCount")) &&
		optional(record.get("lastRunAt"), isTimestamp) &&
		optional(record.get("lastStatus"), isAutomationRunStatus) &&
		optional(record.get("lastStatusAt"), isTimestamp) &&
		optional(record.get("consecutiveDeclines"), isTimestamp) &&
		optional(record.get("overrunCount"), isTimestamp)
}

func nonEmptyString(value jsontext.Value) bool {
	text, ok := stringValue(value)
	return ok && !jsTrimmedEmpty(text)
}

func finiteNumber(value jsontext.Value) bool {
	number, ok := numberValue(value)
	return ok && !math.IsInf(number, 0) && !math.IsNaN(number)
}

// isWorktree is domain isWorktree.
func isWorktree(record object) bool {
	return nonEmptyString(record.get("id")) &&
		optional(record.get("name"), nonEmptyString) &&
		nonEmptyString(record.get("projectId")) &&
		nonEmptyString(record.get("root")) &&
		nonEmptyString(record.get("workspaceId")) &&
		nonEmptyString(record.get("baseCommit")) &&
		finiteNumber(record.get("createdAt")) &&
		finiteNumber(record.get("lastUsedAt"))
}

// isScreenshotContext is domain isScreenshotContext.
func isScreenshotContext(value jsontext.Value) bool {
	record, ok, err := parseObject(value)
	if err != nil || !ok {
		return false
	}
	platform, _ := stringValue(record.get("platform"))
	app, appOK := stringValue(record.get("app"))
	title, titleOK := stringValue(record.get("title"))
	capturedAt, capturedOK := numberValue(record.get("capturedAt"))
	return string(record.get("version")) == "1" &&
		slices.Contains([]string{"macos", "linux-x11", "linux-hyprland"}, platform) &&
		appOK && utf16Length(app) <= maxScreenshotContextName &&
		titleOK && utf16Length(title) <= maxScreenshotContextName &&
		capturedOK && isSafeInteger(capturedAt) && capturedAt >= 0 && capturedAt <= 8_640_000_000_000_000 &&
		isAccessibilitySnapshot(record.get("accessibility"))
}

func isAccessibilitySnapshot(value jsontext.Value) bool {
	record, ok, err := parseObject(value)
	if err != nil || !ok {
		return false
	}
	status, _ := stringValue(record.get("status"))
	if status == "captured" {
		text, textOK := stringValue(record.get("text"))
		truncated := string(record.get("truncated"))
		return textOK && text != "" && utf16Length(text) <= maxScreenshotContextText && (truncated == "true" || truncated == "false")
	}
	reason, reasonOK := stringValue(record.get("reason"))
	return status == "unavailable" && reasonOK && slices.Contains([]string{"permission", "timeout", "busy", "window", "empty", "failed"}, reason)
}

var (
	messageImageFile = regexp.MustCompile(`(?i)\.(?:png|jpe?g|gif|webp)$`)
	urlWithScheme    = regexp.MustCompile(`(?i)^[a-z][a-z\d+.-]*://`)
	imageSource      = regexp.MustCompile(`^(?:attachment|message-image)://file/`)
)

// isMessageImageReference is domain isMessageImageReference.
func isMessageImageReference(file, root, message string) bool {
	return utf16Length(file) <= 4096 && messageImageFile.MatchString(file) && !urlWithScheme.MatchString(file) &&
		utf16Length(root) <= 4096 &&
		message != "" && utf16Length(message) <= 256
}

// isImageSource is domain isImageSource.
func isImageSource(value string) bool {
	return utf16Length(value) <= 16_384 && imageSource.MatchString(value)
}

// attachmentName is application attachmentName.
func attachmentName(filePath string) string {
	return filePath[strings.LastIndexAny(filePath, `\/`)+1:]
}
