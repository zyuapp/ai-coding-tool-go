package devices

import "context"

// microphoneAccess is always granted: Linux asks no one.
func microphoneAccess(context.Context) bool { return true }
