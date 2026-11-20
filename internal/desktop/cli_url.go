package desktop

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// CLIURLScheme is the scheme the `aic` command opens folders through.
const CLIURLScheme = "aicodingtool"

var encodedPath = regexp.MustCompile(`^[A-Za-z0-9\-_=]+$`)

// ProjectPathFromURL is the folder an `aicodingtool://open?path=` URL names, or "" when the URL is
// not one the command wrote.
func ProjectPathFromURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != CLIURLScheme || parsed.Host != "open" {
		return ""
	}
	encoded := parsed.Query().Get("path")
	if encoded == "" || len(encoded) > 8_192 || !encodedPath.MatchString(encoded) {
		return ""
	}
	standard := strings.NewReplacer("-", "+", "_", "/").Replace(encoded)
	trimmed := strings.TrimRight(standard, "=")
	if len(standard)-len(trimmed) > 2 || len(trimmed)%4 == 1 || strings.Contains(trimmed, "=") {
		return ""
	}
	if len(standard) != len(trimmed) && len(standard)%4 != 0 {
		return ""
	}
	binary, err := base64.RawStdEncoding.DecodeString(trimmed)
	if err != nil {
		return ""
	}
	decoded := decodeUTF8(binary)
	if !strings.HasPrefix(decoded, "/") || strings.Contains(decoded, "\x00") {
		return ""
	}
	return decoded
}

// decodeUTF8 decodes as TextDecoder does, with U+FFFD for each byte that is not UTF-8.
func decodeUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var out strings.Builder
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		out.WriteRune(r)
		data = data[size:]
	}
	return out.String()
}

// ProjectPathFromArgv finds the folder a launch argument names; Linux delivers the URL that way.
func ProjectPathFromArgv(argv []string) string {
	for _, argument := range argv {
		if !strings.HasPrefix(argument, CLIURLScheme+"://") {
			continue
		}
		if root := ProjectPathFromURL(argument); root != "" {
			return root
		}
	}
	return ""
}
