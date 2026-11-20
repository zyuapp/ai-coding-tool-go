package main

// The tree extract.mjs writes: every exported type in the four layers, and the local types they reach.

type ir struct {
	Decls       []*decl  `json:"decls"`
	Problems    []string `json:"problems"`
	Diagnostics int      `json:"diagnostics"`
}

type decl struct {
	Name     string   `json:"name"`
	File     string   `json:"file"`
	Pos      int      `json:"pos"`
	Exported bool     `json:"exported"`
	Doc      string   `json:"doc,omitzero"`
	Generic  []string `json:"generic,omitzero"`
	T        *node    `json:"t"`
}

// node kinds: prim (P: string|number|boolean|unknown|null|undefined|never), lit (V), ref (Name, File),
// array (E), tuple (Es), map / jsmap (Key, Val), set (E), promise (E), func (Params, Ret),
// object (Props, Embeds, ID), union (Ms), external (Name).
type node struct {
	K      string   `json:"k"`
	P      string   `json:"t,omitzero"`
	V      any      `json:"v,omitzero"`
	Name   string   `json:"name,omitzero"`
	File   string   `json:"file,omitzero"`
	ID     string   `json:"id,omitzero"`
	Doc    string   `json:"doc,omitzero"`
	E      *node    `json:"e,omitzero"`
	Es     []*node  `json:"es,omitzero"`
	Key    *node    `json:"key,omitzero"`
	Val    *node    `json:"val,omitzero"`
	Ms     []*node  `json:"ms,omitzero"`
	Props  []*prop  `json:"props,omitzero"`
	Embeds []*node  `json:"embeds,omitzero"`
	Params []*param `json:"params,omitzero"`
	Ret    *node    `json:"ret,omitzero"`
}

type prop struct {
	Name   string `json:"name"`
	Opt    bool   `json:"opt,omitzero"`
	Method bool   `json:"method,omitzero"`
	Doc    string `json:"doc,omitzero"`
	T      *node  `json:"t"`
}

type param struct {
	Name string `json:"name"`
	Opt  bool   `json:"opt,omitzero"`
	T    *node  `json:"t"`
}

func (d *decl) key() string { return d.File + "#" + d.Name }

func (n *node) isPrim(p string) bool { return n != nil && n.K == "prim" && n.P == p }
