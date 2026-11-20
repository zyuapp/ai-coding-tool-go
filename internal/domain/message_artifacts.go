package domain

import (
	"regexp"
	"slices"
	"strings"
)

const MessageImageScheme = "message-image"
const MaxMessageImages = 20

var messageImageFile = regexp.MustCompile(`(?i)\.(?:png|jpe?g|gif|webp)$`)

// messageImageMention is \.(?:png|jpe?g|gif|webp)\b with JavaScript's ASCII word boundary.
var messageImageMention = regexp.MustCompile(`(?i)\.(?:png|jpe?g|gif|webp)(?:[^A-Za-z0-9_]|$)`)
var messageURLScheme = regexp.MustCompile(`(?i)^[a-z][a-z\d+.-]*://`)
var messageCommitHash = regexp.MustCompile(`(?i)^[a-f\d]{7,40}$`)
var imageSource = regexp.MustCompile(`^(?:attachment|message-image)://file/`)

func IsMessageImageFile(file string) bool {
	return messageImageFile.MatchString(file)
}

// MayLinkImages is a cheap test that rules out text linking no image.
func MayLinkImages(text string) bool {
	return messageImageMention.MatchString(text)
}

func IsMessageImageReference(file, root, message any) bool {
	fileText, ok := file.(string)
	if !ok || utf16Length(fileText) > 4096 || !IsMessageImageFile(fileText) || messageURLScheme.MatchString(fileText) {
		return false
	}
	rootText, ok := root.(string)
	if !ok || utf16Length(rootText) > 4096 {
		return false
	}
	messageText, ok := message.(string)
	return ok && messageText != "" && utf16Length(messageText) <= 256
}

// MessageImagePath is the local raster image a link names; false for anything else.
func MessageImagePath(href string) (string, bool) {
	file := ParseFileHref(href)
	if file == nil || file.Line != nil || !IsMessageImageFile(file.File) {
		return "", false
	}
	return file.File, true
}

func IsCommitHash(value any) bool {
	hash, ok := value.(string)
	return ok && messageCommitHash.MatchString(hash)
}

// MaxCommitChecks is how many hashes one check may ask about.
const MaxCommitChecks = 64

func IsCommitHashes(value any) bool {
	hashes, ok := value.([]any)
	return ok && len(hashes) > 0 && len(hashes) <= MaxCommitChecks && !slices.ContainsFunc(hashes, func(hash any) bool { return !IsCommitHash(hash) })
}

// MessageImageURL takes nil taskID for none.
func MessageImageURL(file, root, messageID string, thumbnail bool, taskID *string) string {
	query := [][2]string{{"path", file}, {"root", root}, {"message", messageID}}
	if thumbnail {
		query = append(query, [2]string{"thumbnail", "1"})
	}
	if taskID != nil {
		query = append(query, [2]string{"taskId", *taskID})
	}
	return MessageImageScheme + "://file/?" + messageFormEncode(query)
}

// messageFormEncode serializes pairs as URLSearchParams does.
func messageFormEncode(pairs [][2]string) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	encode := func(value string) {
		for index := 0; index < len(value); index++ {
			c := value[index]
			switch {
			case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '*' || c == '-' || c == '.' || c == '_':
				out.WriteByte(c)
			case c == ' ':
				out.WriteByte('+')
			default:
				out.WriteByte('%')
				out.WriteByte(hex[c>>4])
				out.WriteByte(hex[c&15])
			}
		}
	}
	for index, pair := range pairs {
		if index > 0 {
			out.WriteByte('&')
		}
		encode(strings.ToValidUTF8(pair[0], "�"))
		out.WriteByte('=')
		encode(strings.ToValidUTF8(pair[1], "�"))
	}
	return out.String()
}

func IsImageSource(value any) bool {
	source, ok := value.(string)
	return ok && utf16Length(source) <= 16_384 && imageSource.MatchString(source)
}
