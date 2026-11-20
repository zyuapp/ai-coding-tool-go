package store

import (
	"bytes"
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"
	"image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

func pngOf(t *testing.T, width, height int) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewNRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func messageImageURL(file, root, message string, thumbnail bool, taskID string) string {
	query := url.Values{"path": {file}, "root": {root}, "message": {message}}
	if thumbnail {
		query.Set("thumbnail", "1")
	}
	if taskID != "" {
		query.Set("taskId", taskID)
	}
	return "message-image://file/?" + query.Encode()
}

func attachmentURL(file, taskID string) string {
	source := "attachment://file/" + url.PathEscape(attachmentName(file))
	if taskID != "" {
		source += "?taskId=" + url.QueryEscape(taskID)
	}
	return source
}

// localReads answers every read on this computer, as computer-queries does for a local thread.
type localReads struct {
	attachments *Attachments
	images      *MessageImageStore
}

func (r localReads) ReadAttachment(name string, _ *string) ([]byte, error) {
	data, err := r.attachments.ReadSavedAttachment(name)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(data)
}

func (r localReads) ReadMessageImage(query contracts.ComputerQueryMessageImage, _ *string) (MessageImage, error) {
	return r.images.ReadMessageImage(query.Path, query.Root, query.Message, query.Thumbnail != nil && *query.Thumbnail)
}

func TestReferencedImagesSurviveDeletionAndARestart(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "shot with spaces 100%.png")
	original := pngOf(t, 1600, 1200)
	os.WriteFile(file, original, 0o644)
	store := NewMessageImageStore(filepath.Join(root, "message-images"), MessageThumbnail, nil)
	var wait sync.WaitGroup
	results := make([]string, 2)
	for i := range results {
		wait.Go(func() {
			saved, err := store.PreserveMessageImage(file, "", "reply-1", nil)
			if err != nil {
				t.Error(err)
			}
			results[i] = saved
		})
	}
	wait.Wait()
	if results[0] != results[1] {
		t.Fatalf("one copy per reply and link: %v", results)
	}
	if saved, _ := os.ReadFile(results[0]); !bytes.Equal(saved, original) {
		t.Fatal("the copy is the original's bytes")
	}
	os.Remove(file)

	restarted := NewMessageImageStore(filepath.Join(root, "message-images"), MessageThumbnail, nil)
	reads := localReads{images: restarted}
	full := MessageImageResponse(messageImageURL(file, "", "reply-1", false, ""), reads)
	if full.Status != 200 || !bytes.Equal(full.Body, original) || full.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("full %d %s", full.Status, full.Header)
	}
	preview := MessageImageResponse(messageImageURL(file, "", "reply-1", true, ""), reads)
	config, err := png.DecodeConfig(bytes.NewReader(preview.Body))
	if err != nil || config.Width != 480 || config.Height != 360 {
		t.Fatalf("preview %v %v", config, err)
	}
	if response := MessageImageResponse(messageImageURL(file, "", "reply-2", false, ""), reads); response.Status != 404 {
		t.Fatalf("another reply has no copy of a deleted file: %d", response.Status)
	}
	changed := pngOf(t, 2, 2)
	os.WriteFile(file, changed, 0o644)
	later, err := restarted.PreserveMessageImage(file, "", "reply-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if saved, _ := os.ReadFile(later); !bytes.Equal(saved, changed) {
		t.Fatal("a later reply captures its own version")
	}
	if saved, _ := os.ReadFile(results[0]); !bytes.Equal(saved, original) {
		t.Fatal("and the earlier copy stays")
	}
}

func TestUnavailableImagesFailWithoutBlockingOthers(t *testing.T) {
	root := t.TempDir()
	store := NewMessageImageStore(filepath.Join(root, "message-images"), MessageThumbnail, nil)
	reads := localReads{images: store}
	invalid := filepath.Join(root, "text.png")
	os.WriteFile(invalid, []byte("not an image"), 0o644)
	for _, source := range []string{messageImageURL(invalid, "", "reply", false, ""), messageImageURL("/etc/passwd", "", "reply", false, "")} {
		if response := MessageImageResponse(source, reads); response.Status != 404 {
			t.Fatalf("%s answered %d", source, response.Status)
		}
	}
	if _, err := store.PreserveMessageImage("https://example.com/shot.png", "", "reply", nil); err == nil || err.Error() != "Invalid image reference." {
		t.Fatalf("err %v", err)
	}
	good := filepath.Join(root, "good.png")
	os.WriteFile(good, pngOf(t, 4, 4), 0o644)
	if err := store.PreserveMessageImages([]string{invalid, good}, "", "reply"); err != nil {
		t.Fatal(err)
	}
	os.Remove(good)
	if response := MessageImageResponse(messageImageURL(good, "", "reply", false, ""), reads); response.Status != 200 {
		t.Fatalf("good %d", response.Status)
	}
}

func TestRemoteOriginalsAreRetainedOnceAndValidated(t *testing.T) {
	root := t.TempDir()
	store := NewMessageImageStore(filepath.Join(root, "store"), func([]byte) ([]byte, error) { return []byte("preview"), nil }, nil)
	file := filepath.Join(root, "shot.jpg")
	os.WriteFile(file, []byte("different local image"), 0o644)
	jpeg := []byte{255, 216, 255, 1}
	asks := 0
	load := func() ([]byte, error) {
		asks++
		return MessageImageBytes(jsontext.Value(`{"data":"`+base64.StdEncoding.EncodeToString(jpeg)+`","contentType":"image/jpeg"}`), file)
	}
	saved, err := store.PreserveMessageImage(file, "/linux", "reply", load)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := store.PreserveMessageImage(file, "/linux", "reply", load); again != saved || asks != 1 {
		t.Fatalf("asked %d times", asks)
	}
	image, err := store.ReadMessageImage(file, "/linux", "reply", false)
	if err != nil || !bytes.Equal(image.Bytes, jpeg) || image.ContentType != "image/jpeg" {
		t.Fatalf("image %v %v", image, err)
	}
	if preview, _ := store.ReadMessageImage(file, "/linux", "reply", true); string(preview.Bytes) != "preview" || preview.ContentType != "image/png" {
		t.Fatalf("preview %q", preview.Bytes)
	}
	png := filepath.Join(root, "shot.png")
	for _, value := range []string{`null`, `{}`, `{"data":"","contentType":"image/png"}`, `{"data":"!!!!","contentType":"image/png"}`,
		`{"data":"YQ==","contentType":"text/html"}`, `{"data":"YQ==","contentType":"image/jpeg"}`,
		`{"data":"` + base64.StdEncoding.EncodeToString(make([]byte, MaxImageBytes+1)) + `","contentType":"image/png"}`,
		`{"data":"` + strings.Repeat("A", (MaxImageBytes+2)/3*4+4) + `","contentType":"image/png"}`,
	} {
		if _, err := MessageImageBytes(jsontext.Value(value), png); err == nil {
			t.Fatalf("accepted %.60s", value)
		}
	}
	if decoded, err := MessageImageBytes(jsontext.Value(`{"data":"YQ==","contentType":"image/png"}`), png); err != nil || string(decoded) != "a" {
		t.Fatalf("decoded %q %v", decoded, err)
	}
	failing := func() ([]byte, error) { return nil, errors.New("Offline") }
	if _, err := store.PreserveMessageImage(png, "/linux", "remote", failing); err == nil {
		t.Fatal("a failed transfer fails")
	}
	if _, err := store.PreserveMessageImage(png, "/linux", "remote", func() ([]byte, error) { return []byte("a"), nil }); err != nil {
		t.Fatalf("a failed transfer leaves no entry that would prevent a retry: %v", err)
	}
}

func TestAttachmentsAreWrittenAndReadOnlyByTheirOwnNames(t *testing.T) {
	root := t.TempDir()
	attachments := NewAttachments(root)
	original := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	context := `{"version":1,"platform":"macos","app":"Notes","title":"Draft","capturedAt":5,"accessibility":{"status":"unavailable","reason":"permission"}}`
	file, err := attachments.WriteAttachment(strings.TrimRight(base64.StdEncoding.EncodeToString(original), "="), jsontext.Value(context))
	if err != nil {
		t.Fatal(err)
	}
	if saved, ok := attachments.SavedAttachmentPath(file); !ok || saved != file {
		t.Fatalf("saved %q %v", saved, ok)
	}
	data, err := attachments.ReadSavedAttachment(filepath.Base(file))
	if err != nil || data != base64.StdEncoding.EncodeToString(original) {
		t.Fatalf("read %q %v", data, err)
	}
	if got := attachments.ReadAttachmentContext(file); string(got) != context {
		t.Fatalf("context %s", got)
	}
	for _, name := range []string{"../x.png", "x.jpg", ""} {
		if _, err := attachments.ReadSavedAttachment(name); err == nil {
			t.Fatalf("read %q", name)
		}
	}
	if _, ok := attachments.SavedAttachmentPath(filepath.Join(root, filepath.Base(file))); ok {
		t.Fatal("a file of the same name outside the folder is not an attachment")
	}
	for _, payload := range []string{"", "not base64!", strings.Repeat("A", MaxAttachmentEncodedBytes+4)} {
		if _, err := attachments.WriteAttachment(payload, nil); err == nil {
			t.Fatalf("wrote %.20q", payload)
		}
	}
	response := AttachmentResponse(attachmentURL(file, ""), localReads{attachments: attachments})
	if response.Status != 200 || !bytes.Equal(response.Body, original) {
		t.Fatalf("response %d", response.Status)
	}
	if response := AttachmentResponse("attachment://file/%2E%2E%2Fx.png", localReads{attachments: attachments}); response.Status != 404 {
		t.Fatalf("response %d", response.Status)
	}
}

func TestDownloadsSaveTheRetainedOriginal(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "download.png")
	var dialogs []SaveImageDialog
	var fetched []string
	canceled := false
	download := ImageDownload{
		Attachments: NewAttachments(root),
		Images:      NewMessageImageStore(filepath.Join(root, "message-images"), MessageThumbnail, nil),
		Downloads:   root,
		SaveDialog: func(options SaveImageDialog) (string, error) {
			dialogs = append(dialogs, options)
			if canceled {
				return "", nil
			}
			return destination, nil
		},
		Fetch: func(source string) ([]byte, error) {
			fetched = append(fetched, source)
			return []byte("remote bytes"), nil
		},
	}
	original := pngOf(t, 2, 2)
	file := filepath.Join(root, "generated.png")
	os.WriteFile(file, original, 0o644)
	if _, err := download.Images.PreserveMessageImage(file, "", "image-reply", nil); err != nil {
		t.Fatal(err)
	}
	os.Remove(file)
	if err := DownloadImage(download, messageImageURL(file, "", "image-reply", true, "")); err != nil {
		t.Fatal(err)
	}
	if saved, _ := os.ReadFile(destination); !bytes.Equal(saved, original) {
		t.Fatal("the retained original is saved after its source disappears")
	}
	if dialogs[0].DefaultPath != filepath.Join(root, "image.png") || dialogs[0].Extensions[0] != "png" {
		t.Fatalf("dialog %+v", dialogs[0])
	}

	attachment, _ := download.Attachments.WriteAttachment(base64.StdEncoding.EncodeToString(original), nil)
	os.WriteFile(destination, []byte("keep this file"), 0o644)
	canceled = true
	if err := DownloadImage(download, attachmentURL(attachment, "")); err != nil {
		t.Fatal(err)
	}
	if saved, _ := os.ReadFile(destination); string(saved) != "keep this file" {
		t.Fatal("cancelling does not replace a selected file")
	}
	canceled = false
	dialogs = nil
	for _, source := range []string{"https://example.com/image.png", "file:///etc/passwd", "attachment://file/%2E%2E%2Fimage.png"} {
		if err := DownloadImage(download, source); err == nil {
			t.Fatalf("downloaded %s", source)
		}
	}
	if len(dialogs) != 0 {
		t.Fatal("a refused source asks nothing")
	}
	remote := attachmentURL("/linux/attachments/remote.png", "remote-thread")
	if err := DownloadImage(download, remote); err != nil {
		t.Fatal(err)
	}
	if saved, _ := os.ReadFile(destination); string(saved) != "remote bytes" || len(fetched) != 1 || fetched[0] != remote {
		t.Fatalf("a remote attachment reads the same scoped URL as the viewer: %q %v", saved, fetched)
	}
}
