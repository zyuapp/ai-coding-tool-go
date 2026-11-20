package workspace

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var errnoNames = map[syscall.Errno][2]string{
	syscall.ENOENT:       {"ENOENT", "no such file or directory"},
	syscall.ENOTDIR:      {"ENOTDIR", "not a directory"},
	syscall.EACCES:       {"EACCES", "permission denied"},
	syscall.EPERM:        {"EPERM", "operation not permitted"},
	syscall.EEXIST:       {"EEXIST", "file already exists"},
	syscall.EISDIR:       {"EISDIR", "illegal operation on a directory"},
	syscall.ENOTEMPTY:    {"ENOTEMPTY", "directory not empty"},
	syscall.ELOOP:        {"ELOOP", "too many symbolic links encountered"},
	syscall.EBUSY:        {"EBUSY", "resource busy or locked"},
	syscall.EXDEV:        {"EXDEV", "cross-device link not permitted"},
	syscall.ENOSPC:       {"ENOSPC", "no space left on device"},
	syscall.EROFS:        {"EROFS", "read-only file system"},
	syscall.EINVAL:       {"EINVAL", "invalid argument"},
	syscall.ENAMETOOLONG: {"ENAMETOOLONG", "name too long"},
	syscall.EMFILE:       {"EMFILE", "too many open files"},
}

// NodeFSError is a filesystem failure worded as Node words it: `CODE: description, syscall 'path'`.
type NodeFSError struct {
	Code    string
	Syscall string
	Path    string
	message string
	cause   error
}

func (e *NodeFSError) Error() string { return e.message }
func (e *NodeFSError) Unwrap() error { return e.cause }

// nodeFSError rewords err from a filesystem call; errors it cannot name are returned as they are.
func nodeFSError(call, target string, err error) error {
	if err == nil {
		return nil
	}
	var already *NodeFSError
	if errors.As(err, &already) {
		return err
	}
	var errno syscall.Errno
	names, ok := [2]string{}, false
	if errors.As(err, &errno) {
		names, ok = errnoNames[errno]
	} else if strings.Contains(err.Error(), "too many links") {
		names, ok = errnoNames[syscall.ELOOP], true
	}
	if !ok {
		return err
	}
	return &NodeFSError{Code: names[0], Syscall: call, Path: target, message: names[0] + ": " + names[1] + ", " + call + " '" + target + "'", cause: err}
}

func isNodeCode(err error, code string) bool {
	var named *NodeFSError
	return errors.As(err, &named) && named.Code == code
}

type copyOptions struct {
	recursive          bool
	preserveTimestamps bool
	// verbatimSymlinks keeps a relative link target as written instead of resolving it against the source.
	verbatimSymlinks bool
}

// copyPath is Node's `fs.cp` with `force: true`: files, directories and symlinks, overwriting what is there.
func copyPath(from, to string, options copyOptions) error {
	info, err := os.Lstat(from)
	if err != nil {
		return nodeFSError("lstat", from, err)
	}
	switch mode := info.Mode(); {
	case mode.IsDir():
		if !options.recursive {
			return errors.New("Recursive option is required to copy a directory: " + from)
		}
		return copyDirectory(from, to, info, options)
	case mode.IsRegular():
		return copyFile(from, to, info, options)
	case mode&os.ModeSymlink != 0:
		return copySymlink(from, to, options)
	case mode&os.ModeSocket != 0:
		return errors.New("Cannot copy a socket file: " + to)
	case mode&os.ModeNamedPipe != 0:
		return errors.New("Cannot copy a FIFO pipe: " + to)
	}
	return errors.New("Cannot copy an unknown file type: " + to)
}

func copyDirectory(from, to string, info os.FileInfo, options copyOptions) error {
	existing, err := os.Lstat(to)
	switch {
	case err == nil && !existing.IsDir():
		return errors.New("Cannot overwrite non-directory " + to + " with directory " + from)
	case err != nil:
		if err := os.Mkdir(to, info.Mode().Perm()); err != nil {
			return nodeFSError("mkdir", to, err)
		}
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return nodeFSError("opendir", from, err)
	}
	for _, entry := range entries {
		if err := copyPath(filepath.Join(from, entry.Name()), filepath.Join(to, entry.Name()), options); err != nil {
			return err
		}
	}
	return os.Chmod(to, info.Mode().Perm())
}

func copyFile(from, to string, info os.FileInfo, options copyOptions) error {
	source, err := os.Open(from)
	if err != nil {
		return nodeFSError("copyfile", from+"' -> '"+to, err)
	}
	defer source.Close()
	target, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm()|0o200)
	if err != nil {
		return nodeFSError("copyfile", from+"' -> '"+to, err)
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	if options.preserveTimestamps {
		atime := info.ModTime()
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			atime = accessTime(stat)
		}
		if err := os.Chtimes(to, atime, info.ModTime()); err != nil {
			return nodeFSError("utime", to, err)
		}
	}
	return os.Chmod(to, info.Mode().Perm())
}

func copySymlink(from, to string, options copyOptions) error {
	target, err := os.Readlink(from)
	if err != nil {
		return nodeFSError("readlink", from, err)
	}
	if !options.verbatimSymlinks && !filepath.IsAbs(target) {
		target = absolute(filepath.Dir(from), target)
	}
	if existing, err := os.Lstat(to); err == nil {
		if existing.Mode()&os.ModeSymlink == 0 {
			return nodeFSError("symlink", target+"' -> '"+to, syscall.EEXIST)
		}
		if err := os.Remove(to); err != nil {
			return nodeFSError("unlink", to, err)
		}
	}
	return nodeFSError("symlink", target+"' -> '"+to, os.Symlink(target, to))
}
