package contracts

import (
	"fmt"
	"regexp"
)

// ReportedMessage is an error's message, or the value as JavaScript's String would write it.
func ReportedMessage(err any) string {
	switch v := err.(type) {
	case error:
		return v.Error()
	case fmt.Stringer:
		return v.String()
	case nil:
		return "undefined"
	}
	return ipcJSString(err)
}

var remoteMethodWrapper = regexp.MustCompile(`^Error invoking remote method '[^']*': (?:\w*Error: )?`)

// ErrorMessage is ReportedMessage without the wrapper Electron puts around a rejection crossing the bridge.
func ErrorMessage(err any) string {
	return remoteMethodWrapper.ReplaceAllLiteralString(ReportedMessage(err), "")
}
