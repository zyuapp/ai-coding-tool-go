//go:build unix

package codex

import (
	"io/fs"
	"syscall"
)

// The device and inode a file is, which two hard links share.
func identityOf(info fs.FileInfo) fileIdentity {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return fileIdentity{Dev: float64(stat.Dev), Ino: float64(stat.Ino)}
	}
	return fileIdentity{}
}
