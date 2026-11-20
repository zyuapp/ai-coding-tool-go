package desktop

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

type fakeImages struct {
	name, thread string
	query        contracts.ComputerQueryMessageImage
}

func (images *fakeImages) Attachment(ctx context.Context, name, thread string) ([]byte, error) {
	images.name, images.thread = name, thread
	if name == "missing.png" {
		return nil, errors.New("gone")
	}
	return []byte("png"), nil
}

func (images *fakeImages) MessageImage(ctx context.Context, query contracts.ComputerQueryMessageImage, thread string) ([]byte, string, error) {
	images.query, images.thread = query, thread
	return []byte("jpeg"), "image/jpeg", nil
}

func serve(handler http.Handler, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func TestAppSchemes(t *testing.T) {
	images := &fakeImages{}
	passed := false
	handler := appSchemes(images, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { passed = true }))

	response := serve(handler, "/__app/attachment/a%20b-1.png?taskId=t1")
	if response.Code != 200 || images.name != "a b-1.png" || images.thread != "t1" || response.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("attachment: %d %q %q", response.Code, images.name, images.thread)
	}
	if response := serve(handler, "/__app/attachment/missing.png"); response.Code != 404 || response.Body.String() != "Attachment is unavailable" {
		t.Fatalf("missing attachment: %d %q", response.Code, response.Body.String())
	}

	response = serve(handler, "/__app/message-image?path=img.png&root=%2Frepo&message=m1&thumbnail=1&taskId=t2")
	got := images.query
	if response.Code != 200 || got.Path != "img.png" || got.Root != "/repo" || got.Message != "m1" || got.Thumbnail == nil || !*got.Thumbnail || images.thread != "t2" || response.Header().Get("Content-Type") != "image/jpeg" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("message image: %d %+v %q", response.Code, images.query, images.thread)
	}

	response = serve(handler, "/__app/visual-frame")
	if response.Code != 200 || !strings.HasPrefix(response.Header().Get("Content-Security-Policy"), "default-src 'none'") || !strings.Contains(response.Body.String(), "aicodingtool-visual:render") {
		t.Fatalf("visual frame: %d", response.Code)
	}
	if serve(handler, "/__app/visual-frame/other"); !passed {
		t.Fatal("another path was not passed on")
	}
}
