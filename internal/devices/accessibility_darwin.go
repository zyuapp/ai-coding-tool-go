package devices

/*
#cgo LDFLAGS: -framework ApplicationServices
#include <ApplicationServices/ApplicationServices.h>
*/
import "C"

// accessibilityTrusted is whether this app may read other apps through accessibility, without asking.
func accessibilityTrusted() bool { return C.AXIsProcessTrusted() != 0 }
