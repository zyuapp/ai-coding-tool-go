package domain

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// handleSpaceClass is the characters of JavaScript's \s, for a regexp character class.
const handleSpaceClass = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var handleToken = regexp.MustCompile(`(?:^|[` + handleSpaceClass + `])@([^` + handleSpaceClass + `@]*)$`)

var asciiWord = regexp.MustCompile(`^[a-z\d]+$`)

var nonSlugRun = regexp.MustCompile(`[^a-z\d]+`)

var nonAlphanumeric = regexp.MustCompile(`(?i)[^a-z\d]`)

var handleTrailing = regexp.MustCompile(`[.,;:!?)\]]+$`)

const slugWords = 4

const slugChars = 40

func ThreadSlug(text string) string {
	lower := strings.ToLower(text)
	lower = strings.NewReplacer("'", "", "\u2019", "").Replace(lower)
	words := strings.Fields(nonSlugRun.ReplaceAllString(lower, " "))
	words = words[:min(len(words), slugWords)]
	slug := strings.Join(words, "-")
	slug = strings.TrimRight(slug[:min(len(slug), slugChars)], "-")
	if slug == "" {
		return "thread"
	}
	return slug
}

// HandleToken is the `@word` the caret sits in: its lower-cased query and where its `@` starts.
type HandleToken struct {
	Query string
	Start int
}

// HandleTokenAt is nil when the caret is in no handle. caret and Start count UTF-16 code units, as the window's do.
func HandleTokenAt(text string, caret int) *HandleToken {
	match := handleToken.FindStringSubmatch(utf16Slice(text, 0, caret))
	if match == nil {
		return nil
	}
	query := match[1]
	return &HandleToken{Query: strings.ToLower(query), Start: caret - utf16Length(query) - 1}
}

// ThreadHandles gives each thread its handle, newest activity first. The Handle the options carry is ignored.
func ThreadHandles(threads []*ThreadHandleOption) []*ThreadHandleOption {
	sorted := slices.Clone(threads)
	slices.SortStableFunc(sorted, func(left, right *ThreadHandleOption) int {
		return cmp.Compare(right.LastActivityAt, left.LastActivityAt)
	})
	taken := map[string]bool{}
	handles := make([]*ThreadHandleOption, len(sorted))
	for i, thread := range sorted {
		slug := ThreadSlug(thread.Title)
		qualified := slug
		if !thread.InScope && thread.Project != nil && *thread.Project != "" {
			qualified = ThreadSlug(*thread.Project) + "/" + slug
		}
		handle := qualified
		if taken[qualified] {
			id := nonAlphanumeric.ReplaceAllString(thread.ID, "")
			handle = qualified + "-" + strings.ToLower(id[max(0, len(id)-4):])
		}
		taken[handle] = true
		next := *thread
		next.Handle = handle
		handles[i] = &next
	}
	return handles
}

// StartsTitleWord is whether a query starts a word of a title.
func StartsTitleWord(title string, query string) bool {
	wanted := strings.ToLower(query)
	if !asciiWord.MatchString(wanted) {
		return false
	}
	lower := utf16.Encode([]rune(strings.ToLower(title)))
	wantedUnits := utf16.Encode([]rune(wanted))
	atWordStart := true
	for index, code := range lower {
		inWord := code >= 48 && code <= 57 || code >= 97 && code <= 122
		if inWord && atWordStart && len(lower)-index >= len(wantedUnits) && slices.Equal(lower[index:index+len(wantedUnits)], wantedUnits) {
			return true
		}
		atWordStart = !inWord
	}
	return false
}

func ThreadHandleMatches(option *ThreadHandleOption, query string) bool {
	if query == "" {
		return true
	}
	if strings.Contains(option.Handle, strings.ToLower(query)) {
		return true
	}
	return StartsTitleWord(option.Title, query)
}

// RankThreadHandles keeps at most limit; pass math.MaxInt for TypeScript's default of Infinity.
func RankThreadHandles(options []*ThreadHandleOption, query string, limit int) []*ThreadHandleOption {
	inside, outside := []*ThreadHandleOption{}, []*ThreadHandleOption{}
	maximum := max(0, limit)
	for _, option := range options {
		if (query == "" && !option.InScope) || !ThreadHandleMatches(option, query) {
			continue
		}
		if option.InScope {
			inside = append(inside, option)
		} else {
			outside = append(outside, option)
		}
		if len(inside) == maximum {
			break
		}
	}
	ranked := append(inside, outside...)
	return ranked[:min(len(ranked), maximum)]
}

func ThreadReference(id string, title string) string {
	return "[" + strings.NewReplacer("[", "", "]", "").Replace(title) + "](aicodingtool://thread/" + id + ")"
}

func ThreadLink(threadID string) string {
	return "<aicodingtool://thread/" + threadID + ">"
}

func ExpandThreadHandles(text string, options []*ThreadHandleOption) string {
	if !strings.Contains(text, "@") {
		return text
	}
	byHandle := make(map[string]*ThreadHandleOption, len(options))
	for _, option := range options {
		byHandle[option.Handle] = option
	}
	return replaceHandles(text, func(lead, token string) (string, bool) {
		trailing := handleTrailing.FindString(token)
		option := byHandle[strings.ToLower(token[:len(token)-len(trailing)])]
		if option == nil {
			return "", false
		}
		return lead + ThreadReference(option.ID, option.Title) + trailing, true
	})
}

// replaceHandles is text.replace(/(^|\s)@([^\s@]+)/g, …): each `@token` at the start or after
// whitespace, replaced where expand answers. A match consumes the whitespace before it.
func replaceHandles(text string, expand func(lead, token string) (string, bool)) string {
	var out strings.Builder
	last := 0
	for index := 0; index < len(text); {
		r, size := utf8.DecodeRuneInString(text[index:])
		lead := ""
		at := index
		if isJSSpace(r) {
			lead, at = text[index:index+size], index+size
		} else if index != 0 {
			index += size
			continue
		}
		if at >= len(text) || text[at] != '@' {
			index += size
			continue
		}
		end := at + 1
		for end < len(text) {
			next, width := utf8.DecodeRuneInString(text[end:])
			if next == '@' || isJSSpace(next) {
				break
			}
			end += width
		}
		if end == at+1 {
			index += size
			continue
		}
		if replaced, ok := expand(lead, text[at+1:end]); ok {
			out.WriteString(text[last:index])
			out.WriteString(replaced)
			last = end
		}
		index = end
	}
	if last == 0 {
		return text
	}
	out.WriteString(text[last:])
	return out.String()
}
