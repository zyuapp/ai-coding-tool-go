package main

import (
	"strconv"
	"strings"
	"unicode"
)

var initialisms = map[string]string{
	"id": "ID", "ids": "IDs", "url": "URL", "urls": "URLs", "uri": "URI", "http": "HTTP", "https": "HTTPS",
	"json": "JSON", "api": "API", "html": "HTML", "ui": "UI", "ip": "IP", "uuid": "UUID", "udid": "UDID",
	"cpu": "CPU", "mcp": "MCP", "os": "OS", "pty": "PTY", "sql": "SQL",
}

// words splits an identifier or a value such as "run.event" or "taskId" into its words.
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && len(cur) > 0 && (unicode.IsLower(cur[len(cur)-1]) || unicode.IsDigit(cur[len(cur)-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1])):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}

// pascal is an exported Go identifier for s, with Go's initialisms.
func pascal(s string) string {
	var b strings.Builder
	for _, w := range words(s) {
		if up, ok := initialisms[strings.ToLower(w)]; ok && (w == strings.ToLower(w) || w == strings.ToUpper(w) || len(w) > 1 && w[1:] == strings.ToLower(w[1:])) {
			b.WriteString(up)
			continue
		}
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	out := b.String()
	if out == "" {
		return "Empty"
	}
	if unicode.IsDigit([]rune(out)[0]) {
		return "N" + out
	}
	return out
}

// typeName is a Go name for a declared TypeScript type: its own name, first letter upper.
func typeName(s string) string {
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return pascal(string(r))
}

var keywords = map[string]bool{
	"break": true, "default": true, "func": true, "interface": true, "select": true, "case": true, "defer": true,
	"go": true, "map": true, "struct": true, "chan": true, "else": true, "goto": true, "package": true, "switch": true,
	"const": true, "fallthrough": true, "if": true, "range": true, "type": true, "continue": true, "for": true,
	"import": true, "return": true, "var": true, "ctx": true, "error": true, "string": true, "any": true, "bool": true,
}

// paramName is an unexported Go identifier for a parameter.
func paramName(s string) string {
	p := pascal(s)
	r := []rune(p)
	i := 0
	for i < len(r) && unicode.IsUpper(r[i]) && (i == 0 || i+1 >= len(r) || unicode.IsUpper(r[i+1])) {
		r[i] = unicode.ToLower(r[i])
		i++
	}
	out := string(r)
	if keywords[out] {
		return out + "_"
	}
	return out
}

// valueName is the part of a constant or member name that comes from a literal value.
func valueName(v any) string {
	switch v := v.(type) {
	case string:
		return pascal(v)
	case bool:
		if v {
			return "True"
		}
		return "False"
	case float64:
		s := strconv.FormatFloat(v, 'g', -1, 64)
		s = strings.NewReplacer("-", "Minus", ".", "_", "+", "").Replace(s)
		return "N" + s
	}
	return "Value"
}

// literalJSON is a literal's JSON text.
func literalJSON(v any) string {
	switch v := v.(type) {
	case string:
		return strconv.Quote(v)
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	panic("literal")
}

// goLiteral is a literal as a Go constant expression.
func goLiteral(v any) string {
	switch v := v.(type) {
	case string:
		return strconv.Quote(v)
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	panic("literal")
}

// goFile is the generated file for src/<layer>/<path>.ts: internal/<layer>/<path flattened>_types.go.
func goFile(tsFile string) (layer, name string) {
	parts := strings.Split(strings.TrimSuffix(tsFile, ".ts"), "/")
	layer = parts[0]
	rest := strings.Join(parts[1:], "_")
	rest = strings.ReplaceAll(rest, "-", "_")
	return layer, rest + "_types.go"
}
