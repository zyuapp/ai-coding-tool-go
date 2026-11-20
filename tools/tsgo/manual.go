package main

import "strings"

// Declarations the generator leaves to a hand-written *_manual.go in their layer's package,
// keyed "<file under src>#<name>", with why.
var manualDecls = map[string]string{
	"contracts/ipc.ts#DesktopPorts":              "a type-level list of the port types, which Go composes by hand",
	"host/workspace-runtime.ts#WorkspaceRuntime": "inferred from createWorkspaceRuntime; the host's runtime is hand-written",
	"host/host-module.ts#HostModule":             "effect handlers keyed by effect type (host_module_manual.go)",
	"host/effect-host.ts#EffectHandler":          "generic over the effect type; one func over WorkspaceEffect (host_module_manual.go)",
	"host/effect-host.ts#EffectHandlers":         "generic mapped type; a map keyed by effect type (host_module_manual.go)",
}

// Hand-written declarations that are nil-able (slices, maps, interfaces), so optional fields
// of them carry no pointer.
var manualNilable = map[string]bool{}

// Unions TypeScript does not discriminate, by the Go name the generator would give them,
// mapped to the hand-written type (with MarshalJSONTo / UnmarshalJSONFrom) that stands in.
var manualUnions = map[string]string{
	"WorkspacePatchPath": "PathKey",
}

// Problems extract.mjs reports that the Go side covers.
func ignoredProblem(p string) bool {
	return strings.HasPrefix(p, "host/host-module.ts#HostModule.effects")
}
