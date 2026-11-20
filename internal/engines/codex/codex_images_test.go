package codex

import (
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var png = []byte{137, 80, 78, 71, 13, 10, 26, 10}

func imageItem(t *testing.T, fields string) protocol.ThreadItem {
	t.Helper()
	var item protocol.ThreadItem
	must(t, jsonx.Unmarshal([]byte(`{"type":"imageGeneration","id":"image-1","status":"completed","revisedPrompt":"Pixel art cat","result":"","failure":null,"generationId":null`+fields+`}`), &item))
	return item
}

var savedImage = regexp.MustCompile(`^\[([^\]]*)\]\(<([^>]*)>\)$`)

// The label and file a saved image's Markdown names.
func savedFile(t *testing.T, text string) (string, string) {
	t.Helper()
	match := savedImage.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("no image in %q", text)
	}
	segments := strings.Split(match[2], "/")
	for index, segment := range segments {
		decoded, err := url.PathUnescape(segment)
		must(t, err)
		segments[index] = decoded
	}
	return match[1], strings.Join(segments, "/")
}

func TestANativeImageIsCopiedBeforeItsPreviewIsPublished(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "cat [1] (100%) #1.png")
	must(t, os.WriteFile(file, png, 0o644))
	text, err := CodexImageOutput(imageItem(t, `,"savedPath":`+string(encodedOf(file))), root, root)
	must(t, err)
	label, saved := savedFile(t, text)
	if label != "Generated image" || saved == file {
		t.Fatalf("label %q, saved %q", label, saved)
	}
	must(t, os.Remove(file))
	if got := readText(t, saved); got != string(png) {
		t.Fatal("the copy outlives the original")
	}
}

func TestImageDataSuppliesASavedPreviewWhenCodexOmitsItsPathOrTheFileIsUnavailable(t *testing.T) {
	root := t.TempDir()
	payload := base64.StdEncoding.EncodeToString(png)
	for _, savedPath := range []string{"", `,"savedPath":` + string(encodedOf(filepath.Join(root, "missing.png")))} {
		var item protocol.ThreadItem
		must(t, jsonx.Unmarshal([]byte(`{"type":"imageGeneration","id":"image-1","status":"completed","revisedPrompt":"Pixel art cat","failure":null,"generationId":null,"result":"`+payload+`"`+savedPath+`}`), &item))
		text, err := CodexImageOutput(item, root, root)
		must(t, err)
		_, saved := savedFile(t, text)
		if readText(t, saved) != string(png) {
			t.Fatal("the image data is saved")
		}
	}
}

func TestAnUploadedImagePreviewsFromItsSavedFileAndReportsNoImageWithoutOne(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "cat.png")
	must(t, os.WriteFile(file, png, 0o644))
	uploaded := func(savedPath string) protocol.ThreadItem {
		var item protocol.ThreadItem
		fields := ""
		if savedPath != "" {
			fields = `,"savedPath":` + string(encodedOf(savedPath))
		}
		must(t, jsonx.Unmarshal([]byte(`{"type":"imageGeneration","id":"image-1","status":"completed","revisedPrompt":null,"failure":null,"generationId":null,"fileId":"file-1"`+fields+`}`), &item))
		return item
	}
	text, err := CodexImageOutput(uploaded(file), root, root)
	must(t, err)
	if _, saved := savedFile(t, text); readText(t, saved) != string(png) {
		t.Fatal("the uploaded file is saved")
	}
	if text, _ := CodexImageOutput(uploaded(""), root, root); !strings.Contains(text, "without returning an image") {
		t.Fatalf("text = %q", text)
	}
	if _, err := CodexImageOutput(uploaded(filepath.Join(root, "missing.png")), root, root); err == nil {
		t.Fatal("a missing upload is an error")
	}
}

func TestFailedCancelledAndEmptyGenerationsReportWhatHappened(t *testing.T) {
	root := t.TempDir()
	for fields, want := range map[string]string{
		`,"failure":{"type":"usageLimitExceeded","limitId":"imagegen","resetsAt":null}`: "usage limit",
		`,"status":"failed"`:    "generation failed",
		`,"status":"cancelled"`: "cancelled",
		``:                      "without returning an image",
	} {
		text, err := CodexImageOutput(imageItem(t, fields), root, root)
		if err != nil || !strings.Contains(text, want) {
			t.Errorf("%s gave %q, %v", fields, text, err)
		}
	}
	if _, err := CodexImageOutput(imageItem(t, `,"result":"not base64"`), root, root); err == nil {
		t.Fatal("malformed image data is rejected")
	}
	if _, err := CodexImageOutput(imageItem(t, `,"result":"`+base64.StdEncoding.EncodeToString(png)+`"`), root, ""); err == nil || err.Error() != "Generated image storage is unavailable." {
		t.Fatalf("err = %v", err)
	}
}

func TestEncodeURIComponentMatchesJavaScript(t *testing.T) {
	if got := encodeURIComponent("cat [1] (100%) #1 é.png"); got != "cat%20%5B1%5D%20(100%25)%20%231%20%C3%A9.png" {
		t.Fatalf("got %s", got)
	}
}
