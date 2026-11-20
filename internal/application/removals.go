package application

import (
	"maps"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// A record or list without some of its entries, or the same one when none of them is there.

func WithoutThreadKeys[T any](record map[string]T, removed jsonx.Set[string]) map[string]T {
	var cleaned map[string]T
	for threadID := range removed {
		if _, ok := record[threadID]; !ok {
			continue
		}
		if cleaned == nil {
			cleaned = maps.Clone(record)
		}
		delete(cleaned, threadID)
	}
	if cleaned == nil {
		return record
	}
	return cleaned
}

func WithoutMatchingValues[T any](record map[string]T, matches func(value T) bool) map[string]T {
	var cleaned map[string]T
	for key, value := range record {
		if !matches(value) {
			continue
		}
		if cleaned == nil {
			cleaned = maps.Clone(record)
		}
		delete(cleaned, key)
	}
	if cleaned == nil {
		return record
	}
	return cleaned
}

func WithoutMatching[T any](items []T, matches func(item T) bool) []T {
	first := -1
	for index, item := range items {
		if matches(item) {
			first = index
			break
		}
	}
	if first == -1 {
		return items
	}
	kept := make([]T, first, len(items)-1)
	copy(kept, items[:first])
	for _, item := range items[first+1:] {
		if !matches(item) {
			kept = append(kept, item)
		}
	}
	return kept
}
