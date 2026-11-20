package application

import (
	"encoding"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unsafe"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Marks a field in a record's defaults that an older host may not omit.
type remoteWorkspaceRequired struct{}

var requiredText = remoteWorkspaceRequired{}

// Defaults are written in the wire form the reader fills: JSON values decoded as `any`.
// Required identity cannot be invented. Other required fields must declare an older-host default.
var threadDefaults = map[string]any{
	"id": requiredText, "title": "Untitled thread", "executionPolicy": "confirm", "engine": requiredText,
	"messages": []any{}, "continuationStatus": "none", "lastChangeSnapshot": map[string]any{"files": []any{}, "capturedAt": 0.0}, "updatedAt": 0.0,
	// An older host ranks nothing; readableRemoteThread ranks its threads by when they last spoke.
	"rankedAt": 0.0,
}

var projectDefaults = map[string]any{"id": requiredText, "root": requiredText}

var sideChatDefaults = map[string]any{"id": requiredText, "sourceThreadId": requiredText, "error": nil}

func record(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

// The list itself when no item changes.
func mapKept[T comparable](list []T, read func(item T) T) []T {
	result := list
	for index, item := range list {
		next := read(item)
		if next == item {
			continue
		}
		if jsonx.SameSlice(result, list) {
			result = append([]T(nil), list...)
		}
		result[index] = next
	}
	return result
}

// Keeps wire revisions separate from the normalized view. A missing revision asks for a snapshot
// (resync true); a stale one answers nil without resync; incompatible snapshot data is an error so
// the connection can report it instead of looping. Not safe for concurrent use.
func CreateRemoteWorkspaceReplica() func(update *contracts.ComputerWorkspaceUpdate) (state *WorkspaceState, resync bool, err error) {
	read := CreateRemoteWorkspaceReader()
	var wire any
	revision := -1.0
	return func(update *contracts.ComputerWorkspaceUpdate) (*WorkspaceState, bool, error) {
		switch {
		case len(update.State) > 0:
			var decoded any
			if err := jsonx.Unmarshal(update.State, &decoded); err != nil {
				return nil, false, err
			}
			wire = decoded
		case wire != nil && update.Revision == revision+1:
			next, err := ApplyWorkspacePatches(wire, update.Patches)
			if err != nil {
				return nil, true, nil
			}
			wire = next
		case update.Revision <= revision:
			return nil, false, nil
		default:
			return nil, true, nil
		}
		state, err := read(wire)
		if err != nil {
			return nil, false, err
		}
		revision = update.Revision
		return state, false, nil
	}
}

// One reader per connection, over the wire state decoded as `any` (map[string]any, []any, …) so
// unchanged parts keep their identity between patches. Defaults live with the state they describe,
// so a new state field automatically has a value when an older host omits it. Not safe for concurrent use.
func CreateRemoteWorkspaceReader() func(wire any) (*WorkspaceState, error) {
	empty := EmptyWorkspaceState(nil)
	reader := &remoteReader{
		defaults:  remoteWireForm(empty),
		emptyDock: remoteWireForm(EmptyDock),
		empty:     reflect.ValueOf(empty).Elem(),
		memo:      newRemoteIdentityMemo(),
	}
	var lastWire map[string]any
	var lastView *WorkspaceState
	return func(wire any) (*WorkspaceState, error) {
		state, ok := wire.(map[string]any)
		if !ok {
			return nil, errors.New("Invalid remote workspace.")
		}
		if lastView != nil && jsonx.SameMap(state, lastWire) {
			return lastView, nil
		}
		defer reader.memo.turn()
		view, err := reader.read(state)
		if err != nil {
			return nil, err
		}
		lastWire, lastView = state, view
		return view, nil
	}
}

// A Go value as the JSON values it encodes to.
func remoteWireForm(value any) any {
	data, err := jsonx.Marshal(value)
	if err != nil {
		panic("application: remote workspace defaults do not encode: " + err.Error())
	}
	var wire any
	if err := jsonx.Unmarshal(data, &wire); err != nil {
		panic("application: remote workspace defaults do not decode: " + err.Error())
	}
	return wire
}

type remoteReader struct {
	defaults  any
	emptyDock any
	// The empty state, whose field stands in for one this build cannot read.
	empty reflect.Value
	memo  *remoteIdentityMemo
}

func (r *remoteReader) read(wire map[string]any) (*WorkspaceState, error) {
	filled, err := r.fill(wire, true, r.defaults)
	if err != nil {
		return nil, err
	}
	state := filled.(map[string]any)
	if docks, ok := state["docks"].(map[string]any); ok {
		next, err := r.docks(docks)
		if err != nil {
			return nil, err
		}
		state = remoteWithKey(state, "docks", next)
	}
	for _, list := range remoteRecordLists {
		key, template := list.key, list.template
		values, ok := state[key].([]any)
		if !ok {
			continue
		}
		next, err := r.items(values, template)
		if err != nil {
			return nil, err
		}
		state = remoteWithKey(state, key, next)
	}
	decoded := reflect.New(reflect.TypeFor[WorkspaceState]()).Elem()
	if err := r.decodeStruct(state, decoded, r.empty, true); err != nil {
		return nil, err
	}
	view := decoded.Addr().Interface().(*WorkspaceState)
	view.Threads = r.knownThreads(r.originThreads(view.Threads))
	return view, nil
}

// The lists of records whose items are filled from their own defaults, in the order they are read.
var remoteRecordLists = []struct {
	key      string
	template map[string]any
}{{"threads", threadDefaults}, {"projects", projectDefaults}, {"sideChats", sideChatDefaults}}

// `record` with `key` set to `value`, the record itself when it already holds that value.
func remoteWithKey(record map[string]any, key string, value any) map[string]any {
	if remoteSameWire(record[key], value) {
		return record
	}
	next := make(map[string]any, len(record))
	for name, held := range record {
		next[name] = held
	}
	next[key] = value
	return next
}

// Every dock filled from the empty one, the docks themselves while none changes.
func (r *remoteReader) docks(docks map[string]any) (any, error) {
	key := remoteWireKey(docks, "docks")
	if held, ok := r.memo.get(key); ok {
		return held, nil
	}
	next := docks
	for id, dock := range docks {
		filled, err := r.fill(dock, true, r.emptyDock)
		if err != nil {
			return nil, err
		}
		if remoteSameWire(filled, dock) {
			continue
		}
		if jsonx.SameMap(next, docks) {
			next = make(map[string]any, len(docks))
			for name, held := range docks {
				next[name] = held
			}
		}
		next[id] = filled
	}
	r.memo.put(key, docks, next)
	return next, nil
}

func (r *remoteReader) items(values []any, template map[string]any) (any, error) {
	key := remoteWireKey(values, remoteTemplateID(template))
	if held, ok := r.memo.get(key); ok {
		return held, nil
	}
	var failure error
	result := remoteMapKeptWire(values, func(value any) any {
		if failure != nil {
			return value
		}
		if !record(value) {
			failure = errors.New("Invalid remote workspace item.")
			return value
		}
		filled, err := r.fill(value, true, template)
		if err != nil {
			failure = err
			return value
		}
		return filled
	})
	if failure != nil {
		return nil, failure
	}
	r.memo.put(key, values, result)
	return result, nil
}

// mapKept over wire values, which compare by identity.
func remoteMapKeptWire(list []any, read func(item any) any) []any {
	result := list
	for index, item := range list {
		next := read(item)
		if remoteSameWire(next, item) {
			continue
		}
		if jsonx.SameSlice(result, list) {
			result = append([]any(nil), list...)
		}
		result[index] = next
	}
	return result
}

// TypeScript's === on wire values: containers by identity, scalars by value.
func remoteSameWire(a, b any) bool {
	switch a := a.(type) {
	case map[string]any:
		other, ok := b.(map[string]any)
		return ok && jsonx.SameMap(a, other)
	case []any:
		other, ok := b.([]any)
		return ok && jsonx.SameSlice(a, other)
	}
	switch b.(type) {
	case map[string]any, []any:
		return false
	}
	return a == b
}

// `value` with what it leaves out taken from `template`; `present` is false for a key the wire left out.
func (r *remoteReader) fill(value any, present bool, template any) (any, error) {
	if template == requiredText {
		if text, ok := value.(string); !ok || text == "" {
			return nil, errors.New("Missing remote workspace identity.")
		}
		return value, nil
	}
	if !present {
		return template, nil
	}
	if template == nil {
		return value, nil
	}
	switch template := template.(type) {
	case []any:
		if _, ok := value.([]any); !ok {
			return nil, errors.New("Invalid remote workspace collection.")
		}
		return value, nil
	case map[string]any:
		fields, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("Invalid remote workspace record.")
		}
		key := remoteWireKey(fields, remoteTemplateID(template))
		if held, ok := r.memo.get(key); ok {
			return held, nil
		}
		result := fields
		for name, fallback := range template {
			held, has := fields[name]
			next, err := r.fill(held, has, fallback)
			if err != nil {
				return nil, err
			}
			if has && remoteSameWire(next, held) {
				continue
			}
			if jsonx.SameMap(result, fields) {
				result = make(map[string]any, len(fields)+1)
				for field, kept := range fields {
					result[field] = kept
				}
			}
			result[name] = next
		}
		r.memo.put(key, fields, result)
		return result, nil
	}
	if remoteWireKind(value) != remoteWireKind(template) {
		return nil, errors.New("Invalid remote workspace field.")
	}
	return value, nil
}

// JavaScript's typeof for a wire value.
func remoteWireKind(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	}
	return "object"
}

func remoteTemplateID(template map[string]any) unsafe.Pointer {
	return reflect.ValueOf(template).UnsafePointer()
}

// An older host labels messages without saying who sent them; each is read once, by reference.
func (r *remoteReader) originThreads(threads []*domain.Thread) []*domain.Thread {
	key := remoteMemoKey{ptr: unsafe.Pointer(unsafe.SliceData(threads)), n: len(threads), kind: "origins"}
	if held, ok := r.memo.get(key); ok {
		return held.([]*domain.Thread)
	}
	read := mapKept(threads, func(thread *domain.Thread) *domain.Thread {
		threadKey := remoteMemoKey{ptr: unsafe.Pointer(thread), n: -1, kind: "origin"}
		if held, ok := r.memo.get(threadKey); ok {
			return held.(*domain.Thread)
		}
		messages := mapKept(thread.Messages, func(message *domain.ConversationMessage) *domain.ConversationMessage {
			messageKey := remoteMemoKey{ptr: unsafe.Pointer(message), n: -1, kind: "origin"}
			if held, ok := r.memo.get(messageKey); ok {
				return held.(*domain.ConversationMessage)
			}
			next := domain.WithLegacyOrigin(message)
			r.memo.put(messageKey, message, next)
			return next
		})
		next := thread
		if !jsonx.SameSlice(messages, thread.Messages) {
			copied := *thread
			copied.Messages = messages
			next = &copied
		}
		r.memo.put(threadKey, thread, next)
		return next
	})
	r.memo.put(key, threads, read)
	return read
}

// A newer build may send what this one does not know; each thread is read once, by reference.
func (r *remoteReader) knownThreads(threads []*domain.Thread) []*domain.Thread {
	key := remoteMemoKey{ptr: unsafe.Pointer(unsafe.SliceData(threads)), n: len(threads), kind: "known"}
	if held, ok := r.memo.get(key); ok {
		return held.([]*domain.Thread)
	}
	read := mapKept(threads, func(thread *domain.Thread) *domain.Thread {
		threadKey := remoteMemoKey{ptr: unsafe.Pointer(thread), n: -1, kind: "known"}
		if held, ok := r.memo.get(threadKey); ok {
			return held.(*domain.Thread)
		}
		next := domain.ReadableRemoteThread(thread)
		r.memo.put(threadKey, thread, next)
		return next
	})
	r.memo.put(key, threads, read)
	return read
}

// What one read derived from a node, kept while the next read still holds that node. Each entry pins
// its node, so an address is never reused while its key is held.
type remoteMemoKey struct {
	ptr  unsafe.Pointer
	n    int
	kind any
}

type remoteMemoEntry struct {
	node  any
	value any
}

type remoteIdentityMemo struct {
	prev, cur map[remoteMemoKey]remoteMemoEntry
}

func newRemoteIdentityMemo() *remoteIdentityMemo {
	return &remoteIdentityMemo{prev: map[remoteMemoKey]remoteMemoEntry{}, cur: map[remoteMemoKey]remoteMemoEntry{}}
}

func (m *remoteIdentityMemo) get(key remoteMemoKey) (any, bool) {
	if entry, ok := m.cur[key]; ok {
		return entry.value, true
	}
	if entry, ok := m.prev[key]; ok {
		m.cur[key] = entry
		return entry.value, true
	}
	return nil, false
}

func (m *remoteIdentityMemo) put(key remoteMemoKey, node, value any) {
	m.cur[key] = remoteMemoEntry{node: node, value: value}
}

// Ends a read: what it used is kept for the next, everything else is let go.
func (m *remoteIdentityMemo) turn() {
	m.prev, m.cur = m.cur, map[remoteMemoKey]remoteMemoEntry{}
}

// The key of a wire container, or a zero key for a scalar.
func remoteWireKey(node any, kind any) remoteMemoKey {
	switch node := node.(type) {
	case map[string]any:
		return remoteMemoKey{ptr: reflect.ValueOf(node).UnsafePointer(), n: -1, kind: kind}
	case []any:
		return remoteMemoKey{ptr: unsafe.Pointer(unsafe.SliceData(node)), n: len(node), kind: kind}
	}
	return remoteMemoKey{}
}

var (
	remoteUnmarshalerFrom = reflect.TypeFor[json.UnmarshalerFrom]()
	remoteUnmarshaler     = reflect.TypeFor[json.Unmarshaler]()
	remoteTextUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
	remoteRawValue        = reflect.TypeFor[jsontext.Value]()
)

// Whether a type decodes itself, which the reader leaves to the JSON decoder.
func remoteDecodesItself(t reflect.Type) bool {
	if t == remoteRawValue || (t.Kind() == reflect.Interface && t.NumMethod() > 0) {
		return true
	}
	pointer := reflect.PointerTo(t)
	return pointer.Implements(remoteUnmarshalerFrom) || pointer.Implements(remoteUnmarshaler) || pointer.Implements(remoteTextUnmarshaler)
}

// decode reads a filled wire value as t. A container the previous read decoded to t is the same Go
// value, so what did not change keeps its identity. What this build cannot read is left out: a
// list item or record entry dropped, a thread's field as its zero value.
func (r *remoteReader) decode(value any, t reflect.Type) (reflect.Value, error) {
	if remoteDecodesItself(t) {
		return r.viaJSON(value, t)
	}
	if value == nil {
		return reflect.Zero(t), nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		key := remoteWireKey(value, t)
		if key.ptr != nil {
			if held, ok := r.memo.get(key); ok {
				return held.(reflect.Value), nil
			}
		}
		elem, err := r.decode(value, t.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		pointer := reflect.New(t.Elem())
		pointer.Elem().Set(elem)
		if key.ptr != nil {
			r.memo.put(key, value, pointer)
		}
		return pointer, nil
	case reflect.Struct:
		fields, ok := value.(map[string]any)
		if !ok {
			return reflect.Value{}, errors.New("not an object")
		}
		out := reflect.New(t).Elem()
		if err := r.decodeStruct(fields, out, reflect.Value{}, t == remoteLenient); err != nil {
			return reflect.Value{}, err
		}
		return out, nil
	case reflect.Slice:
		items, ok := value.([]any)
		if !ok {
			return reflect.Value{}, errors.New("not an array")
		}
		key := remoteWireKey(items, t)
		if held, ok := r.memo.get(key); ok {
			return held.(reflect.Value), nil
		}
		out := reflect.MakeSlice(t, 0, len(items))
		for _, item := range items {
			if elem, err := r.decode(item, t.Elem()); err == nil {
				out = reflect.Append(out, elem)
			}
		}
		r.memo.put(key, items, out)
		return out, nil
	case reflect.Map:
		entries, ok := value.(map[string]any)
		if !ok || t.Key().Kind() != reflect.String {
			return reflect.Value{}, errors.New("not an object")
		}
		key := remoteWireKey(entries, t)
		if held, ok := r.memo.get(key); ok {
			return held.(reflect.Value), nil
		}
		out := reflect.MakeMapWithSize(t, len(entries))
		for name, entry := range entries {
			if elem, err := r.decode(entry, t.Elem()); err == nil {
				out.SetMapIndex(reflect.ValueOf(name).Convert(t.Key()), elem)
			}
		}
		r.memo.put(key, entries, out)
		return out, nil
	case reflect.String:
		if text, ok := value.(string); ok {
			return reflect.ValueOf(text).Convert(t), nil
		}
	case reflect.Float64, reflect.Float32, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if number, ok := value.(float64); ok {
			return reflect.ValueOf(number).Convert(t), nil
		}
	case reflect.Bool:
		if flag, ok := value.(bool); ok {
			return reflect.ValueOf(flag).Convert(t), nil
		}
	case reflect.Interface:
		out := reflect.New(t).Elem()
		out.Set(reflect.ValueOf(value))
		return out, nil
	}
	return reflect.Value{}, errors.New("unexpected " + remoteWireKind(value))
}

// Decodes through the JSON decoder, which a type that decodes itself asks for.
func (r *remoteReader) viaJSON(value any, t reflect.Type) (reflect.Value, error) {
	key := remoteWireKey(value, t)
	if key.ptr != nil {
		if held, ok := r.memo.get(key); ok {
			return held.(reflect.Value), nil
		}
	}
	data, err := jsonx.Marshal(value)
	if err != nil {
		return reflect.Value{}, err
	}
	out := reflect.New(t)
	if err := jsonx.Unmarshal(data, out.Interface()); err != nil {
		return reflect.Value{}, err
	}
	if key.ptr != nil {
		r.memo.put(key, value, out.Elem())
	}
	return out.Elem(), nil
}

// Fills `out` from an object's fields. A lenient struct leaves out a field this build cannot read,
// taking its value in `fallback` when there is one, else its zero value; any other fails whole.
func (r *remoteReader) decodeStruct(fields map[string]any, out reflect.Value, fallback reflect.Value, lenient bool) error {
	for _, field := range remoteStructFieldsOf(out.Type()) {
		target := out.FieldByIndex(field.index)
		if field.inline {
			var inner reflect.Value
			if fallback.IsValid() {
				inner = fallback.FieldByIndex(field.index)
			}
			if err := r.decodeStruct(fields, target, inner, lenient); err != nil {
				return err
			}
			continue
		}
		value, present := fields[field.name]
		if !present {
			continue
		}
		decoded, err := r.decode(value, target.Type())
		switch {
		case err == nil:
			target.Set(decoded)
		case !lenient:
			return fmt.Errorf("%s: %w", field.name, err)
		case fallback.IsValid():
			target.Set(fallback.FieldByIndex(field.index))
		}
	}
	return nil
}

// A thread leaves out what it cannot read field by field, as another build's thread is read.
var remoteLenient = reflect.TypeFor[domain.Thread]()

type remoteStructField struct {
	index  []int
	name   string
	inline bool
}

var remoteStructFields sync.Map // reflect.Type -> []remoteStructField

// The fields JSON reads of a struct, by the names its tags give them.
func remoteStructFieldsOf(t reflect.Type) []remoteStructField {
	if held, ok := remoteStructFields.Load(t); ok {
		return held.([]remoteStructField)
	}
	var fields []remoteStructField
	for index := range t.NumField() {
		field := t.Field(index)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		inline := strings.Contains(","+options+",", ",inline,") || (field.Anonymous && name == "" && field.Type.Kind() == reflect.Struct)
		if !field.IsExported() && !(field.Anonymous && inline) {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields = append(fields, remoteStructField{index: field.Index, name: name, inline: inline})
	}
	remoteStructFields.Store(t, fields)
	return fields
}
