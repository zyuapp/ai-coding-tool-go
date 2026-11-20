package tools

// McpToolName is how every engine names a tool an MCP server offers, and what the app's grants are written against.
func McpToolName(server, name string) string {
	return "mcp__" + server + "__" + name
}

// ReadOnlyToolNames is the names of a server's read-only tools, taken from the definitions so an
// allow list cannot drift from them.
func ReadOnlyToolNames(server string, definitions []DefinitionInfo) map[string]bool {
	names := map[string]bool{}
	for _, definition := range definitions {
		if definition.ReadOnly {
			names[McpToolName(server, definition.Name)] = true
		}
	}
	return names
}
