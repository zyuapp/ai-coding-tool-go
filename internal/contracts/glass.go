package contracts

import "slices"

// How many panes of glass one window may ask for.
const maxGlassRegions = 32

func IsGlassRegions(value any) bool {
	regions, ok := value.([]any)
	return ok && len(regions) <= maxGlassRegions && !slices.ContainsFunc(regions, func(value any) bool {
		region, ok := ipcRecord(value)
		return !ok || !ipcFinite(region["x"]) || !ipcFinite(region["y"]) || !ipcFinite(region["width"]) || !ipcFinite(region["height"]) || !ipcFinite(region["radius"]) ||
			ipcNumber(region["width"]) < 0 || ipcNumber(region["height"]) < 0 || ipcNumber(region["radius"]) < 0
	})
}
