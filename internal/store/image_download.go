package store

import (
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// SaveImageDialog is the save dialog's options.
type SaveImageDialog struct {
	Title       string
	DefaultPath string
	FilterName  string
	Extensions  []string
}

// ImageDownload is what downloading an image needs from the desktop.
type ImageDownload struct {
	Attachments *Attachments
	Images      *MessageImageStore
	// Downloads is the user's downloads folder.
	Downloads string
	// SaveDialog asks where to save; an empty path is a cancel.
	SaveDialog func(SaveImageDialog) (string, error)
	// Fetch reads a scoped attachment URL the way the attachment protocol answers it.
	Fetch func(source string) ([]byte, error)
}

var errImageGone = errors.New("This image is no longer available.")

// DownloadImage saves the same retained original the preview displays, even if its source has
// since disappeared.
func DownloadImage(download ImageDownload, source string) error {
	if !isImageSource(source) {
		return errInvalidImageReference
	}
	parsed, err := url.Parse(source)
	if err != nil {
		return err
	}
	query := parsed.Query()
	messageImage := parsed.Scheme == MessageImageScheme
	attachmentSource := !messageImage && query.Has("taskId")
	var original string
	if messageImage {
		if !query.Has("path") || !query.Has("root") || !query.Has("message") {
			return errInvalidImageReference
		}
		original, err = download.Images.PreserveMessageImage(query.Get("path"), query.Get("root"), query.Get("message"), nil)
		if err != nil {
			return err
		}
	} else {
		name, err := url.PathUnescape(strings.TrimPrefix(urlPathname(parsed), "/"))
		if err != nil {
			return err
		}
		saved, ok := download.Attachments.SavedAttachmentPath(filepath.Join(download.Attachments.Directory(), name))
		if !ok {
			return errImageGone
		}
		original = saved
	}
	// Scoped attachments use the same holder lookup as the preview, including when saved from its viewer.
	var fetched []byte
	if attachmentSource {
		if fetched, err = download.Fetch(source); err != nil {
			return errImageGone
		}
	}
	extension := strings.TrimPrefix(filepath.Ext(original), ".")
	target, err := download.SaveDialog(SaveImageDialog{
		Title:       "Save image",
		DefaultPath: filepath.Join(download.Downloads, "image."+extension),
		FilterName:  "Image",
		Extensions:  []string{extension},
	})
	if err != nil || target == "" {
		return err
	}
	if attachmentSource {
		return os.WriteFile(target, fetched, 0o666)
	}
	return copyFile(original, target)
}

func copyFile(from, to string) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		return err
	}
	return target.Close()
}
