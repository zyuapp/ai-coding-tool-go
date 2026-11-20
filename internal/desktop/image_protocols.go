package desktop

import (
	"net/http"
	"net/url"
	"strings"
)

// The window's own schemes (`attachment:`, `message-image:`, `aicodingtool-visual:`) are served
// under these paths of the page's origin; the renderer's bridge rewrites each scheme URL to its path.
// None ends in "/", which the asset server would take for a page and inject its runtime into.
const (
	AttachmentPath   = "/__app/attachment/"
	MessageImagePath = "/__app/message-image"
	VisualFramePath  = "/__app/visual-frame"
)

// appSchemes serves the schemes, and passes every other request to next.
func appSchemes(images ImageReads, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path := request.URL.Path
		switch {
		case strings.HasPrefix(path, AttachmentPath):
			attachmentResponse(writer, request, images)
		case path == MessageImagePath:
			messageImageResponse(writer, request, images)
		case path == VisualFramePath:
			visualFrameResponse(writer)
		default:
			next.ServeHTTP(writer, request)
		}
	})
}

func notFound(writer http.ResponseWriter, message string) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusNotFound)
	_, _ = writer.Write([]byte(message))
}

// attachmentResponse resolves from the addressed thread, so selecting another thread cannot change an open image.
func attachmentResponse(writer http.ResponseWriter, request *http.Request, images ImageReads) {
	name, err := url.PathUnescape(strings.TrimPrefix(request.URL.EscapedPath(), AttachmentPath))
	if err != nil || images == nil {
		notFound(writer, "Attachment is unavailable")
		return
	}
	bytes, err := images.Attachment(request.Context(), name, request.URL.Query().Get("taskId"))
	if err != nil {
		notFound(writer, "Attachment is unavailable")
		return
	}
	writer.Header().Set("Content-Type", "image/png")
	_, _ = writer.Write(bytes)
}

// messageImageResponse retains the holder's original once, for both previews and the full-size viewer.
func messageImageResponse(writer http.ResponseWriter, request *http.Request, images ImageReads) {
	if images == nil {
		notFound(writer, "Image unavailable")
		return
	}
	params := request.URL.Query()
	query := MessageImageQuery{Path: params.Get("path"), Root: params.Get("root"), Message: params.Get("message"), Thumbnail: params.Get("thumbnail") == "1"}
	bytes, contentType, err := images.MessageImage(request.Context(), query, params.Get("taskId"))
	if err != nil {
		notFound(writer, "Image unavailable")
		return
	}
	header := writer.Header()
	header.Set("Content-Type", contentType)
	header.Set("Cache-Control", "private, max-age=31536000, immutable")
	header.Set("X-Content-Type-Options", "nosniff")
	_, _ = writer.Write(bytes)
}

// visualFrameResponse serves the one page the scheme has; nothing a URL names is ever read.
func visualFrameResponse(writer http.ResponseWriter) {
	header := writer.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Content-Security-Policy", visualFrameCSP)
	header.Set("X-Content-Type-Options", "nosniff")
	_, _ = writer.Write([]byte(visualFrameDocument))
}
