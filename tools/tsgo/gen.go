package main

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const module = "github.com/zyuapp/ai-coding-tool-go/internal"

var layerOrder = map[string]int{"domain": 0, "contracts": 1, "application": 2, "host": 3}

// goRef names a Go type the generator defines: its package, name, and the file it is written in.
type goRef struct {
	layer, name string
	file        *file
}

func (r goRef) key() string { return r.layer + "." + r.name }

type file struct {
	ts, layer, out string
	imports        map[string]bool
	body           bytes.Buffer
	methods        map[string]bool
	registers      []string
	consts         []string
}

type unionInfo struct {
	ref    goRef
	marker string
}

type gen struct {
	decls        map[string]*decl
	canonical    map[string]string // duplicate decl → the identical one emitted
	declName     map[string]string // decl → Go name
	used         map[string]map[string]bool
	files        map[string]*file
	objects      map[string]goRef // type literal id → its struct
	lits         map[string]string
	unions       map[string]*unionInfo // goRef.key → union
	markers      map[string]string     // union name → marker method, decided up front
	ports        map[string]bool       // goRef.key of interfaces with methods
	emitted      map[string]bool
	plans        map[string]*plan
	memberShapes map[string]goRef           // anonymous union member → its struct, shared by every union it is in
	supers       map[string]map[string]bool // union goRef.key → markers of the unions it is nested in
	problems     []string
	cur          *file
}

func newGen(tree *ir) *gen {
	g := &gen{
		decls: map[string]*decl{}, canonical: map[string]string{}, declName: map[string]string{},
		used: map[string]map[string]bool{}, files: map[string]*file{}, objects: map[string]goRef{},
		lits: map[string]string{}, unions: map[string]*unionInfo{}, markers: map[string]string{},
		ports: map[string]bool{}, emitted: map[string]bool{}, supers: map[string]map[string]bool{}, plans: map[string]*plan{}, memberShapes: map[string]goRef{},
	}
	for _, l := range []string{"domain", "contracts", "application", "host"} {
		g.used[l] = map[string]bool{}
	}
	slices.SortStableFunc(tree.Decls, func(a, b *decl) int {
		la, lb := layerOrder[layerOf(a.File)], layerOrder[layerOf(b.File)]
		if la != lb {
			return la - lb
		}
		if a.File != b.File {
			return strings.Compare(a.File, b.File)
		}
		return a.Pos - b.Pos
	})
	first := map[string]*decl{}
	for _, d := range tree.Decls {
		g.decls[d.key()] = d
		layer := layerOf(d.File)
		name := typeName(d.Name)
		if prev, ok := first[layer+"."+name]; ok {
			if shapeWith(prev.T, g.canonical) == shapeWith(d.T, g.canonical) {
				g.canonical[d.key()] = prev.key()
				continue
			}
			name = pascal(strings.TrimSuffix(d.File[len(layer)+1:], ".ts")) + name
		}
		first[layer+"."+name] = d
		g.used[layer][name] = true
		g.declName[d.key()] = name
	}
	g.planMarkers(tree.Decls)
	return g
}

// planMarkers names each union's marker method once: Is<Name>, with the layer in it where two layers share a name.
func (g *gen) planMarkers(decls []*decl) {
	layers := map[string]map[string]bool{}
	for _, d := range decls {
		name := g.declName[d.key()]
		if layers[name] == nil {
			layers[name] = map[string]bool{}
		}
		layers[name][layerOf(d.File)] = true
	}
	for name, ls := range layers {
		if len(ls) > 1 {
			g.markers[name] = "layered"
		}
	}
}

func (g *gen) marker(ref goRef) string {
	if g.markers[ref.name] == "layered" && ref.layer != "domain" {
		return "Is" + pascal(ref.layer) + ref.name
	}
	return "Is" + ref.name
}

func shape(n *node) string { return shapeWith(n, nil) }

// shapeWith is a node's structure without ids and comments, with references to duplicates
// replaced by the declarations they duplicate.
func shapeWith(n *node, canonical map[string]string) string {
	data, _ := json.Marshal(n)
	var v any
	_ = json.Unmarshal(data, &v)
	var strip func(any) any
	strip = func(v any) any {
		switch v := v.(type) {
		case map[string]any:
			delete(v, "id")
			delete(v, "doc")
			if v["k"] == "ref" {
				if c, ok := canonical[fmt.Sprint(v["file"], "#", v["name"])]; ok {
					file, name, _ := strings.Cut(c, "#")
					v["file"], v["name"] = file, name
				}
			}
			for k, x := range v {
				v[k] = strip(x)
			}
		case []any:
			for i, x := range v {
				v[i] = strip(x)
			}
		}
		return v
	}
	out, _ := json.Marshal(strip(v), json.Deterministic(true))
	return string(out)
}

func layerOf(tsFile string) string { return strings.SplitN(tsFile, "/", 2)[0] }

func (g *gen) problem(format string, args ...any) {
	g.problems = append(g.problems, fmt.Sprintf(format, args...))
}

func (g *gen) fileFor(ts string) *file {
	if f, ok := g.files[ts]; ok {
		return f
	}
	layer, out := goFile(ts)
	f := &file{ts: ts, layer: layer, out: out, imports: map[string]bool{}, methods: map[string]bool{}}
	g.files[ts] = f
	return f
}

// fresh claims a name in the current package for a type the generator derives.
func (g *gen) fresh(want string) string {
	used := g.used[g.cur.layer]
	name := want
	for i := 2; used[name]; i++ {
		name = want + strconv.Itoa(i)
	}
	used[name] = true
	return name
}

func (g *gen) use(pkg string) { g.cur.imports[pkg] = true }

func (g *gen) qualify(ref goRef) string {
	if ref.layer == g.cur.layer {
		return ref.name
	}
	g.use(module + "/" + ref.layer)
	return ref.layer + "." + ref.name
}

// ----- resolving references -----

func (g *gen) lookup(n *node) *decl {
	key := n.File + "#" + n.Name
	if c, ok := g.canonical[key]; ok {
		key = c
	}
	return g.decls[key]
}

func (g *gen) declRef(d *decl) goRef {
	key := d.key()
	if c, ok := g.canonical[key]; ok {
		d = g.decls[c]
		key = c
	}
	return goRef{layer: layerOf(d.File), name: g.declName[key], file: g.fileFor(d.File)}
}

// target follows references through aliases to the type a name finally stands for.
func (g *gen) target(n *node) (*node, *decl) {
	var last *decl
	for i := 0; n != nil && n.K == "ref" && i < 32; i++ {
		d := g.lookup(n)
		if d == nil || d.Generic != nil || manualDecls[d.key()] != "" {
			return n, d
		}
		last = d
		n = d.T
	}
	return n, last
}

// ----- unions -----

type unionKind int

const (
	uSingle unionKind = iota
	uNull
	uEnum
	uString
	uNumber
	uBool
	uObjects
	uMixed
)

type analysis struct {
	kind     unionKind
	nullable bool
	optional bool
	single   *node
	members  []*node // without null and undefined
	values   []any   // enum values
}

func flatten(ms []*node) []*node {
	var out []*node
	for _, m := range ms {
		if m.K == "union" {
			out = append(out, flatten(m.Ms)...)
		} else {
			out = append(out, m)
		}
	}
	return out
}

func (g *gen) analyze(n *node) analysis {
	if n.K != "union" {
		if n.isPrim("null") {
			return analysis{kind: uNull, nullable: true}
		}
		if n.isPrim("undefined") {
			return analysis{kind: uNull, optional: true}
		}
		return analysis{kind: uSingle, single: n}
	}
	var a analysis
	for _, m := range flatten(n.Ms) {
		switch {
		case m.isPrim("null"):
			a.nullable = true
		case m.isPrim("undefined"):
			a.optional = true
		case m.isPrim("never"):
		default:
			if !slices.ContainsFunc(a.members, func(x *node) bool { return shape(x) == shape(m) }) {
				a.members = append(a.members, m)
			}
		}
	}
	// A value or a promise of it is the promise: a Go func may answer at once.
	if i := slices.IndexFunc(a.members, func(m *node) bool { return m.K == "promise" }); i >= 0 {
		a.members = []*node{a.members[i]}
	}
	switch len(a.members) {
	case 0:
		a.kind = uNull
		return a
	case 1:
		a.kind, a.single = uSingle, a.members[0]
		return a
	}
	classes := map[string]bool{}
	var values []any
	enumOnly := true
	for _, m := range a.members {
		c, vs := g.class(m)
		classes[c] = true
		if vs == nil || c != "string" {
			enumOnly = false
		}
		values = append(values, vs...)
	}
	switch {
	case len(classes) == 1 && classes["string"] && enumOnly:
		a.kind, a.values = uEnum, dedupe(values)
	case len(classes) == 1 && classes["string"]:
		a.kind = uString
	case len(classes) == 1 && classes["number"]:
		a.kind = uNumber
	case len(classes) == 1 && classes["bool"]:
		a.kind = uBool
	case len(classes) == 1 && classes["object"]:
		a.kind = uObjects
	default:
		a.kind = uMixed
	}
	return a
}

func dedupe(values []any) []any {
	var out []any
	for _, v := range values {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// class says what JSON a member is: string, number, bool, object or other, with its literal values when it has only those.
func (g *gen) class(n *node) (string, []any) {
	t, d := g.target(n)
	if d != nil && manualDecls[d.key()] != "" {
		return "other", nil
	}
	switch t.K {
	case "lit":
		switch t.V.(type) {
		case string:
			return "string", []any{t.V}
		case float64:
			return "number", nil
		case bool:
			return "bool", nil
		}
	case "prim":
		switch t.P {
		case "string":
			return "string", nil
		case "number":
			return "number", nil
		case "boolean":
			return "bool", nil
		}
	case "object":
		if g.isPortObject(t) {
			return "other", nil
		}
		return "object", nil
	case "union":
		a := g.analyze(t)
		switch a.kind {
		case uEnum:
			return "string", a.values
		case uString:
			return "string", nil
		case uNumber:
			return "number", nil
		case uBool:
			return "bool", nil
		case uObjects:
			return "object", nil
		case uSingle:
			return g.class(a.single)
		}
	}
	return "other", nil
}

// member is one concrete struct of a discriminated union, with the nested unions it came through.
type member struct {
	n   *node
	via []*decl
}

func (g *gen) concrete(ms []*node, via []*decl) []*member {
	var out []*member
	for _, m := range flatten(ms) {
		if m.isPrim("null") || m.isPrim("undefined") || m.isPrim("never") {
			continue
		}
		t, d := g.target(m)
		if t.K == "union" {
			nested := via
			if d != nil {
				nested = append(slices.Clip(via), d)
			}
			out = append(out, g.concrete(t.Ms, nested)...)
			continue
		}
		out = append(out, &member{n: m, via: via})
	}
	return out
}

// props lists an object's properties, its embedded parts' included.
func (g *gen) props(n *node) []*prop {
	t, _ := g.target(n)
	if t.K != "object" {
		return nil
	}
	var out []*prop
	for _, e := range t.Embeds {
		out = append(out, g.props(e)...)
	}
	return append(out, t.Props...)
}

func (g *gen) literalValues(n *node) []any {
	t, _ := g.target(n)
	switch t.K {
	case "lit":
		return []any{t.V}
	case "union":
		a := g.analyze(t)
		if a.nullable || a.optional {
			return nil
		}
		switch a.kind {
		case uEnum:
			return a.values
		case uSingle:
			return g.literalValues(a.single)
		}
		var out []any
		for _, m := range a.members {
			vs := g.literalValues(m)
			if vs == nil {
				return nil
			}
			out = append(out, vs...)
		}
		return out
	}
	return nil
}

var preferredKeys = []string{"type", "kind", "status", "state", "ok", "result", "outcome", "access"}

// group is the members of a union that share one discriminator value, merged into one struct when more than one does.
type group struct {
	values  []any
	members []*member
	nested  *decl // the union these members make up on their own, decoded through it
}

// plan is how a union of objects becomes Go: an interface over members told apart by key, or,
// when no property tells them apart, one struct holding every member's fields.
type plan struct {
	key    string
	groups []*group
	merged bool
}

func (g *gen) plan(ms []*node) *plan {
	memo := shape(&node{K: "union", Ms: ms})
	if p, ok := g.plans[memo]; ok {
		return p
	}
	members := g.concrete(ms, nil)
	p := g.discriminate(members)
	g.plans[memo] = p
	return p
}

func (g *gen) discriminate(members []*member) *plan {
	counts := map[string]int{}
	for _, m := range members {
		for _, p := range g.props(m.n) {
			if !p.Opt {
				counts[p.Name]++
			}
		}
	}
	var keys []string
	for k, c := range counts {
		if c == len(members) {
			keys = append(keys, k)
		}
	}
	rank := func(k string) int {
		if i := slices.Index(preferredKeys, k); i >= 0 {
			return i
		}
		return len(preferredKeys)
	}
	sort.Slice(keys, func(i, j int) bool {
		if rank(keys[i]) != rank(keys[j]) {
			return rank(keys[i]) < rank(keys[j])
		}
		return keys[i] < keys[j]
	})
	valuesOf := func(m *member, key string) []any {
		for _, q := range g.props(m.n) {
			if q.Name == key {
				return g.literalValues(q.T)
			}
		}
		return nil
	}
	for _, grouping := range []bool{false, true} {
	next:
		for _, key := range keys {
			var groups []*group
			seen := map[any]*group{}
			for _, m := range members {
				vs := valuesOf(m, key)
				if len(vs) == 0 {
					continue next
				}
				if grouping && len(vs) == 1 {
					if gr, ok := seen[vs[0]]; ok && len(gr.values) == 1 {
						gr.members = append(gr.members, m)
						continue
					}
				}
				for _, v := range vs {
					if seen[v] != nil {
						continue next
					}
				}
				gr := &group{values: vs, members: []*member{m}}
				for _, v := range vs {
					seen[v] = gr
				}
				groups = append(groups, gr)
			}
			for _, gr := range groups {
				if len(gr.members) > 1 {
					gr.nested = g.nestedUnion(gr.members)
				}
			}
			return &plan{key: key, groups: groups}
		}
	}
	return &plan{merged: true, groups: []*group{{members: members}}}
}

// nestedUnion is the declared union exactly these members came through, if there is one.
func (g *gen) nestedUnion(members []*member) *decl {
	first := members[0].via
	for i := len(first) - 1; i >= 0; i-- {
		d := first[i]
		all := true
		for _, m := range members[1:] {
			if !slices.Contains(m.via, d) {
				all = false
			}
		}
		if t, _ := g.target(d.T); all && t.K == "union" && len(g.concrete(t.Ms, nil)) == len(members) {
			return d
		}
	}
	return nil
}

// existing is the struct already defined for a union member.
func (g *gen) existing(n *node) (goRef, bool) {
	t, d := g.target(n)
	if t.K != "object" {
		return goRef{}, false
	}
	if n.K == "ref" && d != nil {
		return g.declRef(d), true
	}
	if t.ID != "" {
		if prev, ok := g.objects[t.ID]; ok {
			return prev, true
		}
	}
	prev, ok := g.memberShapes[shape(t)]
	return prev, ok
}

// merge is one object with every member's properties: required only where every member requires
// it, typed as the union of what the members say.
func (g *gen) merge(members []*member) *node {
	var order []string
	types := map[string][]*node{}
	present := map[string]int{}
	optional := map[string]bool{}
	docs := map[string]string{}
	for _, m := range members {
		for _, p := range g.props(m.n) {
			if _, ok := types[p.Name]; !ok {
				order = append(order, p.Name)
			}
			present[p.Name]++
			if p.Opt {
				optional[p.Name] = true
			}
			if docs[p.Name] == "" {
				docs[p.Name] = p.Doc
			}
			if !slices.ContainsFunc(types[p.Name], func(x *node) bool { return shape(x) == shape(p.T) }) {
				types[p.Name] = append(types[p.Name], p.T)
			}
		}
	}
	out := &node{K: "object"}
	for _, name := range order {
		t := types[name][0]
		if len(types[name]) > 1 {
			t = &node{K: "union", Ms: types[name]}
		}
		out.Props = append(out.Props, &prop{Name: name, Opt: optional[name] || present[name] < len(members), Doc: docs[name], T: t})
	}
	return out
}

// ----- emitting -----

func (g *gen) emitDecl(d *decl) {
	if _, dup := g.canonical[d.key()]; dup || d.Generic != nil || manualDecls[d.key()] != "" {
		return
	}
	if g.emitted[d.key()] {
		return
	}
	g.emitted[d.key()] = true
	g.cur = g.fileFor(d.File)
	ref := g.declRef(d)
	name := ref.name
	t := d.T
	switch {
	case t.K == "ref":
		target := g.lookup(t)
		if target == nil {
			g.problem("%s: reference to unknown %s#%s", d.key(), t.File, t.Name)
			return
		}
		if manualDecls[target.key()] == "" && target.Generic == nil {
			tr := g.declRef(target)
			if tr.key() == ref.key() {
				if target.key() == d.key() {
					g.problem("%s: refers to itself", d.key())
				}
				return
			}
			g.writeDoc(d.Doc)
			fmt.Fprintf(&g.cur.body, "type %s = %s\n\n", name, g.qualify(tr))
			return
		}
		g.writeDoc(d.Doc)
		fmt.Fprintf(&g.cur.body, "type %s = %s\n\n", name, g.goType(t, name))
	case t.K == "object":
		if prev, ok := g.memberShapes[shape(t)]; ok && t.ID == "" {
			g.writeDoc(d.Doc)
			fmt.Fprintf(&g.cur.body, "type %s = %s\n\n", name, g.qualify(prev))
			return
		}
		g.defineObject(t, name, d.Doc)
	case t.K == "union":
		a := g.analyze(t)
		switch a.kind {
		case uEnum:
			g.defineEnum(name, d.Doc, a.values)
		case uObjects:
			g.defineUnion(name, d.Doc, a.members)
		case uMixed:
			g.problem("%s: union TypeScript does not discriminate; add it to manualDecls", d.key())
		default:
			hint := name
			if a.nullable || a.optional {
				hint = name + "Value"
			}
			typ := g.fieldType(t, hint, false, false)
			g.writeDoc(d.Doc)
			fmt.Fprintf(&g.cur.body, "type %s = %s\n\n", name, typ)
		}
	case t.K == "lit":
		if _, ok := t.V.(string); ok {
			g.defineEnum(name, d.Doc, []any{t.V})
			return
		}
		g.writeDoc(d.Doc)
		fmt.Fprintf(&g.cur.body, "type %s = %s\n\n", name, g.goType(t, name))
	case t.K == "func":
		g.writeDoc(d.Doc)
		fmt.Fprintf(&g.cur.body, "type %s %s\n\n", name, g.funcType(t, name))
	default:
		g.writeDoc(d.Doc)
		fmt.Fprintf(&g.cur.body, "type %s = %s\n\n", name, g.goType(t, name))
	}
}

// ensure emits a declaration now, so the unions nested in another name their members first.
func (g *gen) ensure(d *decl) {
	if g.emitted[d.key()] {
		return
	}
	saved := g.cur
	g.emitDecl(d)
	g.cur = saved
}

func (g *gen) writeDoc(doc string) {
	if doc == "" {
		return
	}
	for _, line := range strings.Split(doc, "\n") {
		fmt.Fprintf(&g.cur.body, "// %s\n", strings.TrimRight(line, " "))
	}
}

func (g *gen) isPortObject(t *node) bool {
	if t.K != "object" || len(t.Props) == 0 || len(t.Embeds) > 0 {
		return false
	}
	for _, p := range t.Props {
		if p.Opt || !p.Method && strip(p.T).K != "func" {
			return false
		}
	}
	return true
}

// strip drops undefined from a union, for function-typed members.
func strip(n *node) *node {
	if n.K != "union" {
		return n
	}
	var keep []*node
	for _, m := range flatten(n.Ms) {
		if !m.isPrim("undefined") {
			keep = append(keep, m)
		}
	}
	if len(keep) == 1 {
		return keep[0]
	}
	return &node{K: "union", Ms: keep}
}

func (g *gen) defineObject(t *node, name, doc string) goRef {
	ref := goRef{layer: g.cur.layer, name: name, file: g.cur}
	if t.ID != "" {
		if prev, ok := g.objects[t.ID]; ok && prev.name != name {
			g.writeDoc(doc)
			fmt.Fprintf(&g.cur.body, "type %s = %s\n\n", name, g.qualify(prev))
			return prev
		}
		g.objects[t.ID] = ref
	}
	var body bytes.Buffer
	if g.isPortObject(t) {
		g.ports[ref.key()] = true
		for _, p := range t.Props {
			if p.Doc != "" {
				for _, line := range strings.Split(p.Doc, "\n") {
					fmt.Fprintf(&body, "// %s\n", line)
				}
			}
			fmt.Fprintf(&body, "%s%s\n", pascal(p.Name), strings.TrimPrefix(g.funcType(strip(p.T), name+pascal(p.Name)), "func"))
		}
		g.writeDoc(doc)
		fmt.Fprintf(&g.cur.body, "type %s interface {\n%s}\n\n", name, body.String())
		return ref
	}
	fields := map[string]bool{}
	for _, e := range t.Embeds {
		target := g.lookup(e)
		if target == nil {
			g.problem("%s: embeds unknown %s", name, e.Name)
			continue
		}
		er := g.declRef(target)
		fields[er.name] = true
		fmt.Fprintf(&body, "%s `json:\",inline\"`\n", g.qualify(er))
	}
	for _, p := range t.Props {
		if a := g.analyze(p.T); !p.Method && (p.T.isPrim("never") || a.kind == uNull && !a.nullable) {
			continue
		}
		field := pascal(p.Name)
		for fields[field] {
			field += "_"
		}
		fields[field] = true
		if p.Doc != "" {
			for _, line := range strings.Split(p.Doc, "\n") {
				fmt.Fprintf(&body, "// %s\n", line)
			}
		}
		typ, tag := g.field(p, name)
		fmt.Fprintf(&body, "%s %s `json:\"%s\"`\n", field, typ, tag)
	}
	g.writeDoc(doc)
	fmt.Fprintf(&g.cur.body, "type %s struct {\n%s}\n\n", name, body.String())
	return ref
}

// field is a property's Go type and json tag, following the JSON rules in ARCHITECTURE.md.
func (g *gen) field(p *prop, parent string) (string, string) {
	hint := parent + pascal(p.Name)
	name := p.Name
	if strings.ContainsAny(name, "\",`\\") || name == "-" {
		name = "'" + name + "'"
	}
	t := p.T
	if p.Method {
		return g.funcType(t, hint), "-"
	}
	a := g.analyze(t)
	optional := p.Opt || a.optional
	base := t
	if t.K == "union" {
		switch len(a.members) {
		case 0:
			base = &node{K: "prim", P: "null"}
		case 1:
			base = a.members[0]
		default:
			base = &node{K: "union", Ms: a.members}
		}
	}
	if base.K == "func" || base.K == "promise" {
		return g.goType(base, hint), "-"
	}
	if tb, _ := g.target(base); tb.K == "object" && g.isPortObject(tb) {
		return g.goType(base, hint), "-"
	}
	if base.K == "lit" && !optional && !a.nullable {
		g.use("github.com/zyuapp/ai-coding-tool-go/internal/jsonx")
		g.use("github.com/zyuapp/ai-coding-tool-go/internal/jsonx/lit")
		return "jsonx.Tag[lit." + g.lit(base.V) + "]", name
	}
	typ := g.fieldType(base, hint, optional, a.nullable)
	if optional || typ == "jsontext.Value" {
		return typ, name + ",omitzero"
	}
	return typ, name
}

// fieldType applies optional and nullable to a base type: pointers for scalars and structs,
// jsonx.Opt for both at once, nothing for types that are already nil-able.
func (g *gen) fieldType(base *node, hint string, optional, nullable bool) string {
	if base.K == "union" {
		a := g.analyze(base)
		nullable = nullable || a.nullable
		optional = optional || a.optional
		if a.kind == uSingle {
			base = a.single
		} else if a.kind == uNull {
			return "jsontext.Value"
		}
	}
	typ := g.goType(base, hint)
	nilable := g.nilable(base)
	switch {
	case nullable && !optional && strings.HasPrefix(typ, "[]"):
		g.use("github.com/zyuapp/ai-coding-tool-go/internal/jsonx")
		return "jsonx.NullSlice[" + typ[2:] + "]"
	case nullable && !optional && strings.HasPrefix(typ, "map["):
		g.use("github.com/zyuapp/ai-coding-tool-go/internal/jsonx")
		k, v := splitMap(typ)
		return "jsonx.NullMap[" + k + ", " + v + "]"
	case optional && nullable:
		g.use("github.com/zyuapp/ai-coding-tool-go/internal/jsonx")
		return "jsonx.Opt[" + typ + "]"
	case (optional || nullable) && !nilable:
		return "*" + typ
	}
	return typ
}

func (g *gen) nilable(n *node) bool {
	t, d := g.target(n)
	if d != nil && manualDecls[d.key()] != "" {
		return manualNilable[d.key()]
	}
	switch t.K {
	case "prim":
		return t.P == "unknown" || t.P == "null"
	case "array", "map", "jsmap", "set", "func", "promise", "external":
		return true
	case "object":
		return g.isPortObject(t)
	case "union":
		a := g.analyze(t)
		switch a.kind {
		case uObjects:
			return !g.plan(a.members).merged
		case uMixed, uNull:
			return true
		case uSingle:
			return a.nullable || g.nilable(a.single)
		}
	}
	return false
}

func (g *gen) lit(v any) string {
	text := literalJSON(v)
	g.lits[text] = ""
	return "\x00LIT:" + text + "\x00"
}

// goType is the Go type for a node, defining named types for anonymous objects and unions under hint.
func (g *gen) goType(n *node, hint string) string {
	switch n.K {
	case "prim":
		switch n.P {
		case "string":
			return "string"
		case "number":
			return "float64"
		case "boolean":
			return "bool"
		case "undefined", "never":
			return "struct{}"
		}
		g.use("encoding/json/jsontext")
		return "jsontext.Value"
	case "lit":
		switch n.V.(type) {
		case string:
			return "string"
		case float64:
			return "float64"
		}
		return "bool"
	case "ref":
		d := g.lookup(n)
		if d == nil {
			g.problem("%s: reference to unknown %s#%s", hint, n.File, n.Name)
			return "any"
		}
		if d.Generic != nil {
			g.problem("%s: reference to generic %s", hint, d.key())
			return "any"
		}
		return g.qualify(g.declRef(d))
	case "array":
		return "[]" + g.elemType(n.E, hint)
	case "tuple":
		first := g.elemType(n.Es[0], hint)
		for _, e := range n.Es[1:] {
			if g.elemType(e, hint) != first {
				g.problem("%s: tuple of mixed types", hint)
				g.use("encoding/json/jsontext")
				return "jsontext.Value"
			}
		}
		return "[" + strconv.Itoa(len(n.Es)) + "]" + first
	case "map", "jsmap":
		return "map[" + g.keyType(n.Key, hint+"Key") + "]" + g.elemType(n.Val, hint)
	case "set":
		elem := g.elemType(n.E, hint)
		if c, _ := g.class(n.E); c == "string" || c == "number" {
			g.use("github.com/zyuapp/ai-coding-tool-go/internal/jsonx")
			return "jsonx.Set[" + elem + "]"
		}
		return "map[" + elem + "]struct{}"
	case "promise":
		g.use("context")
		res := g.results(n, hint)
		return "func(ctx context.Context) " + res
	case "func":
		return g.funcType(n, hint)
	case "external":
		switch n.Name {
		case "Uint8Array", "Buffer":
			return "[]byte"
		case "AbortSignal":
			g.use("context")
			return "context.Context"
		}
		return "any"
	case "object":
		if n.ID != "" {
			if prev, ok := g.objects[n.ID]; ok {
				return g.qualify(prev)
			}
		}
		saved := g.cur.body
		g.cur.body = bytes.Buffer{}
		ref := g.defineObject(n, g.fresh(hint), "")
		nested := g.cur.body
		g.cur.body = saved
		g.cur.deferred(nested.Bytes())
		return g.qualify(ref)
	case "union":
		a := g.analyze(n)
		switch a.kind {
		case uNull:
			g.use("encoding/json/jsontext")
			return "jsontext.Value"
		case uSingle:
			if a.nullable && !g.nilable(a.single) {
				return "*" + g.goType(a.single, hint)
			}
			return g.goType(a.single, hint)
		case uString:
			return "string"
		case uNumber:
			return "float64"
		case uBool:
			return "bool"
		case uEnum:
			if len(a.members) == 1 || g.sameEnum(a) != "" {
				if name := g.sameEnum(a); name != "" {
					return name
				}
			}
			return g.deferDefine(func(name string) { g.defineEnum(name, "", a.values) }, hint)
		case uObjects:
			if one := g.sameUnion(a); one != "" {
				return one
			}
			return g.deferDefine(func(name string) { g.defineUnion(name, "", a.members) }, hint)
		}
		if m, ok := manualUnions[hint]; ok {
			return m
		}
		g.problem("%s: union TypeScript does not discriminate; add it to manualUnions", hint)
		g.use("encoding/json/jsontext")
		return "jsontext.Value"
	}
	g.problem("%s: unknown node %s", hint, n.K)
	return "any"
}

// sameEnum is the declared enum a union is exactly, when it is one reference.
func (g *gen) sameEnum(a analysis) string {
	if len(a.members) == 1 && a.members[0].K == "ref" {
		return g.goType(a.members[0], "")
	}
	return ""
}

func (g *gen) sameUnion(a analysis) string {
	if len(a.members) == 1 && a.members[0].K == "ref" {
		return g.goType(a.members[0], "")
	}
	return ""
}

func (f *file) deferred(b []byte) { f.body.Write(b) }

func (g *gen) deferDefine(define func(name string), hint string) string {
	name := g.fresh(hint)
	saved := g.cur.body
	g.cur.body = bytes.Buffer{}
	define(name)
	nested := g.cur.body
	g.cur.body = saved
	g.cur.deferred(nested.Bytes())
	return name
}

// elemType is the type of an array element or map value. Records are held by pointer, so a
// copied collection shares the records it did not change; null makes a pointer too.
func (g *gen) elemType(n *node, hint string) string {
	a := g.analyze(n)
	if n.K == "union" && a.kind == uSingle {
		if g.isStruct(a.single) {
			return "*" + g.goType(a.single, hint)
		}
		return g.fieldType(a.single, hint, false, a.nullable)
	}
	if g.isStruct(n) {
		return "*" + g.goType(n, hint)
	}
	return g.goType(n, hint)
}

// isStruct says a node becomes a Go struct.
func (g *gen) isStruct(n *node) bool {
	t, d := g.target(n)
	if d != nil && manualDecls[d.key()] != "" {
		return false
	}
	switch t.K {
	case "object":
		return !g.isPortObject(t)
	case "union":
		a := g.analyze(t)
		if a.nullable || a.optional {
			return false
		}
		return a.kind == uObjects && g.plan(a.members).merged || a.kind == uSingle && g.isStruct(a.single)
	}
	return false
}

func (g *gen) keyType(n *node, hint string) string {
	switch c, _ := g.class(n); c {
	case "string":
		if t, _ := g.target(n); t.K == "union" || n.K == "ref" {
			return g.goType(n, hint)
		}
		return "string"
	case "number":
		return "float64"
	}
	return "string"
}

func (g *gen) results(promise *node, hint string) string {
	inner := strip(promise.E)
	if inner.isPrim("undefined") || inner.isPrim("never") {
		return "error"
	}
	return "(" + g.elemType(inner, hint+"Result") + ", error)"
}

// funcType is a Go func type for a TypeScript function. A function answering a promise takes a
// context and returns an error beside its value.
func (g *gen) funcType(f *node, hint string) string {
	if f.K != "func" {
		return g.goType(f, hint)
	}
	var params []string
	ret := strip(f.Ret)
	async := false
	if ret.K == "promise" {
		async = true
	} else if ret.K == "union" {
		for _, m := range flatten(ret.Ms) {
			if m.K == "promise" {
				async, ret = true, m
			}
		}
	}
	if async {
		g.use("context")
		params = append(params, "ctx context.Context")
	}
	for _, p := range f.Params {
		t := p.T
		var typ string
		a := g.analyze(t)
		switch {
		case t.isPrim("unknown"):
			typ = "any"
		case t.K == "external" && t.Name == "AbortSignal":
			continue
		default:
			base := t
			if t.K == "union" && a.kind == uSingle {
				base = a.single
			}
			typ = g.fieldType(base, hint+pascal(p.Name), p.Opt || a.optional, a.nullable)
		}
		params = append(params, paramName(p.Name)+" "+typ)
	}
	sig := "func(" + strings.Join(params, ", ") + ")"
	switch {
	case async:
		sig += " " + g.results(ret, hint)
	case ret.isPrim("undefined") || ret.isPrim("never"):
	case ret.isPrim("unknown"):
		sig += " any"
	default:
		a := g.analyze(ret)
		if ret.K == "union" && a.kind == uSingle {
			sig += " " + g.fieldType(a.single, hint+"Result", false, a.nullable)
		} else {
			sig += " " + g.goType(ret, hint+"Result")
		}
	}
	return sig
}

func (g *gen) defineEnum(name, doc string, values []any) {
	g.writeDoc(doc)
	fmt.Fprintf(&g.cur.body, "type %s string\n\n", name)
	var consts []string
	for _, v := range values {
		c := g.fresh(name + valueName(v))
		consts = append(consts, fmt.Sprintf("%s %s = %s", c, name, goLiteral(v)))
	}
	fmt.Fprintf(&g.cur.body, "const (\n%s\n)\n\n", strings.Join(consts, "\n"))
}

func (g *gen) defineUnion(name, doc string, ms []*node) {
	p := g.plan(ms)
	if p.merged {
		saved := g.cur.body
		g.cur.body = bytes.Buffer{}
		g.defineObject(g.merge(p.groups[0].members), name, doc)
		nested := g.cur.body
		g.cur.body = saved
		g.cur.deferred(nested.Bytes())
		return
	}
	for _, gr := range p.groups {
		for _, m := range gr.members {
			for _, d := range m.via {
				g.ensure(d)
			}
		}
	}
	ref := goRef{layer: g.cur.layer, name: name, file: g.cur}
	info := &unionInfo{ref: ref, marker: g.marker(ref)}
	g.unions[ref.key()] = info
	g.writeDoc(doc)
	fmt.Fprintf(&g.cur.body, "\x00UNION:%s\x00\n\n", ref.key())
	var cases, nested []string
	marked := map[*node]bool{}
	vias := map[*decl]bool{}
	mark := func(mref goRef, m *member) {
		mref.file.methods[fmt.Sprintf("func (%s) %s() {}", mref.name, info.marker)] = true
		marked[m.n] = true
		for _, d := range m.via {
			vias[d] = true
		}
	}
	for _, gr := range p.groups {
		if gr.nested != nil {
			g.ensure(gr.nested)
			for _, m := range gr.members {
				mref, ok := g.existing(m.n)
				if !ok {
					g.problem("%s: a member of %s has no struct", name, gr.nested.Name)
					continue
				}
				mark(mref, m)
			}
			for _, v := range gr.values {
				nested = append(nested, fmt.Sprintf("jsonx.RegisterNested[%s, %s](%s)", name, g.qualify(g.declRef(gr.nested)), goLiteral(v)))
			}
			continue
		}
		var mref goRef
		if len(gr.members) == 1 {
			if prev, ok := g.existing(gr.members[0].n); ok {
				mref = prev
			}
		}
		if mref.name == "" {
			t := g.merge(gr.members)
			if len(gr.members) == 1 {
				t, _ = g.target(gr.members[0].n)
			}
			if t.K != "object" {
				g.problem("%s: member is not an object", name)
				continue
			}
			saved := g.cur.body
			g.cur.body = bytes.Buffer{}
			doc := t.Doc
			if doc == "" && len(gr.members) > 1 {
				for _, m := range gr.members {
					if m.n.Doc != "" {
						doc = m.n.Doc
						break
					}
				}
			}
			mref = g.defineObject(t, g.fresh(name+valueName(gr.values[0])), doc)
			nested := g.cur.body
			g.cur.body = saved
			g.cur.deferred(nested.Bytes())
			if len(gr.members) == 1 {
				g.memberShapes[shape(t)] = mref
			}
		}
		if len(gr.members) == 1 {
			mark(mref, gr.members[0])
		} else {
			mref.file.methods[fmt.Sprintf("func (%s) %s() {}", mref.name, info.marker)] = true
		}
		for _, v := range gr.values {
			cases = append(cases, fmt.Sprintf("%s: %s{},", goLiteral(v), g.qualify(mref)))
		}
	}
	// A union nested in this one is one of it only when every member it has is.
	for d := range vias {
		t, _ := g.target(d.T)
		all := true
		for _, m := range g.concrete(t.Ms, nil) {
			if !marked[m.n] {
				all = false
			}
		}
		if !all {
			continue
		}
		via := g.declRef(d)
		if g.supers[via.key()] == nil {
			g.supers[via.key()] = map[string]bool{}
		}
		g.supers[via.key()][info.marker] = true
	}
	g.use("github.com/zyuapp/ai-coding-tool-go/internal/jsonx")
	g.cur.registers = append(g.cur.registers, fmt.Sprintf("jsonx.RegisterUnion[%s](%s, map[any]%s{\n%s\n})", name, strconv.Quote(p.key), name, strings.Join(cases, "\n")))
	g.cur.registers = append(g.cur.registers, nested...)
}

// splitMap splits "map[K]V" into K and V.
func splitMap(typ string) (string, string) {
	depth := 0
	for i := len("map["); i < len(typ); i++ {
		switch typ[i] {
		case '[':
			depth++
		case ']':
			if depth == 0 {
				return typ[len("map["):i], typ[i+1:]
			}
			depth--
		}
	}
	panic(typ)
}
