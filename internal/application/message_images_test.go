package application

import (
	"strings"
	"testing"
)

func TestMessageImagesFollowMarkdownDestinations(t *testing.T) {
	text := "[Before](</tmp/before view.png>) ![After](/tmp/after\\(1\\).jpg)\n\n[Again][before]\n\n[before]: </tmp/before view.png>\n\n`[Example](/tmp/example.png)`\n\n[Website](https://example.com/image.png)"
	coreEqualJSON(t, MessageImages(text), `[{"path":"/tmp/before view.png","label":"Before"},{"path":"/tmp/after(1).jpg","label":"After"}]`, "images")
}

func TestMessageImagesAreCached(t *testing.T) {
	text := "[Shot](/tmp/shot.webp)"
	first := MessageImages(text)
	if again := MessageImages(text); len(again) != 1 || again[0] != first[0] {
		t.Fatal("a repeated lookup parsed again")
	}
	large := "![Big](/tmp/big.png)\n\n" + strings.Repeat("x", 70*1024)
	for range 2 {
		coreEqualJSON(t, MessageImages(large), `[{"path":"/tmp/big.png","label":"Big"}]`, "large")
	}
	if got := MessageImages("no links here"); len(got) != 0 {
		t.Fatal("plain text")
	}
}
