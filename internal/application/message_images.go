package application

import (
	"container/list"
	"strings"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// Image lists of recently seen texts, least recently used first, bounded by the characters they were parsed from.
var cached struct {
	sync.Mutex
	order  *list.List
	byText map[string]*list.Element
}

// Characters of the texts in `cached`; guarded by its mutex.
var cachedChars int

const cacheChars = 512 * 1024

const maxCachedChars = 64 * 1024

type cachedImages struct {
	text   string
	images []*MessageImage
}

// CommonMark without extensions, as mdast's fromMarkdown reads it.
var messageMarkdown = goldmark.New()

// The same Markdown destinations supply both previews and durable copies, including reference links.
// Callers must not modify the returned slice: it is shared with the cache.
func MessageImages(text string) []*MessageImage {
	if !domain.MayLinkImages(text) {
		return []*MessageImage{}
	}
	length := threadsUTF16Len(text)
	if length > maxCachedChars {
		return parseMessageImages(text)
	}
	cached.Lock()
	if cached.order == nil {
		cached.order, cached.byText = list.New(), map[string]*list.Element{}
	}
	if hit, ok := cached.byText[text]; ok {
		cached.order.MoveToBack(hit)
		images := hit.Value.(*cachedImages).images
		cached.Unlock()
		return images
	}
	cached.Unlock()
	images := parseMessageImages(text)
	cached.Lock()
	defer cached.Unlock()
	if _, ok := cached.byText[text]; !ok {
		cached.byText[text] = cached.order.PushBack(&cachedImages{text: text, images: images})
		cachedChars += length
	}
	for cachedChars > cacheChars {
		oldest := cached.order.Front()
		entry := cached.order.Remove(oldest).(*cachedImages)
		delete(cached.byText, entry.text)
		cachedChars -= threadsUTF16Len(entry.text)
	}
	return images
}

func parseMessageImages(source string) []*MessageImage {
	raw := []byte(source)
	tree := messageMarkdown.Parser().Parse(text.NewReader(raw))
	images := []*MessageImage{}
	seen := map[string]bool{}
	_ = ast.Walk(tree, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if len(images) >= domain.MaxMessageImages {
			return ast.WalkStop, nil
		}
		var url, label string
		switch node := node.(type) {
		case *ast.Link:
			url, label = markdownDecoded(node.Destination), linkLabel(node, raw)
		case *ast.Image:
			url, label = markdownDecoded(node.Destination), altText(node, raw)
		case *ast.AutoLink:
			url, label = string(node.URL(raw)), string(node.Label(raw))
		default:
			return ast.WalkContinue, nil
		}
		if url == "" {
			return ast.WalkContinue, nil
		}
		if file, ok := domain.MessageImagePath(url); ok && file != "" && !seen[file] {
			seen[file] = true
			if label == "" {
				label = "Screenshot"
			}
			images = append(images, &MessageImage{Path: file, Label: label})
		}
		return ast.WalkContinue, nil
	})
	return images
}

// markdownDecoded is a destination with its escapes and character references read, as mdast gives it.
func markdownDecoded(raw []byte) string {
	return string(util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(raw))))
}

// linkLabel is what a link's own text says: the values of its direct children, as mdast joins them.
func linkLabel(link ast.Node, source []byte) string {
	var label strings.Builder
	for child := link.FirstChild(); child != nil; child = child.NextSibling() {
		switch child := child.(type) {
		case *ast.Text:
			label.WriteString(markdownDecoded(child.Segment.Value(source)))
			if child.SoftLineBreak() || child.HardLineBreak() {
				label.WriteByte('\n')
			}
		case *ast.String:
			label.Write(child.Value)
		case *ast.CodeSpan:
			label.WriteString(plainText(child, source, false))
		case *ast.RawHTML:
			for index := 0; index < child.Segments.Len(); index++ {
				segment := child.Segments.At(index)
				label.Write(segment.Value(source))
			}
		}
	}
	return label.String()
}

// altText is an image's description as plain text.
func altText(image ast.Node, source []byte) string {
	return plainText(image, source, true)
}

// plainText is every piece of text under node, in order; `decode` reads escapes as mdast does outside code.
func plainText(node ast.Node, source []byte, decode bool) string {
	var plain strings.Builder
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch child := child.(type) {
		case *ast.Text:
			value := child.Segment.Value(source)
			if decode {
				plain.WriteString(markdownDecoded(value))
			} else {
				plain.Write(value)
			}
			if child.SoftLineBreak() || child.HardLineBreak() {
				plain.WriteByte('\n')
			}
		case *ast.String:
			plain.Write(child.Value)
		case *ast.CodeSpan:
			plain.WriteString(plainText(child, source, false))
		default:
			plain.WriteString(plainText(child, source, decode))
		}
	}
	return plain.String()
}
