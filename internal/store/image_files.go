package store

import (
	"errors"
	"io"
	"os"
)

// MaxImageBytes bounds every image the app reads.
const MaxImageBytes = 25 * 1024 * 1024

var (
	errImageUnavailable = errors.New("Image is too large or unavailable.")
	errImageChanged     = errors.New("Image changed while reading.")
)

// ReadImageFile reads an image, bounding the read on the open handle even if the original
// changes during capture.
func ReadImageFile(file string) ([]byte, error) {
	handle, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > MaxImageBytes {
		return nil, errImageUnavailable
	}
	bytes := make([]byte, info.Size())
	if _, err := io.ReadFull(handle, bytes); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, errImageChanged
		}
		return nil, err
	}
	return bytes, nil
}
