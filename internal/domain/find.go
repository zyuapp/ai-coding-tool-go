package domain

import (
	"iter"
	"runtime"
	"strings"
	"sync"
	"weak"
)

// TargetKey flattens every field of a target, so two are the same when they name the same view.
func TargetKey(target FindTarget) string {
	switch target := target.(type) {
	case FindTargetThread:
		return "thread:" + featuresDeref(target.TaskID)
	case FindTargetBrowser:
		return "browser:" + target.TabID
	case FindTargetTerminal:
		return "terminal:" + target.TerminalID
	case FindTargetReview:
		return "review:" + target.Owner
	case FindTargetPanel:
		return "panel:" + target.Owner + ":" + target.Panel
	}
	panic("domain: unknown find target")
}

// SearchesItself reports whether the view holds its own text and keeps its own place in what it found.
func SearchesItself(target FindTarget) bool {
	switch target.(type) {
	case FindTargetBrowser, FindTargetTerminal:
		return true
	}
	return false
}

// MaxFindHits is more matches than anyone steps through one at a time, and still bounded on a long thread.
const MaxFindHits = 500

func SameFindTarget(one, other FindTarget) bool {
	return TargetKey(one) == TargetKey(other)
}

// hitsInMessage is every match in a message's own text. Starts are UTF-16 offsets into the lowered text.
func hitsInMessage(message *ConversationMessage, needle string, limit int) []*FindHit {
	hits := []*FindHit{}
	for _, field := range [...]FindHitField{FindHitFieldText, FindHitFieldDetail} {
		haystack := message.Text
		if field == FindHitFieldDetail {
			haystack = featuresDeref(message.Detail)
		}
		if haystack == "" {
			continue
		}
		haystack = featuresJSLower(haystack)
		for start := range featuresMatchOffsets(haystack, needle) {
			hits = append(hits, &FindHit{MessageID: message.ID, Field: field, Start: float64(start), Occurrence: float64(len(hits))})
			if len(hits) == limit {
				return hits
			}
		}
	}
	return hits
}

type findMessageHits struct {
	needle string
	hits   []*FindHit
}

// messageHits holds each message's matches against the message itself.
var messageHits = newFindIdentityCache[ConversationMessage, findMessageHits]()

// cachedHitsInMessage is one message's matches, held against the message itself.
func cachedHitsInMessage(message *ConversationMessage, needle string) []*FindHit {
	if cached, ok := messageHits.get(message); ok && cached.needle == needle {
		return cached.hits
	}
	hits := hitsInMessage(message, needle, MaxFindHits)
	messageHits.set(message, findMessageHits{needle: needle, hits: hits})
	return hits
}

// FindHits is every match in a thread, oldest first, capped at MaxFindHits.
func FindHits(messages []*ConversationMessage, query string) []*FindHit {
	needle := featuresJSLower(featuresJSTrim(query))
	if needle == "" {
		return []*FindHit{}
	}
	hits := []*FindHit{}
	for _, message := range messages {
		found := cachedHitsInMessage(message, needle)
		hits = append(hits, found[:min(len(found), MaxFindHits-len(hits))]...)
		if len(hits) == MaxFindHits {
			return hits
		}
	}
	return hits
}

type findTranscriptHits struct {
	query  string
	length int
	hits   []*FindHit
}

// hitCache holds a transcript search against the message slice it searched, keyed by its first element.
var hitCache = newFindIdentityCache[*ConversationMessage, findTranscriptHits]()

// MemoizedFindHits reuses a transcript search while its immutable message list and normalized query are unchanged.
func MemoizedFindHits(messages []*ConversationMessage, query string) []*FindHit {
	needle := featuresJSLower(featuresJSTrim(query))
	if len(messages) == 0 {
		return FindHits(messages, needle)
	}
	first := &messages[0]
	if cached, ok := hitCache.get(first); ok && cached.query == needle && cached.length == len(messages) {
		return cached.hits
	}
	hits := FindHits(messages, needle)
	hitCache.set(first, findTranscriptHits{query: needle, length: len(messages), hits: hits})
	return hits
}

// StepMatch is the match delta (-1 or 1) away, wrapping at both ends.
func StepMatch(index int, delta int, matches int) int {
	if matches <= 0 {
		return 0
	}
	return ((index+delta)%matches + matches) % matches
}

func SameReviewHit(one, other *ReviewHit) bool {
	return one.Path == other.Path && featuresSamePointee(one.Key, other.Key) && one.Occurrence == other.Occurrence
}

// hitsInText is every match in one piece of text, numbered from zero; a nil key is the file's name.
func hitsInText(path string, key *string, text string, needle string, room int) []*ReviewHit {
	hits := []*ReviewHit{}
	if len(hits) >= room {
		return hits
	}
	haystack := featuresJSLower(text)
	for range featuresMatchOffsets(haystack, needle) {
		hits = append(hits, &ReviewHit{Path: path, Key: key, Occurrence: float64(len(hits))})
		if len(hits) >= room {
			break
		}
	}
	return hits
}

// FileReviewHits is every match in one file, its name first, then its lines. The needle arrives lowered and trimmed.
func FileReviewHits(path string, file *DiffFile, needle string, limit int) []*ReviewHit {
	if needle == "" || limit <= 0 {
		return []*ReviewHit{}
	}
	hits := hitsInText(path, nil, path, needle, limit)
	if file == nil {
		return hits
	}
	for _, hunk := range file.Hunks {
		for _, row := range hunk.Rows {
			key := row.Key
			hits = append(hits, hitsInText(path, &key, row.Text, needle, limit-len(hits))...)
			if len(hits) >= limit {
				return hits
			}
		}
	}
	return hits
}

// ReviewHits is every match in a review, in the order it is drawn. scanned holds each file's matches
// under "version|needle" and is written to; nil scans without keeping anything.
func ReviewHits(files []*ReviewFile, needle string, scanned map[string][]*ReviewHit) ReviewCount {
	hits := []*ReviewHit{}
	if needle == "" {
		return ReviewCount{Hits: hits, Counting: false}
	}
	counting := false
	for _, entry := range files {
		if len(hits) >= MaxFindHits {
			break
		}
		if entry.File == nil && entry.Coming {
			counting = true
		}
		// Scanned against the whole cap and sliced after, so what is held never depends on what came before it.
		key := entry.Version + "|" + needle
		var found []*ReviewHit
		if entry.File != nil && scanned != nil {
			found = scanned[key]
		}
		if found == nil {
			found = FileReviewHits(entry.Path, entry.File, needle, MaxFindHits)
			if entry.File != nil && scanned != nil {
				scanned[key] = found
			}
		}
		hits = append(hits, found[:min(len(found), MaxFindHits-len(hits))]...)
	}
	return ReviewCount{Hits: hits, Counting: counting}
}

// findIdentityCache maps a pointer to a value for as long as the pointee lives, as a WeakMap does.
type findIdentityCache[K any, V any] struct {
	mu      sync.Mutex
	entries map[weak.Pointer[K]]V
}

func newFindIdentityCache[K any, V any]() *findIdentityCache[K, V] {
	return &findIdentityCache[K, V]{entries: map[weak.Pointer[K]]V{}}
}

func (cache *findIdentityCache[K, V]) get(key *K) (V, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.entries[weak.Make(key)]
	return value, ok
}

func (cache *findIdentityCache[K, V]) set(key *K, value V) {
	handle := weak.Make(key)
	cache.mu.Lock()
	_, known := cache.entries[handle]
	cache.entries[handle] = value
	cache.mu.Unlock()
	if !known {
		runtime.AddCleanup(key, cache.forget, handle)
	}
}

func (cache *findIdentityCache[K, V]) forget(handle weak.Pointer[K]) {
	cache.mu.Lock()
	delete(cache.entries, handle)
	cache.mu.Unlock()
}

// featuresJSLower is toLowerCase, which writes İ as two code units where Go writes one.
func featuresJSLower(text string) string {
	if !strings.ContainsRune(text, 'İ') {
		return strings.ToLower(text)
	}
	return strings.ToLower(strings.ReplaceAll(text, "İ", "i\u0307"))
}

// featuresMatchOffsets yields each non-overlapping match of needle as a UTF-16 offset, as repeated indexOf finds them.
func featuresMatchOffsets(haystack, needle string) iter.Seq[int] {
	return func(yield func(int) bool) {
		if needle == "" {
			return
		}
		units, scanned := 0, 0
		for from := 0; ; {
			at := strings.Index(haystack[from:], needle)
			if at < 0 {
				return
			}
			at += from
			units += featuresUTF16Len(haystack[scanned:at])
			scanned = at
			if !yield(units) {
				return
			}
			from = at + len(needle)
		}
	}
}

// featuresSamePointee reports whether two optional values are both absent or both present and equal.
func featuresSamePointee[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
