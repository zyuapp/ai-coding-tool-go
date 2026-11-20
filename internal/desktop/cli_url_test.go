package desktop

import (
	"encoding/base64"
	"strings"
	"testing"
)

func urlFor(root string) string {
	encoded := strings.NewReplacer("+", "-", "/", "_").Replace(base64.StdEncoding.EncodeToString([]byte(root)))
	return "aicodingtool://open?path=" + encoded
}

func TestProjectPathFromURL(t *testing.T) {
	for _, root := range []string{"/Users/me/code/app", "/Users/me/Ünïcode dir", "/a"} {
		if got := ProjectPathFromURL(urlFor(root)); got != root {
			t.Errorf("%q read as %q", root, got)
		}
	}
	unpadded := strings.TrimRight(urlFor("/a"), "=")
	if got := ProjectPathFromURL(unpadded); got != "/a" {
		t.Errorf("unpadded read as %q", got)
	}
	for _, refused := range []string{"https://example.com/open?path=Lw", "aicodingtool://send?path=Lw", "aicodingtool://open", "aicodingtool://open?path=***", urlFor("relative/path"), urlFor("/a\x00b"), "not a url"} {
		if got := ProjectPathFromURL(refused); got != "" {
			t.Errorf("%q read as %q", refused, got)
		}
	}
}

func TestProjectPathFromArgv(t *testing.T) {
	if got := ProjectPathFromArgv([]string{"/Applications/AI Coding Tool.app", urlFor("/tmp/app")}); got != "/tmp/app" {
		t.Errorf("got %q", got)
	}
	if got := ProjectPathFromArgv([]string{"/Applications/AI Coding Tool.app", "--updated"}); got != "" {
		t.Errorf("got %q", got)
	}
}
