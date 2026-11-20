package domain

import (
	"reflect"
	"testing"
)

func TestImportSitesFoldSubdomains(t *testing.T) {
	sites := ImportSites([]BrowserCookieHost{
		{"gist.github.com", 2}, {".github.com", 3}, {"a.b.github.com", 1}, {"bbc.co.uk", 1},
		{"news.bbc.co.uk", 1}, {"other.co.uk", 4}, {"only.sub.example.com", 1},
	})
	want := []*BrowserImportSite{{Domain: "bbc.co.uk", Cookies: 2}, {Domain: "github.com", Cookies: 6}, {Domain: "only.sub.example.com", Cookies: 1}, {Domain: "other.co.uk", Cookies: 4}}
	if !reflect.DeepEqual(sites, want) {
		t.Fatalf("got %v", sites)
	}
}

func TestCookieOnSiteNeverMatchesALookalike(t *testing.T) {
	for host, want := range map[string]bool{".github.com": true, "api.github.com": true, "notgithub.com": false, "github.com.evil.io": false} {
		if CookieOnSite(host, "github.com") != want {
			t.Errorf("%s", host)
		}
	}
}

func TestOnlyPlainHostNamesCanBeAskedFor(t *testing.T) {
	for site, want := range map[string]bool{"github.com": true, ".github.com": false, "github.com/path": false, "": false} {
		if IsImportSite(site) != want {
			t.Errorf("%q", site)
		}
	}
	memory := func(sites ...any) any { return map[string]any{"sourceId": "brave:0:Default", "sites": sites} }
	if !IsBrowserImportMemory(memory("github.com")) || IsBrowserImportMemory(memory()) || IsBrowserImportMemory(memory("../x")) {
		t.Fatal("memory validation")
	}
}
