package application

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// container says whether a decoded JSON value (map[string]any or []any) has children a patch can address.
func container(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// WorkspacePatches is the patches that take previous to next, both Go state values of the same type
// (usually *WorkspaceState). Shared references cost nothing: values the same by identity (jsonx.Same,
// structs field by field) are skipped, and only changed branches are encoded. Paths use JSON field
// names; an omitzero field that becomes zero is a remove.
func WorkspacePatches(previous, next any, path []contracts.PathKey) ([]*contracts.WorkspacePatch, error) {
	differ := patchDiffer{patches: []*contracts.WorkspacePatch{}}
	if err := differ.diff(reflect.ValueOf(previous), reflect.ValueOf(next), slices.Clip(path)); err != nil {
		return nil, err
	}
	return differ.patches, nil
}

// Patches is WorkspacePatches between two published states.
func Patches(previous, next *WorkspaceState) ([]*contracts.WorkspacePatch, error) {
	return WorkspacePatches(previous, next, nil)
}

type patchDiffer struct {
	patches []*contracts.WorkspacePatch
}

// jsonShape is what a Go value encodes as, for diffing.
type jsonShape uint8

const (
	shapeNull jsonShape = iota
	shapeLeaf
	shapeObject
	shapeArray
)

func (d *patchDiffer) diff(previous, next reflect.Value, path []contracts.PathKey) error {
	if identical(previous, next) {
		return nil
	}
	beforeShape, before := shapeOf(previous)
	afterShape, after := shapeOf(next)
	if beforeShape == shapeLeaf && afterShape == shapeLeaf || beforeShape == shapeNull && afterShape == shapeNull {
		same, err := sameEncoding(before, after)
		if err != nil || same {
			return err
		}
	}
	if beforeShape != afterShape || beforeShape != shapeObject && beforeShape != shapeArray {
		return d.set(path, next)
	}
	if afterShape == shapeArray {
		return d.diffArrays(before, after, path)
	}
	return d.diffObjects(before, after, path)
}

func (d *patchDiffer) set(path []contracts.PathKey, value reflect.Value) error {
	encoded, err := encodeValue(value)
	if err != nil {
		return err
	}
	d.patches = append(d.patches, &contracts.WorkspacePatch{Path: path, Value: encoded})
	return nil
}

func (d *patchDiffer) diffArrays(previous, next reflect.Value, path []contracts.PathKey) error {
	beforeLength, afterLength := previous.Len(), next.Len()
	prefix := 0
	for prefix < beforeLength && prefix < afterLength && identical(previous.Index(prefix), next.Index(prefix)) {
		prefix++
	}
	suffix := 0
	for suffix < beforeLength-prefix && suffix < afterLength-prefix && identical(previous.Index(beforeLength-suffix-1), next.Index(afterLength-suffix-1)) {
		suffix++
	}
	if beforeLength != afterLength {
		items := make([]jsontext.Value, 0, afterLength-suffix-prefix)
		for index := prefix; index < afterLength-suffix; index++ {
			encoded, err := encodeValue(next.Index(index))
			if err != nil {
				return err
			}
			items = append(items, encoded)
		}
		d.patches = append(d.patches, &contracts.WorkspacePatch{Path: path, Splice: &contracts.WorkspaceSplice{
			Index:       float64(prefix),
			DeleteCount: float64(beforeLength - prefix - suffix),
			Items:       items,
		}})
		return nil
	}
	for index := prefix; index < afterLength-suffix; index++ {
		if err := d.diff(previous.Index(index), next.Index(index), extended(path, contracts.IndexStep(index))); err != nil {
			return err
		}
	}
	return nil
}

func (d *patchDiffer) diffObjects(previous, next reflect.Value, path []contracts.PathKey) error {
	before := objectMembers(previous)
	after := objectMembers(next)
	held := make(map[string]reflect.Value, len(before))
	for _, member := range before {
		held[member.name] = member.value
	}
	present := make(map[string]bool, len(after))
	for _, member := range after {
		present[member.name] = true
		step := extended(path, contracts.KeyStep(member.name))
		if value, ok := held[member.name]; ok {
			if err := d.diff(value, member.value, step); err != nil {
				return err
			}
		} else if err := d.set(step, member.value); err != nil {
			return err
		}
	}
	removed := true
	for _, member := range before {
		if !present[member.name] {
			d.patches = append(d.patches, &contracts.WorkspacePatch{Path: extended(path, contracts.KeyStep(member.name)), Remove: &removed})
		}
	}
	return nil
}

func extended(path []contracts.PathKey, step contracts.PathKey) []contracts.PathKey {
	return append(slices.Clip(path), step)
}

// identical is TypeScript's Object.is between two positions of the state: the same pointer, slice
// (backing array and length) or map, equal scalars, and structs held inline the same field by field.
func identical(a, b reflect.Value) bool {
	if !a.IsValid() || !b.IsValid() {
		return a.IsValid() == b.IsValid()
	}
	if a.Type() != b.Type() {
		return false
	}
	return sameValue(a, b)
}

var (
	marshalerTo   = reflect.TypeFor[json.MarshalerTo]()
	marshalerV1   = reflect.TypeFor[json.Marshaler]()
	rawValueType  = reflect.TypeFor[jsontext.Value]()
	jsonxPackage  = reflect.TypeFor[jsonx.Opt[int]]().PkgPath()
	optGetter     = "Get"
	optNullMethod = "IsNull"
)

// jsonxKind names the jsonx generic a type instantiates ("Opt", "NullSlice", …), or "".
func jsonxKind(t reflect.Type) string {
	if t.PkgPath() != jsonxPackage {
		return ""
	}
	name, _, _ := strings.Cut(t.Name(), "[")
	return name
}

// shapeOf is what value encodes as, with the value that holds its members: pointers, interfaces
// and jsonx.Opt unwrapped.
func shapeOf(value reflect.Value) (jsonShape, reflect.Value) {
	for {
		if !value.IsValid() {
			return shapeNull, value
		}
		switch value.Kind() {
		case reflect.Interface, reflect.Pointer:
			if value.IsNil() {
				return shapeNull, value
			}
			if value.Kind() == reflect.Pointer && customEncoding(value.Type()) {
				return shapeLeaf, value
			}
			value = value.Elem()
			continue
		}
		switch jsonxKind(value.Type()) {
		case "Opt":
			got := value.MethodByName(optGetter).Call(nil)
			if !got[1].Bool() {
				return shapeNull, value
			}
			value = got[0]
			continue
		case "NullSlice":
			if value.IsNil() {
				return shapeNull, value
			}
			return shapeArray, value
		case "NullMap":
			if value.IsNil() {
				return shapeNull, value
			}
			return shapeObject, value
		}
		if customEncoding(value.Type()) {
			return shapeLeaf, value
		}
		switch value.Kind() {
		case reflect.Struct, reflect.Map:
			return shapeObject, value
		case reflect.Slice:
			if value.Type().Elem().Kind() == reflect.Uint8 {
				return shapeLeaf, value
			}
			return shapeArray, value
		case reflect.Array:
			return shapeArray, value
		}
		return shapeLeaf, value
	}
}

func customEncoding(t reflect.Type) bool {
	if t == rawValueType {
		return true
	}
	return t.Implements(marshalerTo) || t.Implements(marshalerV1) ||
		t.Kind() != reflect.Pointer && (reflect.PointerTo(t).Implements(marshalerTo) || reflect.PointerTo(t).Implements(marshalerV1))
}

// sameEncoding says whether two leaves (or nulls) stand for the same JSON value.
func sameEncoding(a, b reflect.Value) (bool, error) {
	if a.IsValid() && b.IsValid() && a.Type() == b.Type() {
		switch a.Kind() {
		case reflect.String:
			return a.String() == b.String(), nil
		case reflect.Bool:
			return a.Bool() == b.Bool(), nil
		case reflect.Float32, reflect.Float64:
			x, y := a.Float(), b.Float()
			return x == y && math.Signbit(x) == math.Signbit(y) || math.IsNaN(x) && math.IsNaN(y), nil
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return a.Int() == b.Int(), nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return a.Uint() == b.Uint(), nil
		}
	}
	left, err := encodeValue(a)
	if err != nil {
		return false, err
	}
	right, err := encodeValue(b)
	if err != nil {
		return false, err
	}
	return string(left) == string(right), nil
}

func encodeValue(value reflect.Value) (jsontext.Value, error) {
	if !value.IsValid() {
		return jsontext.Value("null"), nil
	}
	encoded, err := jsonx.Marshal(value.Interface())
	if err != nil {
		return nil, fmt.Errorf("application: encoding a workspace patch: %w", err)
	}
	return jsontext.Value(encoded), nil
}

type objectMember struct {
	name  string
	value reflect.Value
}

// objectMembers is the members value encodes, in order: a struct's fields as written (omitzero ones
// that are zero left out), a map's entries by key.
func objectMembers(value reflect.Value) []objectMember {
	if value.Kind() == reflect.Map {
		return mapMembers(value)
	}
	var members []objectMember
	for _, field := range jsonFieldsOf(value.Type()) {
		held := value.Field(field.index)
		if field.inline {
			if shape, inner := shapeOf(held); shape == shapeObject {
				members = append(members, objectMembers(inner)...)
			}
			continue
		}
		if field.omitzero && isZeroValue(held) {
			continue
		}
		if field.omitempty && emptyValue(held) {
			continue
		}
		members = append(members, objectMember{name: field.name, value: held})
	}
	return members
}

func mapMembers(value reflect.Value) []objectMember {
	members := make([]objectMember, 0, value.Len())
	iterator := value.MapRange()
	for iterator.Next() {
		key := iterator.Key()
		var name string
		if key.Kind() == reflect.String {
			name = key.String()
		} else {
			name = fmt.Sprint(key.Interface())
		}
		members = append(members, objectMember{name: name, value: iterator.Value()})
	}
	slices.SortFunc(members, func(a, b objectMember) int { return strings.Compare(a.name, b.name) })
	return members
}

func isZeroValue(value reflect.Value) bool {
	if zeroer, ok := value.Interface().(interface{ IsZero() bool }); ok {
		if value.Kind() == reflect.Pointer && value.IsNil() {
			return true
		}
		return zeroer.IsZero()
	}
	return value.IsZero()
}

func emptyValue(value reflect.Value) bool {
	shape, inner := shapeOf(value)
	switch shape {
	case shapeNull:
		return true
	case shapeArray, shapeObject:
		return inner.Kind() != reflect.Struct && inner.Len() == 0
	}
	return inner.Kind() == reflect.String && inner.Len() == 0
}

type jsonField struct {
	name      string
	index     int
	omitzero  bool
	omitempty bool
	inline    bool
}

var jsonFields sync.Map // reflect.Type -> []jsonField

func jsonFieldsOf(t reflect.Type) []jsonField {
	if held, ok := jsonFields.Load(t); ok {
		return held.([]jsonField)
	}
	var fields []jsonField
	for index := range t.NumField() {
		field := t.Field(index)
		tag, tagged := field.Tag.Lookup("json")
		if tag == "-" {
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		described := jsonField{index: index, name: name}
		for option := range strings.SplitSeq(options, ",") {
			switch option {
			case "omitzero":
				described.omitzero = true
			case "omitempty":
				described.omitempty = true
			case "inline":
				described.inline = true
			}
		}
		if field.Anonymous && (!tagged || name == "") {
			described.inline = true
		}
		if !field.IsExported() && !described.inline {
			continue
		}
		if described.name == "" {
			described.name = field.Name
		} else if unquoted, err := strconv.Unquote(described.name); err == nil && strings.HasPrefix(described.name, "'") {
			described.name = unquoted
		}
		fields = append(fields, described)
	}
	jsonFields.Store(t, fields)
	return fields
}

// ApplyWorkspacePatches applies patches to a state held as decoded JSON (map[string]any, []any,
// scalars), copying each changed branch once and sharing the rest. state itself is not modified.
func ApplyWorkspacePatches(state any, patches []*contracts.WorkspacePatch) (any, error) {
	applier := patchApplier{copies: map[any]any{}, owned: map[any]bool{}}
	result := state
	for _, patch := range patches {
		if len(patch.Path) == 0 {
			if patch.Splice != nil {
				spliced, err := applier.spliced(result, patch.Splice)
				if err != nil {
					return nil, err
				}
				result = spliced
				continue
			}
			value, err := decodePatchValue(patch.Value)
			if err != nil {
				return nil, err
			}
			result = value
			continue
		}
		parent, err := applier.writable(result)
		if err != nil {
			return nil, err
		}
		result = parent
		for _, segment := range patch.Path[:len(patch.Path)-1] {
			if !segment.IsIndex && segment.Key == "__proto__" {
				return nil, errInvalidPatchKey
			}
			held, err := applier.child(parent, segment)
			if err != nil {
				return nil, err
			}
			child, err := applier.writable(held)
			if err != nil {
				return nil, err
			}
			applier.assign(parent, segment, child)
			parent = child
		}
		key := patch.Path[len(patch.Path)-1]
		if !key.IsIndex && key.Key == "__proto__" {
			return nil, errInvalidPatchKey
		}
		switch {
		case patch.Splice != nil:
			held, err := applier.child(parent, key)
			if err != nil {
				return nil, err
			}
			spliced, err := applier.spliced(held, patch.Splice)
			if err != nil {
				return nil, err
			}
			applier.assign(parent, key, spliced)
		case patch.Remove != nil && *patch.Remove:
			if err := applier.remove(parent, key); err != nil {
				return nil, err
			}
		default:
			value, err := decodePatchValue(patch.Value)
			if err != nil {
				return nil, err
			}
			if err := applier.set(parent, key, value); err != nil {
				return nil, err
			}
		}
	}
	return applier.unboxed(result), nil
}

var (
	errInvalidPatchPath   = errors.New("Invalid workspace patch path.")
	errInvalidPatchKey    = errors.New("Invalid workspace patch key.")
	errInvalidPatchSplice = errors.New("Invalid workspace array splice.")
)

// arrayBox holds an array the patches own while they run, so a splice that resizes it stays in place.
type arrayBox struct{ items []any }

// patchApplier copies each container the patches reach once, by identity: copies maps an original
// to its copy, and owned holds the copies (maps by identity, arrays boxed).
type patchApplier struct {
	copies map[any]any
	owned  map[any]bool
}

type sliceIdentity struct {
	data   unsafe.Pointer
	length int
}

func containerIdentity(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return reflect.ValueOf(value).UnsafePointer()
	case []any:
		return sliceIdentity{data: reflect.ValueOf(value).UnsafePointer(), length: len(value)}
	case *arrayBox:
		return value
	}
	return nil
}

func (a *patchApplier) writable(value any) (any, error) {
	if !container(value) {
		if box, ok := value.(*arrayBox); ok {
			return box, nil
		}
		return nil, errInvalidPatchPath
	}
	// Empty slices and nil maps share one address, so they have no identity to remember a copy by.
	identity := containerIdentity(value)
	if reflect.ValueOf(value).Len() == 0 && reflect.ValueOf(value).Kind() == reflect.Slice || reflect.ValueOf(value).IsNil() {
		identity = nil
	}
	if identity != nil && a.owned[identity] {
		return value, nil
	}
	if held, ok := a.copies[identity]; ok && identity != nil {
		return held, nil
	}
	var copied any
	switch value := value.(type) {
	case map[string]any:
		fresh := make(map[string]any, len(value)+1)
		for key, item := range value {
			fresh[key] = item
		}
		copied = fresh
	case []any:
		copied = &arrayBox{items: slices.Clone(value)}
	}
	if identity != nil {
		a.copies[identity] = copied
	}
	a.owned[containerIdentity(copied)] = true
	return copied, nil
}

// unboxed is value with every array the patches own put back as a plain []any.
func (a *patchApplier) unboxed(value any) any {
	switch held := value.(type) {
	case *arrayBox:
		for index, item := range held.items {
			held.items[index] = a.unboxed(item)
		}
		if held.items == nil {
			return []any{}
		}
		return held.items
	case map[string]any:
		if !a.owned[containerIdentity(held)] {
			return value
		}
		for key, item := range held {
			if _, boxed := item.(*arrayBox); boxed || container(item) && a.owned[containerIdentity(item)] {
				held[key] = a.unboxed(item)
			}
		}
	}
	return value
}

func (a *patchApplier) child(parent any, key contracts.PathKey) (any, error) {
	switch parent := parent.(type) {
	case map[string]any:
		name := key.String()
		if name == "__proto__" {
			return nil, errInvalidPatchKey
		}
		child, ok := parent[name]
		if !ok {
			return nil, errInvalidPatchKey
		}
		return child, nil
	case *arrayBox:
		index, ok := arrayIndex(key, len(parent.items))
		if !ok {
			return nil, errInvalidPatchKey
		}
		return parent.items[index], nil
	}
	return nil, errInvalidPatchPath
}

func (a *patchApplier) assign(parent any, key contracts.PathKey, value any) {
	switch parent := parent.(type) {
	case map[string]any:
		parent[key.String()] = value
	case *arrayBox:
		if index, ok := arrayIndex(key, len(parent.items)); ok {
			parent.items[index] = value
		}
	}
}

func (a *patchApplier) set(parent any, key contracts.PathKey, value any) error {
	switch parent := parent.(type) {
	case map[string]any:
		parent[key.String()] = value
		return nil
	case *arrayBox:
		if !key.IsIndex || key.Index < 0 || key.Index != math.Trunc(key.Index) || key.Index > float64(math.MaxInt32) {
			return errInvalidPatchKey
		}
		index := int(key.Index)
		for len(parent.items) <= index {
			parent.items = append(parent.items, nil)
		}
		parent.items[index] = value
		return nil
	}
	return errInvalidPatchPath
}

func (a *patchApplier) remove(parent any, key contracts.PathKey) error {
	switch parent := parent.(type) {
	case map[string]any:
		delete(parent, key.String())
		return nil
	case *arrayBox:
		if index, ok := arrayIndex(key, len(parent.items)); ok {
			parent.items[index] = nil
		}
		return nil
	}
	return errInvalidPatchPath
}

func arrayIndex(key contracts.PathKey, length int) (int, bool) {
	index := key.Index
	if !key.IsIndex {
		parsed, err := strconv.ParseFloat(key.Key, 64)
		if err != nil || strconv.FormatFloat(parsed, 'g', -1, 64) != key.Key {
			return 0, false
		}
		index = parsed
	}
	if index < 0 || index != math.Trunc(index) || index >= float64(length) {
		return 0, false
	}
	return int(index), true
}

func (a *patchApplier) spliced(value any, operation *contracts.WorkspaceSplice) (any, error) {
	target, err := a.writable(value)
	if err != nil {
		return nil, err
	}
	box, ok := target.(*arrayBox)
	const safe = 1<<53 - 1
	index, deleteCount := operation.Index, operation.DeleteCount
	if !ok || index != math.Trunc(index) || deleteCount != math.Trunc(deleteCount) || math.Abs(index) > safe || math.Abs(deleteCount) > safe ||
		index < 0 || index > float64(len(box.items)) || deleteCount < 0 || deleteCount > float64(len(box.items))-index || operation.Items == nil {
		return nil, errInvalidPatchSplice
	}
	items := make([]any, len(operation.Items))
	for offset, raw := range operation.Items {
		if items[offset], err = decodePatchValue(raw); err != nil {
			return nil, err
		}
	}
	box.items = slices.Replace(box.items, int(index), int(index)+int(deleteCount), items...)
	return box, nil
}

func decodePatchValue(raw jsontext.Value) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value any
	if err := jsonx.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}
