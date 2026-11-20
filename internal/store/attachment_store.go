package store

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	savedAttachmentName = regexp.MustCompile(`^[A-Za-z0-9-]+\.png$`)
	base64Payload       = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

	errAttachmentSize = errors.New("Attachment is empty or too large.")
)

const maxAttachmentContextBytes = 128_000

// Attachments is where this host keeps attachments: one flat folder of PNGs named by the app.
type Attachments struct {
	directory string
}

// NewAttachments keeps attachments in the profile's attachments folder.
func NewAttachments(userData string) *Attachments {
	return &Attachments{directory: filepath.Join(userData, "attachments")}
}

func (a *Attachments) Directory() string { return a.directory }

// IsSavedAttachmentName is whether name is one the app gives an attachment.
func IsSavedAttachmentName(name string) bool {
	return savedAttachmentName.MatchString(name)
}

// ReadSavedAttachment reads only a saved name, never an arbitrary path supplied by another
// computer, as base64.
func (a *Attachments) ReadSavedAttachment(name string) (string, error) {
	if !IsSavedAttachmentName(name) {
		return "", errors.New("Invalid attachment name.")
	}
	handle, err := os.Open(filepath.Join(a.directory, name))
	if err != nil {
		return "", err
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > MaxAttachmentBytes {
		return "", errAttachmentSize
	}
	bytes, err := io.ReadAll(io.LimitReader(handle, MaxAttachmentBytes+1))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(bytes), nil
}

// SavedAttachmentPath is the file in the attachments folder that file names, or false when file
// is anything a renderer may not name.
func (a *Attachments) SavedAttachmentPath(file string) (string, bool) {
	name := attachmentName(file)
	if !IsSavedAttachmentName(name) {
		return "", false
	}
	saved := filepath.Join(a.directory, name)
	if resolvePath(file) != saved {
		return "", false
	}
	return saved, true
}

// WriteAttachment puts base64 PNG bytes in the attachments folder under a name of the app's own
// making. context, when not nil, is a ScreenshotContext (or its JSON) kept beside the image.
func (a *Attachments) WriteAttachment(data string, context any) (string, error) {
	if len(data) == 0 || len(data) > MaxAttachmentEncodedBytes {
		return "", errAttachmentSize
	}
	if !base64Payload.MatchString(data) {
		return "", errors.New("Attachment payload is not base64.")
	}
	bytes := decodeBase64(data)
	if len(bytes) == 0 || len(bytes) > MaxAttachmentBytes {
		return "", errAttachmentSize
	}
	if err := os.MkdirAll(a.directory, 0o777); err != nil {
		return "", err
	}
	file := filepath.Join(a.directory, randomUUID()+".png")
	if err := os.WriteFile(file, bytes, 0o666); err != nil {
		return "", err
	}
	// Context is optional: a sidecar storage failure must not discard a successful screenshot.
	if context != nil {
		if encoded, err := encodeJSON(context); err == nil && string(encoded) != "null" {
			_ = os.WriteFile(file+".context.json", encoded, 0o666)
		}
	}
	return file, nil
}

// ReadAttachmentContext is the ScreenshotContext saved beside an attachment, or nil. It follows
// the PNG path across drafts, history, and app restarts.
func (a *Attachments) ReadAttachmentContext(file string) jsontext.Value {
	saved, ok := a.SavedAttachmentPath(file)
	if !ok {
		return nil
	}
	handle, err := os.Open(saved + ".context.json")
	if err != nil {
		return nil
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxAttachmentContextBytes {
		return nil
	}
	bytes := make([]byte, info.Size())
	if _, err := io.ReadFull(handle, bytes); err != nil {
		return nil
	}
	if _, err := parseJSON(bytes); err != nil || !isScreenshotContext(bytes) {
		return nil
	}
	return bytes
}

// decodeBase64 decodes as Node's Buffer.from(data, "base64") does once the payload is known to be
// base64 characters: padding is optional and a dangling character is dropped.
func decodeBase64(data string) []byte {
	data = strings.TrimRight(data, "=")
	if len(data)%4 == 1 {
		data = data[:len(data)-1]
	}
	bytes, err := base64.RawStdEncoding.DecodeString(data)
	if err != nil {
		return nil
	}
	return bytes
}

func encodeJSON(value any) (jsontext.Value, error) {
	switch typed := value.(type) {
	case jsontext.Value:
		return typed, nil
	case []byte:
		return typed, nil
	}
	return json.Marshal(value)
}

func randomUUID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
}
