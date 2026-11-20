//go:build !(darwin && cgo) && !(linux && cgo)

package browser

// platformEngine is nil where this build has no web view to draw: every read finds no page.
func platformEngine(Options) engine { return nil }
