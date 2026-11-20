//go:build linux && cgo

package desktop

/*
#cgo pkg-config: glib-2.0
#include <stdlib.h>
#include <glib.h>
*/
import "C"

import (
	"unsafe"

	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// init names the program before GTK starts: the window takes its WM_CLASS and app id from that name
// when it is created, before Wails applies ProgramName.
func init() {
	program, name := C.CString(LinuxAppID), C.CString(platform.AppName)
	defer C.free(unsafe.Pointer(program))
	defer C.free(unsafe.Pointer(name))
	C.g_set_prgname(program)
	C.g_set_application_name(name)
}
