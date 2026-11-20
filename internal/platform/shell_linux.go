package platform

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/godbus/dbus/v5"
)

const openTimeout = 30 * time.Second

func xdgOpen(target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	_, _, err := Run(ctx, "xdg-open", []string{target}, RunOptions{})
	return err
}

// OpenExternal opens a URL with the desktop's own handler.
func OpenExternal(target string) error {
	return xdgOpen(target)
}

// OpenPath opens a file or folder with the desktop's own handler, answering an error message, or empty on success.
func OpenPath(path string) string {
	if xdgOpen(path) != nil {
		return "Failed to open path"
	}
	return ""
}

// ShowItemInFolder asks the file manager to select the item, and opens its folder when none answers.
func ShowItemInFolder(path string) {
	go func() {
		if showItemOverDBus(path) == nil {
			return
		}
		folder := path
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			folder = filepath.Dir(path)
		}
		_ = xdgOpen(folder)
	}()
}

func showItemOverDBus(path string) error {
	connection, err := dbus.SessionBus()
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	item := (&url.URL{Scheme: "file", Path: absolute}).String()
	call := connection.Object("org.freedesktop.FileManager1", "/org/freedesktop/FileManager1").Call("org.freedesktop.FileManager1.ShowItems", 0, []string{item}, "")
	return call.Err
}

// TrashItem moves a file or folder to the desktop's trash, through the first trash tool installed.
func TrashItem(path string) error {
	attempts := [][]string{{"gio", "trash", path}, {"kioclient5", "move", path, "trash:/"}, {"kioclient", "move", path, "trash:/"}, {"gvfs-trash", path}}
	var failures []error
	for _, attempt := range attempts {
		ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
		_, _, err := Run(ctx, attempt[0], attempt[1:], RunOptions{})
		cancel()
		if err == nil {
			return nil
		}
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
