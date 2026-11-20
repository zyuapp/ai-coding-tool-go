package store

import (
	"net/http"
	"net/url"
)

// ImageReads is the part of the computer reads (main/computer-queries.ts) the image protocols use:
// each read is answered by the computer holding thread, or by this one when thread is nil.
type ImageReads interface {
	ReadAttachment(name string, thread *string) ([]byte, error)
	ReadMessageImage(query MessageImageQuery, thread *string) (MessageImage, error)
}

// MessageImageQuery is contracts' { kind: "message-image" } computer query.
type MessageImageQuery struct {
	Path      string
	Root      string
	Message   string
	Thumbnail bool
}

// ImageResponse is what an image protocol answers a request with.
type ImageResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// Write sends the response.
func (r ImageResponse) Write(writer http.ResponseWriter) {
	for name, values := range r.Header {
		writer.Header()[name] = values
	}
	writer.WriteHeader(r.Status)
	writer.Write(r.Body)
}

func textResponse(status int, body string) ImageResponse {
	return ImageResponse{Status: status, Header: http.Header{"Content-Type": {"text/plain;charset=UTF-8"}}, Body: []byte(body)}
}

// AttachmentResponse answers an attachment:// request from the addressed thread, so selecting
// another thread cannot change an open image.
func AttachmentResponse(source string, reads ImageReads) ImageResponse {
	parsed, err := url.Parse(source)
	if err != nil {
		return textResponse(http.StatusNotFound, "Attachment is unavailable")
	}
	name, err := url.PathUnescape(urlPathname(parsed)[1:])
	if err != nil {
		return textResponse(http.StatusNotFound, "Attachment is unavailable")
	}
	bytes, err := reads.ReadAttachment(name, queryParam(parsed, "taskId"))
	if err != nil {
		return textResponse(http.StatusNotFound, "Attachment is unavailable")
	}
	return ImageResponse{Status: http.StatusOK, Header: http.Header{"Content-Type": {"image/png"}}, Body: bytes}
}

// urlPathname is the WHATWG URL pathname: still percent-encoded, "/" when empty.
func urlPathname(parsed *url.URL) string {
	path := parsed.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

// queryParam is URLSearchParams.get: the first value, or nil.
func queryParam(parsed *url.URL, name string) *string {
	values, ok := parsed.Query()[name]
	if !ok || len(values) == 0 {
		return nil
	}
	return &values[0]
}
