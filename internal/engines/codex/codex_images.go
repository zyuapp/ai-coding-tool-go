package codex

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/workspace"
)

// Saves a generated image and answers with the message text that shows it.
type ImageOutput func(ctx context.Context, item protocol.ThreadItem, root string) (string, error)

const maxImageBytes = 25 * 1024 * 1024

// Bound the read on an open handle even if the original changes during capture.
func readImageFile(file string) ([]byte, error) {
	handle, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxImageBytes {
		return nil, errors.New("Image is too large or unavailable.")
	}
	bytes := make([]byte, info.Size())
	if _, err := io.ReadFull(handle, bytes); err != nil {
		return nil, errors.New("Image changed while reading.")
	}
	return bytes, nil
}

var base64Payload = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

// Decodes base64 as Node's Buffer.from(text, "base64") does once the text is known to be base64.
func decodeBase64(text string) []byte {
	text = strings.TrimRight(text, "=")
	if len(text)%4 == 1 {
		text = text[:len(text)-1]
	}
	bytes, _ := base64.RawStdEncoding.DecodeString(text)
	return bytes
}

// JavaScript's encodeURIComponent.
func encodeURIComponent(text string) string {
	var out strings.Builder
	for _, char := range []byte(text) {
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.IndexByte("-_.!~*'()", char) >= 0 {
			out.WriteByte(char)
		} else {
			fmt.Fprintf(&out, "%%%02X", char)
		}
	}
	return out.String()
}

func failureType(failure jsontext.Value) string {
	var read struct {
		Type string `json:"type"`
	}
	if len(failure) == 0 || jsonx.Unmarshal(failure, &read) != nil {
		return ""
	}
	return read.Type
}

// Saves a generated image under directory and answers with the Markdown that shows it.
func CodexImageOutput(item protocol.ThreadItem, root, directory string) (string, error) {
	if failureType(item.Failure) == "usageLimitExceeded" {
		return "Image generation failed: the Codex image-generation usage limit has been reached.", nil
	}
	switch deref(item.Status) {
	case "failed":
		return "Image generation failed. Codex did not return an image.", nil
	case "cancelled", "interrupted":
		return "Image generation was cancelled.", nil
	}
	result := item.ImageResult()
	savedPath := deref(item.SavedPath)
	if savedPath == "" && result == "" {
		return "Image generation finished without returning an image.", nil
	}
	if directory == "" || !filepath.IsAbs(directory) {
		return "", errors.New("Generated image storage is unavailable.")
	}
	var bytes []byte
	extension := ".png"
	if savedPath != "" {
		err := func() error {
			if !domain.IsMessageImageFile(savedPath) {
				return errors.New("Unsupported image file type.")
			}
			var roots []string
			if root != "" {
				roots = []string{root}
			}
			file, err := workspace.OpenableFile(roots, savedPath)
			if err != nil {
				return err
			}
			if bytes, err = readImageFile(file); err != nil {
				return err
			}
			extension = strings.ToLower(filepath.Ext(savedPath))
			return nil
		}()
		if err != nil {
			if result == "" {
				return "", err
			}
			bytes = nil
		}
	}
	if bytes == nil {
		if len(result) > (maxImageBytes+2)/3*4 {
			return "", errors.New("Image is too large.")
		}
		if !base64Payload.MatchString(result) {
			return "", errors.New("Image payload is not base64.")
		}
		bytes = decodeBase64(result)
		if len(bytes) == 0 || len(bytes) > maxImageBytes {
			return "", errors.New("Image is empty or too large.")
		}
	}
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return "", err
	}
	file := filepath.Join(directory, uuid.NewString()+extension)
	handle, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return "", err
	}
	_, err = handle.Write(bytes)
	if closeErr := handle.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	// Encode each segment so spaces, percent signs and Markdown delimiters remain literal filenames.
	segments := strings.Split(file, "/")
	for index, segment := range segments {
		segments[index] = encodeURIComponent(segment)
	}
	return "[Generated image](<" + strings.Join(segments, "/") + ">)", nil
}
