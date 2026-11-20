package store

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/jsontext"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var messageImageMIME = map[string]string{"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}

var (
	errInvalidImageReference = errors.New("Invalid image reference.")
	errInvalidImageBytes     = errors.New("Invalid image bytes.")
)

// MessageImageStore keeps copies of the images replies link to, so a preview outlives its source.
// Only images referenced in replies are retained; browser captures that are never used stay
// temporary.
type MessageImageStore struct {
	directory string
	// thumbnail cuts a preview from an image; nil on a host with no image decoder.
	thumbnail func([]byte) ([]byte, error)
	// openableFile is path-policy openableFile: the file a link names, found under roots.
	openableFile func(roots []string, file string) (string, error)

	mu      sync.Mutex
	pending map[string]*imageCopy
	decode  sync.Mutex
}

type imageCopy struct {
	done chan struct{}
	file string
	err  error
}

// MessageImage is an image's bytes and the type they are served as.
type MessageImage struct {
	Bytes       []byte
	ContentType string
}

// NewMessageImageStore keeps copies in directory. thumbnail may be nil; openableFile may be nil,
// in which case a link is read at the absolute path it names.
func NewMessageImageStore(directory string, thumbnail func([]byte) ([]byte, error), openableFile func(roots []string, file string) (string, error)) *MessageImageStore {
	return &MessageImageStore{directory: directory, thumbnail: thumbnail, openableFile: openableFile, pending: map[string]*imageCopy{}}
}

func (s *MessageImageStore) Directory() string { return s.directory }

type imageRequest struct {
	file, root, key, extension string
}

func messageImageRequest(file, root, messageID string) (imageRequest, error) {
	if !isMessageImageReference(file, root, messageID) {
		return imageRequest{}, errInvalidImageReference
	}
	sum := sha256.Sum256(joinArray([]jsontext.Value{jsString(messageID), jsString(file)}))
	return imageRequest{file: file, root: root, key: hex.EncodeToString(sum[:]), extension: imageExtension(file)}, nil
}

func imageExtension(file string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))
}

func (s *MessageImageStore) copyImage(request imageRequest, load func() ([]byte, error)) (string, error) {
	destination := filepath.Join(s.directory, request.key+"."+request.extension)
	if info, err := os.Stat(destination); err == nil && info.Mode().IsRegular() {
		return destination, nil
	}
	// Network waits must not hold up this computer's thumbnail decoder.
	var remote []byte
	if load != nil {
		bytes, err := load()
		if err != nil {
			return "", err
		}
		remote = bytes
	}
	s.decode.Lock()
	defer s.decode.Unlock()
	bytes := remote
	if bytes == nil {
		source, err := s.openable(request)
		if err != nil {
			return "", err
		}
		if bytes, err = ReadImageFile(source); err != nil {
			return "", err
		}
	}
	if len(bytes) == 0 || len(bytes) > MaxImageBytes {
		return "", errImageUnavailable
	}
	var thumbnail []byte
	if s.thumbnail != nil {
		cut, err := s.thumbnail(bytes)
		if err != nil {
			return "", err
		}
		thumbnail = cut
	}
	if err := os.MkdirAll(s.directory, 0o777); err != nil {
		return "", err
	}
	if thumbnail != nil {
		if err := os.WriteFile(filepath.Join(s.directory, request.key+"-thumb.png"), thumbnail, 0o666); err != nil {
			return "", err
		}
	}
	staging := destination + ".tmp"
	if err := os.WriteFile(staging, bytes, 0o666); err != nil {
		return "", err
	}
	if err := os.Rename(staging, destination); err != nil {
		return "", err
	}
	return destination, nil
}

func (s *MessageImageStore) openable(request imageRequest) (string, error) {
	var roots []string
	if request.root != "" {
		roots = []string{request.root}
	}
	if s.openableFile != nil {
		return s.openableFile(roots, request.file)
	}
	return resolvePath(request.file), nil
}

// PreserveMessageImage retains the image a reply links to, once per reply and link, and names
// the retained copy. load, when given, fetches the original from the computer that holds it.
func (s *MessageImageStore) PreserveMessageImage(file, root, messageID string, load func() ([]byte, error)) (string, error) {
	request, err := messageImageRequest(file, root, messageID)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	copying, ok := s.pending[request.key]
	if !ok {
		copying = &imageCopy{done: make(chan struct{})}
		s.pending[request.key] = copying
		go func() {
			copying.file, copying.err = s.copyImage(request, load)
			s.mu.Lock()
			delete(s.pending, request.key)
			s.mu.Unlock()
			close(copying.done)
		}()
	}
	s.mu.Unlock()
	<-copying.done
	return copying.file, copying.err
}

// PreserveMessageImages retains every image a reply links to, one at a time so a reply of
// full-page captures cannot multiply peak memory use. One that fails is skipped.
func (s *MessageImageStore) PreserveMessageImages(files []string, root, messageID string) error {
	if len(files) > MaxMessageImages {
		return errors.New("Invalid image references.")
	}
	for _, file := range files {
		_, _ = s.PreserveMessageImage(file, root, messageID, nil)
	}
	return nil
}

// ReadMessageImage reads a retained image, or its preview. A host with no decoder keeps no
// previews, so a preview request falls back to the original.
func (s *MessageImageStore) ReadMessageImage(file, root, messageID string, thumbnail bool) (MessageImage, error) {
	request, err := messageImageRequest(file, root, messageID)
	if err != nil {
		return MessageImage{}, err
	}
	original, err := s.PreserveMessageImage(file, root, messageID, nil)
	if err != nil {
		return MessageImage{}, err
	}
	if thumbnail {
		bytes, err := ReadImageFile(filepath.Join(s.directory, request.key+"-thumb.png"))
		if err == nil {
			return MessageImage{Bytes: bytes, ContentType: "image/png"}, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return MessageImage{}, err
		}
	}
	bytes, err := ReadImageFile(original)
	if err != nil {
		return MessageImage{}, err
	}
	return MessageImage{Bytes: bytes, ContentType: messageImageMIME[request.extension]}, nil
}

// MessageImageBytes validates an original transferred from another computer ({ data,
// contentType } as JSON) before it is retained under the same snapshot key.
func MessageImageBytes(value jsontext.Value, file string) ([]byte, error) {
	record, ok, err := parseObject(value)
	if err != nil || !ok || !record.has("data") || !record.has("contentType") {
		return nil, errInvalidImageBytes
	}
	contentType, contentTypeOK := stringValue(record.get("contentType"))
	expected, known := messageImageMIME[imageExtension(file)]
	data, dataOK := stringValue(record.get("data"))
	if !contentTypeOK || !known || contentType != expected || !dataOK || data == "" ||
		len(data) > (MaxImageBytes+2)/3*4 || len(data)%4 != 0 || !base64Payload.MatchString(data) {
		return nil, errInvalidImageBytes
	}
	bytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		bytes = decodeBase64(data)
	}
	if len(bytes) == 0 || len(bytes) > MaxImageBytes {
		return nil, errImageUnavailable
	}
	return bytes, nil
}
