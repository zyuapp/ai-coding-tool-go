// Package platform is everything that differs between operating systems: the search path children
// get and how they are started, grouped and signalled; the profile folder and its single-instance
// lock; memory; keeping a locked Mac awake; opening folders and files in other applications; desktop
// notices; and the Dock.
//
// Code every Unix shares sits in *_unix.go; what one system does its own way sits in *_darwin.go and
// *_linux.go. A Windows port adds *_windows.go beside them.
package platform
