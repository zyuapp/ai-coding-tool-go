package domain

import (
	"reflect"
	"testing"
)

func TestFileURLNamesTheSameFileAsItsPath(t *testing.T) {
	line := 12.0
	cases := map[string]*FileHref{
		"file:///tmp/mock%20up.html":     {File: "/tmp/mock up.html"},
		"file://localhost/tmp/app.ts:12": {File: "/tmp/app.ts", Line: &line},
		"file:///C:/work/page.html":      {File: "C:/work/page.html"},
		"https://example.com/page.html":  nil,
		"file://server/share/page.html":  nil,
		"/tmp/bad%zz.ts":                 nil,
		"/tmp/bad%ff.ts":                 nil,
	}
	for href, want := range cases {
		if got := ParseFileHref(href); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v", href, got)
		}
	}
}
