package platform

import (
	"context"
	"errors"
	"strings"
	"time"
)

const openTimeout = 30 * time.Second

func runOpen(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	_, stderr, err := Run(ctx, "/usr/bin/open", args, RunOptions{})
	if err != nil {
		if message := strings.TrimSpace(string(stderr)); message != "" {
			return errors.New(message)
		}
	}
	return err
}

// OpenExternal opens a URL with the desktop's own handler.
func OpenExternal(url string) error {
	return runOpen(url)
}

// OpenPath opens a file or folder with the desktop's own handler, answering an error message, or empty on success.
func OpenPath(path string) string {
	if runOpen(path) != nil {
		return "Failed to open path"
	}
	return ""
}

// ShowItemInFolder shows a file selected in the Finder.
func ShowItemInFolder(path string) {
	go func() { _ = runOpen("-R", path) }()
}
