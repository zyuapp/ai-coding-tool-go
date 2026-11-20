package browser

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
)

// maxShots is how many pictures the folder keeps. A new one past this deletes the oldest, so a long
// run is bounded.
const maxShots = 20

// shots is where pictures of pages are kept: a folder made on the first capture and removed with the host.
type shots struct {
	home  string
	files []string
}

func (s *shots) directory() (string, error) {
	if s.home != "" {
		if info, err := os.Stat(s.home); err == nil && info.IsDir() {
			return s.home, nil
		}
	}
	home, err := os.MkdirTemp("", "aicodingtool-shots-")
	if err != nil {
		return "", err
	}
	s.home = home
	return home, nil
}

// write keeps the folder bounded. A caller that has yet to open its file still has the newest ones.
func (s *shots) write(bytes []byte) (string, error) {
	home, err := s.directory()
	if err != nil {
		return "", err
	}
	file := filepath.Join(home, application.NewID()+".png")
	if err := os.WriteFile(file, bytes, 0o600); err != nil {
		return "", err
	}
	s.files = append(s.files, file)
	for len(s.files) > maxShots {
		_ = os.Remove(s.files[0])
		s.files = s.files[1:]
	}
	return file, nil
}

// discard removes the pictures: they only serve the run that asked for them, so none outlives the
// window they were taken in.
func (s *shots) discard() {
	s.files = nil
	if s.home != "" {
		_ = os.RemoveAll(s.home)
		s.home = ""
	}
}

// pngSize is what a PNG really measures, read from its header; on a dense screen a multiple of the
// page's own size.
func pngSize(bytes []byte) (width, height uint32) {
	if len(bytes) < 24 {
		return 0, 0
	}
	return binary.BigEndian.Uint32(bytes[16:20]), binary.BigEndian.Uint32(bytes[20:24])
}
