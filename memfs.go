package jsonfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	gopherfs "github.com/gopherfs/fs"
)

var _ FS = MemFS{}

// MemFS represents an inmemory filesystem for storing JSON data. It can be
// used with  tools that work on fs.FS to do introspection on the JSON data or
// make data modifications. The normal way to use MemFS is for
// a single JSON entry.
type MemFS struct {
	root *Directory
}

// NewMemFS creates a new MemFS from a Directory that will act as the root.
func NewMemFS(dir Directory) MemFS {
	return MemFS{root: &dir}
}

// Open implements fs.FS.Open().
func (f MemFS) Open(name string) (fs.File, error) {
	name = strings.TrimPrefix(path.Clean(name), "/")
	if !fs.ValidPath(name) {
		return File{}, fmt.Errorf("invalid name for a path as reported by fs.ValidPath()")
	}

	d := *f.root
	if name == "." {
		return d, nil
	}

	p := strings.Split(name, "/")
	if len(p) == 0 {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("path is invalid")}
	}

	for i := 0; i < len(p)-1; i++ {
		v, ok := d.objs[p[i]]
		if !ok || v.Type != OTDir {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("could not find directory %q", strings.Join(p, "/"))}
		}
		d = v.Dir
	}
	fn := p[len(p)-1]
	v, ok := d.objs[fn]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("could not find file %q", "/"+name)}
	}
	switch v.Type {
	case OTDir:
		return v.Dir, nil
	case OTFile:
		x := v.File.value
		v.File.readValue = &x
		return v.File, nil
	}
	panic("should never get here")
}

// OpenFile implements gopherfs.OpenFiler. Perms are ignored except for the IsDir directive.
func (m MemFS) OpenFile(name string, perms fs.FileMode, options ...gopherfs.OFOption) (fs.File, error) {
	if perms.IsDir() {
		// If the directory already exists, return it.
		file, err := m.Open(name)
		if err == nil {
			if _, ok := file.(Directory); ok {
				return file, nil
			}
			return nil, fmt.Errorf("%q exists as a file, not a directory", name)
		}

		// Okay, let's try to create a new directory.
		dirName, fileName := path.Split(name)
		if dirName == "" { // They want to create a directory at the root
			d, _ := NewDir(fileName) // Cannot error, as we are not passing contents
			m.root.objs[fileName] = Object{Type: OTDir, Dir: d}
			return d, nil
		}

		// Try to open the containing directory.
		f, err := m.Open(dirName)
		if err != nil {
			return nil, err
		}

		// See if it actually was a directory.
		d, ok := f.(Directory)
		if !ok {
			return nil, fmt.Errorf("%q exists as a file, not a directory", name)
		}

		dir, _ := NewDir(fileName)
		d.objs[fileName] = Object{Type: OTDir, Dir: dir}
		return dir, nil
	}

	file, err := m.Open(name)
	if err == nil {
		if v, ok := file.(File); ok {
			x := v.value
			v.readValue = &x
			return v, nil
		}
		return nil, fmt.Errorf("%q exists as a directory, not a file", name)
	}

	return nil, fmt.Errorf("%q does not exist. You cannot create new files with OpenFile(), use WriteFile() instead", name)
}

// ReadDir implements fs.ReadDirFS.Read().
func (f MemFS) ReadDir(name string) ([]fs.DirEntry, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	d, ok := file.(Directory)
	if !ok {
		return nil, fmt.Errorf("%q is not a directory", name)
	}

	return d.ReadDir(0)
}

func (f MemFS) Stat(name string) (fs.FileInfo, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	return file.Stat()
}

// ReadFile implemnts fs.ReadFileFS.ReadFile().
func (f MemFS) ReadFile(name string) ([]byte, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	fi, _ := file.Stat()
	if fi.IsDir() {
		panic(fmt.Sprintf("panic: read %s: is a directory", name))
	}

	return io.ReadAll(file)
}

// Sub implements fs.SubFS.Sub().
func (f MemFS) Sub(dir string) (fs.FS, error) {
	file, err := f.Open(dir)
	if err != nil {
		return nil, err
	}
	fi, _ := file.Stat()
	if !fi.IsDir() {
		return nil, &fs.PathError{Op: "Sub", Path: dir, Err: errors.New("not a directory")}
	}

	return NewMemFS(file.(Directory)), nil
}

/*
// Not sure I want to do this.

// OpenFile implements github.com/gopherfs/fs/OpenFiler.OpenFile(). If opening a directory,
// directory must exist and perm must be fs.ModeDir + 0555. You cannot write to a
// directory.  If opening a file an existing file, the mode must be 0444. If openeing
// a new file, the mode must be 0555.
func (f *FS) OpenFile(name string, perm fs.FileMode, options ...gopherfs.OFOption) (fs.File, error) {
	if len(options) != 0 {
		return nil, fmt.Errorf("filesystem does not support any options to OpenFile")
	}

	if perm !=
}
*/

// MkdirAll creates a directory named path, along with any necessary parents, and returns nil, or else returns an error.
// The permission bits perm (before umask) are used for all directories that MkdirAll creates, which must be
// 2147483940 (fs.ModeDir + 0444).
// If path is already a directory, MkdirAll does nothing and returns nil.
// This implements github.com/gopherfs/fs.MkdirAllFS.MkdirAll.
// TODO(jdoak): Move logic to Directory.MkdirAll and take a lock.
func (f MemFS) MkdirAll(p string, perm fs.FileMode) error {
	if perm != fs.ModeDir+0444 {
		return fmt.Errorf("incorrect FileMode")
	}

	p = strings.TrimPrefix(path.Clean(p), "/")
	if !fs.ValidPath(p) {
		return fmt.Errorf("invalid name for a path as reported by fs.ValidPath()")
	}

	sp := strings.Split(p, "/")
	if len(sp) == 0 {
		return &fs.PathError{Op: "mkdirall", Path: p, Err: fmt.Errorf("path is invalid")}
	}

	d := *f.root
	for i := 0; i < len(sp)-1; i++ {
		o, ok := d.objs[sp[i]]
		if ok {
			if o.Type == OTFile {
				return &fs.PathError{Op: "mkdirall", Path: p, Err: fmt.Errorf("%q is a file", strings.Join(sp[:i+1], "/"))}
			}
			d = o.Dir
			continue
		}
		dir := Directory{name: sp[i], modTime: time.Now()}
		d.objs[sp[i]] = Object{Type: OTDir, Dir: dir}
		d = dir
	}
	return nil
}

// WriteFile writes file with path "name" to the filesystem. If the file
// already exists, this will overwrite the existing file. data must not
// be mutated after it is passed here. perm must be 0444.
// This implements github.com/gopherfs/fs.Writer .
func (f MemFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	if perm != 0444 {
		return fmt.Errorf("filesystem only accepts perm 0444")
	}
	if name == "" {
		return &fs.PathError{Op: "writeFile", Path: name, Err: fmt.Errorf("name cannot be empty")}
	}
	if len(data) == 0 {
		return &fs.PathError{Op: "writeFile", Path: name, Err: fmt.Errorf("filesystem doesn't support empty files")}
	}

	var d Directory
	dir, file := path.Split(name)
	if dir == "" {
		d = *f.root
	} else {
		x, err := f.Open(dir)
		if err != nil {
			return err
		}
		d = x.(Directory)
	}

	return d.WriteFile(file, data)
}

// Remove removes a file or directory (empty) at path "name". This implements
// github.com/gopherfs/fs.Remove.Remove .
func (f MemFS) Remove(name string) error {
	return f.remove(name, false)
}

// RemoveAll removes path and any children it contains. It removes
// everything it can but returns the first error it encounters.
// If the path does not exist, RemoveAll returns nil (no error).
// If there is an error, it will be of type *fs.PathError.
func (f MemFS) RemoveAll(path string) error {
	return f.remove(path, true)
}

func (f MemFS) remove(name string, children bool) error {
	name = strings.TrimPrefix(path.Clean(name), "/")
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: "remove", Path: name, Err: fmt.Errorf("invalid name for a path as reported by fs.ValidPath()")}
	}

	p := strings.Split(name, "/")
	if len(p) == 0 {
		return &fs.PathError{Op: "remove", Path: name, Err: fmt.Errorf("path is invalid")}
	}

	d := *f.root
	for i := 0; i < len(p)-1; i++ {
		o, ok := d.objs[p[i]]
		if !ok {
			return &fs.PathError{Op: "remove", Path: name, Err: fmt.Errorf("could not find directory %q", strings.Join(p, "/"))}
		}
		if o.Type != OTDir {
			return &fs.PathError{Op: "remove", Path: name, Err: fmt.Errorf("could not find directory %q, %q was a file not a directory", strings.Join(p, "/"), strings.Join(p[:i+1], "/"))}
		}
		d = o.Dir
	}

	fn := p[len(p)-1]
	if children {
		if err := d.RemoveAll(fn); err != nil {
			return &fs.PathError{Op: "remove", Path: name, Err: err}
		}
		return nil
	}
	if err := d.Remove(fn); err != nil {
		return &fs.PathError{Op: "remove", Path: name, Err: err}
	}
	return nil
}
