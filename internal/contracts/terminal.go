package contracts

const (
	MaxTerminalInput     = 64 * 1024
	MaxTerminalDimension = 1_000
)

func IsTerminalDimension(value any) bool {
	return ipcInteger(value) && ipcNumber(value) >= 1 && ipcNumber(value) <= MaxTerminalDimension
}

// IsTerminalOutputRead accepts nil (JSON null) as well as a TerminalOutputRead.
func IsTerminalOutputRead(value any) bool {
	if value == nil {
		return true
	}
	read, ok := ipcRecord(value)
	if !ok {
		return false
	}
	_, data := read["data"].(string)
	return (read["kind"] == "snapshot" || read["kind"] == "output") && data &&
		IsTerminalDimension(read["cols"]) && IsTerminalDimension(read["rows"]) &&
		ipcSafeInteger(read["sequence"]) && ipcNumber(read["sequence"]) >= 0
}
