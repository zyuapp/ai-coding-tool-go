package domain

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// FileHref is the file a link names, and the line to open it at when the link carried one.
type FileHref struct {
	File string   `json:"file"`
	Line *float64 `json:"line"`
}

var threadHref = regexp.MustCompile(`(?i)^aicodingtool://thread/([^/?#]+)$`)
var lineSuffix = regexp.MustCompile(`:\d+(?::\d+)?$`)
var urlScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z\d+.-]*:`)
var windowsPath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// fileUrl matches a `file://` prefix before the path's leading slash, captured as $1 so a replace keeps it.
var fileUrl = regexp.MustCompile(`(?i)^file://(?:localhost)?(/)`)

// fileUrlDrive matches the slash before a drive letter, which a replace with $1 drops.
var fileUrlDrive = regexp.MustCompile(`^/([A-Za-z]:/)`)

// ParseThreadHref is the thread id an `aicodingtool://thread/<id>` link names.
func ParseThreadHref(href string) (string, bool) {
	match := threadHref.FindStringSubmatch(href)
	if match == nil {
		return "", false
	}
	return match[1], true
}

func ParseFileHref(href string) *FileHref {
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "//") {
		return nil
	}
	raw := href
	if fileUrl.MatchString(href) {
		raw = fileUrlDrive.ReplaceAllString(fileUrl.ReplaceAllString(href, "$1"), "$1")
	}
	decoded, ok := decodeURIComponent(raw)
	if !ok {
		return nil
	}
	file := decoded
	suffix := lineSuffix.FindStringIndex(decoded)
	if suffix != nil {
		file = decoded[:suffix[0]]
	}
	if file == "" || strings.HasSuffix(file, "/") || urlScheme.MatchString(file) && !windowsPath.MatchString(file) {
		return nil
	}
	var line float64
	if suffix != nil {
		digits, _, _ := strings.Cut(decoded[suffix[0]+1:suffix[1]], ":")
		line, _ = strconv.ParseFloat(digits, 64)
	}
	if line > 0 {
		return &FileHref{File: file, Line: &line}
	}
	return &FileHref{File: file}
}

// decodeURIComponent is JavaScript's decodeURIComponent; false where it throws.
func decodeURIComponent(value string) (string, bool) {
	if !strings.Contains(value, "%") {
		return value, true
	}
	var out strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			out.WriteByte(value[index])
			continue
		}
		if index+2 >= len(value) {
			return "", false
		}
		decoded, err := strconv.ParseUint(value[index+1:index+3], 16, 8)
		if err != nil {
			return "", false
		}
		out.WriteByte(byte(decoded))
		index += 2
	}
	result := out.String()
	return result, utf8.ValidString(result)
}

// IsAbsoluteFilePath reports whether a written path names a place on its own.
func IsAbsoluteFilePath(file string) bool {
	return strings.HasPrefix(file, "/") || strings.HasPrefix(file, "~/") || windowsPath.MatchString(file)
}
