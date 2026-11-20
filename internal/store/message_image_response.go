package store

import (
	"net/http"
	"net/url"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// MessageImageResponse answers a message-image:// request, retaining the holder's original once
// for both previews and the full-size viewer.
func MessageImageResponse(source string, reads ImageReads) ImageResponse {
	parsed, err := url.Parse(source)
	if err != nil {
		return textResponse(http.StatusNotFound, "Image unavailable")
	}
	param := func(name string) string {
		if value := queryParam(parsed, name); value != nil {
			return *value
		}
		return ""
	}
	query := contracts.ComputerQueryMessageImage{Path: param("path"), Root: param("root"), Message: param("message"), Thumbnail: new(param("thumbnail") == "1")}
	image, err := reads.ReadMessageImage(query, queryParam(parsed, "taskId"))
	if err != nil {
		return textResponse(http.StatusNotFound, "Image unavailable")
	}
	return ImageResponse{Status: http.StatusOK, Body: image.Bytes, Header: http.Header{
		"Content-Type":           {image.ContentType},
		"Cache-Control":          {"private, max-age=31536000, immutable"},
		"X-Content-Type-Options": {"nosniff"},
	}}
}
