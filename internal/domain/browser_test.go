package domain

import "testing"

func TestBrowserURLsOnlyLoadWebPages(t *testing.T) {
	cases := map[string]string{
		"localhost:3000":       "http://localhost:3000/",
		"example.com":          "https://example.com/",
		" HTTPS://Example.COM": "https://example.com/",
		"https://a.com:443/x":  "https://a.com/x",
		"file:///etc/passwd":   "",
		"javascript:alert(1)":  "",
		"":                     "",
	}
	for text, want := range cases {
		if got, _ := BrowserURL(text); got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
	if origin, ok := BrowserOrigin("https://a.com:8443/x?y"); !ok || origin != "https://a.com:8443" {
		t.Error(origin)
	}
	if _, ok := BrowserOrigin("about:blank"); ok {
		t.Error("about:blank has no origin")
	}
	if got := BrowserSearchURL(" a b&c "); got != "https://duckduckgo.com/?q=a%20b%26c" {
		t.Error(got)
	}
	if got := BrowserTabTitle(&BrowserTab{URL: "http://localhost:3000/x"}); got != "localhost:3000" {
		t.Error(got)
	}
	if got := BrowserTabTitle(&BrowserTab{URL: "not a url"}); got != "New tab" {
		t.Error(got)
	}
}
