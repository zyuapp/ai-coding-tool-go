package domain

import (
	"errors"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	whatwg "github.com/nationallibraryofnorway/whatwg-url/url"
)

var localHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "[::1]": true, "0.0.0.0": true}

var browserSchemePattern = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)

// BrowserURL is what the browser loads for what a caller typed; false when that is nothing it can load.
func BrowserURL(text string) (string, bool) {
	trimmed := jsTrim(text)
	if trimmed == "" {
		return "", false
	}
	candidate := trimmed
	if !browserSchemePattern.MatchString(trimmed) {
		scheme := "https"
		host := trimmed
		if end := strings.IndexAny(trimmed, ":/?#"); end >= 0 {
			host = trimmed[:end]
		}
		if localHosts[host] {
			scheme = "http"
		}
		candidate = scheme + "://" + trimmed
	}
	url, err := parseWebURL(candidate)
	if err != nil {
		return "", false
	}
	if url.Protocol() != "http:" && url.Protocol() != "https:" {
		return "", false
	}
	if url.Hostname() == "" {
		return "", false
	}
	return url.Href(false), true
}

func BrowserSearchURL(query string) string {
	return "https://duckduckgo.com/?q=" + featuresEncodeURIComponent(jsTrim(query))
}

// BrowserOrigin is the origin an allowlist entry covers; false for anything that is not a loadable page.
func BrowserOrigin(rawURL string) (string, bool) {
	parsed, err := parseWebURL(rawURL)
	if err != nil || parsed.Protocol() != "http:" && parsed.Protocol() != "https:" {
		return "", false
	}
	return parsed.Protocol() + "//" + parsed.Host(), true
}

// BrowserTabTitle reads only the tab's Title and URL.
func BrowserTabTitle(tab *BrowserTab) string {
	if tab.Title != "" {
		return tab.Title
	}
	parsed, err := parseWebURL(tab.URL)
	if err != nil || parsed.Host() == "" {
		return "New tab"
	}
	return parsed.Host()
}

func DescribeTab(tab *BrowserTab) string {
	label := tab.Title
	if label == "" {
		label = tab.URL
	}
	if label == "" {
		label = "Blank tab"
	}
	parts := []string{label + " [" + tab.ID + "]", tab.URL}
	if tab.Loading {
		parts = append(parts, "loading")
	}
	if tab.Error != nil {
		parts = append(parts, *tab.Error)
	}
	kept := parts[:0]
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " · ")
}

var errInvalidLabel = errors.New("invalid punycode label")

// parseWebURL is new URL(text). It also refuses an "xn--" label that does not decode to the label it
// names, which the URL standard's domain-to-ASCII step rejects.
func parseWebURL(text string) (*whatwg.Url, error) {
	url, err := whatwg.Parse(text)
	if err != nil || !url.IsSpecialScheme() || url.IsIPv4() || url.IsIPv6() {
		return url, err
	}
	for label := range strings.SplitSeq(url.Hostname(), ".") {
		if !strings.HasPrefix(label, "xn--") {
			continue
		}
		decoded, ok := decodePunycode(label[4:])
		if !ok || decoded == "" {
			return nil, errInvalidLabel
		}
		if isASCII(decoded) {
			continue
		}
		if again, err := whatwg.Parse("https://" + decoded + "/"); err != nil || again.Hostname() != label {
			return nil, errInvalidLabel
		}
	}
	return url, nil
}

func isASCII(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// decodePunycode is RFC 3492's decoding, without any check on what it decodes to.
func decodePunycode(encoded string) (string, bool) {
	const base, tMin, tMax, skew, damp = 36, 1, 26, 38, 700
	output := []rune{}
	if delimiter := strings.LastIndexByte(encoded, '-'); delimiter >= 0 {
		for _, r := range encoded[:delimiter] {
			if r >= utf8.RuneSelf {
				return "", false
			}
			output = append(output, r)
		}
		encoded = encoded[delimiter+1:]
	}
	n, bias, i := 128, 72, 0
	for position := 0; position < len(encoded); {
		previous, weight := i, 1
		for k := base; ; k += base {
			if position >= len(encoded) {
				return "", false
			}
			c := encoded[position]
			position++
			var digit int
			switch {
			case c >= '0' && c <= '9':
				digit = int(c-'0') + 26
			case c >= 'a' && c <= 'z':
				digit = int(c - 'a')
			case c >= 'A' && c <= 'Z':
				digit = int(c - 'A')
			default:
				return "", false
			}
			if digit > (math.MaxInt32-i)/weight {
				return "", false
			}
			i += digit * weight
			t := min(max(k-bias, tMin), tMax)
			if digit < t {
				break
			}
			if weight > math.MaxInt32/(base-t) {
				return "", false
			}
			weight *= base - t
		}
		length := len(output) + 1
		delta := i - previous
		if previous == 0 {
			delta /= damp
		} else {
			delta /= 2
		}
		delta += delta / length
		k := 0
		for delta > (base-tMin)*tMax/2 {
			delta /= base - tMin
			k += base
		}
		bias = k + (base-tMin+1)*delta/(delta+skew)
		if i/length > math.MaxInt32-n {
			return "", false
		}
		n += i / length
		i %= length
		if n > utf8.MaxRune || n >= 0xD800 && n <= 0xDFFF {
			return "", false
		}
		output = append(output[:i], append([]rune{rune(n)}, output[i:]...)...)
		i++
	}
	return string(output), true
}
