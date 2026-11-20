//go:build !cgo

package store

import "errors"

// Backup needs SQLite's online backup, which only the cgo build of the driver has.
func (ProfileDatabase) Backup(from, to string) error {
	return errors.New("this build has no SQLite")
}
