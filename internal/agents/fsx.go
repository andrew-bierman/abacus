package agents

import (
	"io/fs"
	"os"
	"path/filepath"
)

// openRegular opens name only if it is a regular file. A FIFO, a device or a socket (an attacker can
// name one x.jsonl, or put one at .git/HEAD) is refused before anything is read, because opening a
// FIFO for reading blocks until a writer appears and no deadline reaches it. The check is made before
// the open and again on the opened file; the real file system also opens non-blocking, so a file
// swapped for a FIFO between the two cannot hang the scan either.
func openRegular(fsys fs.FS, name string) (fs.File, fs.FileInfo, error) {
	if fi, err := fs.Stat(fsys, name); err != nil {
		return nil, nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, nil, fs.ErrInvalid
	}
	f, err := fsys.Open(name)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, fs.ErrInvalid
	}
	return f, fi, nil
}

// rootFS is the real file system under root with non-blocking opens (see openRegular).
type rootFS struct{ root string }

func (r rootFS) path(name string) (string, error) {
	if !fs.ValidPath(name) {
		return "", fs.ErrInvalid
	}
	return filepath.Join(r.root, filepath.FromSlash(name)), nil
}

func (r rootFS) Open(name string) (fs.File, error) {
	p, err := r.path(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return os.OpenFile(p, os.O_RDONLY|nonblockFlag, 0)
}

func (r rootFS) Stat(name string) (fs.FileInfo, error) {
	p, err := r.path(name)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	return os.Stat(p)
}

func (r rootFS) ReadDir(name string) ([]fs.DirEntry, error) {
	p, err := r.path(name)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	return os.ReadDir(p)
}
