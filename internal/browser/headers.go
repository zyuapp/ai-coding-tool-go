package browser

import (
	"net/url"
	"os"
	"strings"
)

// ChromeIdentity is how a Chromium page presents itself to a site. Electron's own headers
// contradict each other: the user agent names Electron, and replacing only that string leaves
// `Sec-CH-UA` announcing Chromium at a different version with no Google Chrome brand, which is a
// pair no real browser sends.
type ChromeIdentity struct {
	UserAgent      string
	AcceptLanguage string
	Hints          map[string]string
}

// lowEntropyHints are the hints Chrome puts on every secure request. The rest go out only where a
// site asked for them.
var lowEntropyHints = map[string]bool{"sec-ch-ua": true, "sec-ch-ua-mobile": true, "sec-ch-ua-platform": true}

const greaseBrand = "Not?A_Brand"

// ChromeIdentityFor is the identity of the Chrome at chromeVersion.
func ChromeIdentityFor(chromeVersion string) ChromeIdentity {
	major, _, _ := strings.Cut(chromeVersion, ".")
	brands := func(version, grease string) string {
		return `"Chromium";v="` + version + `", "Google Chrome";v="` + version + `", "` + greaseBrand + `";v="` + grease + `"`
	}
	return ChromeIdentity{
		UserAgent:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + major + ".0.0.0 Safari/537.36",
		AcceptLanguage: "en-US,en;q=0.9",
		Hints: map[string]string{
			"Sec-CH-UA":                   brands(major, "24"),
			"Sec-CH-UA-Mobile":            "?0",
			"Sec-CH-UA-Platform":          `"macOS"`,
			"Sec-CH-UA-Full-Version-List": brands(chromeVersion, "24.0.0.0"),
		},
	}
}

func dropHeader(headers map[string]string, name string) {
	for key := range headers {
		if strings.EqualFold(key, name) {
			delete(headers, key)
		}
	}
}

func hasHeader(headers map[string]string, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func setHeader(headers map[string]string, name, value string) {
	dropHeader(headers, name)
	headers[name] = value
}

// secureURL: client hints ride secure requests only, so http keeps whatever the engine decided to send.
func secureURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && strings.EqualFold(parsed.Scheme, "https")
}

// ChromeHeaders is the request as identity would make it: hints that agree with the user agent,
// and its language.
func ChromeHeaders(rawURL string, requestHeaders map[string]string, identity ChromeIdentity) map[string]string {
	headers := make(map[string]string, len(requestHeaders)+len(identity.Hints)+1)
	for key, value := range requestHeaders {
		headers[key] = value
	}
	setHeader(headers, "Accept-Language", identity.AcceptLanguage)
	if !secureURL(rawURL) {
		return headers
	}
	for name, value := range identity.Hints {
		if lowEntropyHints[strings.ToLower(name)] || hasHeader(headers, name) {
			setHeader(headers, name, value)
		}
	}
	return headers
}

const fallbackSafariVersion = "18.0"

// SafariVersion is `Version/x.y` as Safari states it, from the major and minor of the bundle
// version in its Info.plist.
func SafariVersion(infoPlist string) (string, bool) {
	at := strings.Index(infoPlist, "<key>CFBundleShortVersionString</key>")
	if at < 0 {
		return "", false
	}
	rest := infoPlist[at:]
	start := strings.Index(rest, "<string>")
	if start < 0 {
		return "", false
	}
	rest = rest[start+len("<string>"):]
	end := strings.Index(rest, "</string>")
	if end < 0 {
		return "", false
	}
	parts := strings.Split(strings.TrimSpace(rest[:end]), ".")
	digits := func(part string) bool {
		return !strings.ContainsFunc(part, func(r rune) bool { return r < '0' || r > '9' })
	}
	if parts[0] == "" || !digits(parts[0]) {
		return "", false
	}
	minor := "0"
	if len(parts) > 1 && digits(parts[1]) {
		minor = parts[1]
	}
	return parts[0] + "." + minor, true
}

// SafariUserAgent is what the WebKit pages send. Sites refuse sign-in from a user agent that names
// an embedded web view, and WebKit's own leaves out the Safari version, so the panel says it is the
// Safari this machine has: the engine it really is, so no site serves it code for another.
func SafariUserAgent() string {
	version := fallbackSafariVersion
	if plist, err := os.ReadFile("/Applications/Safari.app/Contents/Info.plist"); err == nil {
		if read, ok := SafariVersion(string(plist)); ok {
			version = read
		}
	}
	return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/" + version + " Safari/605.1.15"
}
