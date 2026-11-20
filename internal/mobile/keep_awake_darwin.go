package mobile

import "github.com/zyuapp/ai-coding-tool-go/internal/platform"

// holdAwake keeps the Mac from idle sleep until the returned release.
func holdAwake() (release func()) {
	blocker := platform.PowerBlocker()
	id := blocker.Start()
	return func() {
		if blocker.IsStarted(id) {
			blocker.Stop(id)
		}
	}
}
