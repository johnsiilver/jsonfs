package jsonfs

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	gopherfs "github.com/gopherfs/fs"
	osfs "github.com/gopherfs/fs/io/os"
)

var _ FS = DiskFS{}

// DiskFS provides an FS for OS access.  It will treat everything in the
// filesystem from the root down as JSON. So directories represent JSON
// objects or arrays while files represent JSON values.
type DiskFS struct {
	fs *osfs.FS
}

// NewDiskFS creates a new DiskFS rooted at path string. The path should
// represent a new directory and must not exist. MkdirAll() will be called
// with perms 0700. If you wish to open an existing path, use OpenDiskFS().
func NewDiskFS(path string) (DiskFS, error) {
	fs, err := osfs.New()
	if err != nil {
		return DiskFS{}, err
	}

	_, err = fs.Stat(path)
	if err == nil {
		return DiskFS{}, fmt.Errorf("path %q already exists", path)
	}

	if err := fs.MkdirAll(path, 0700); err != nil {
		return DiskFS{}, err
	}

	fsx, err := fs.Sub(path)
	if err != nil {
		return DiskFS{}, fmt.Errorf("could not get fs.Sub(%q): %s", path, err)
	}
	return DiskFS{fs: fsx.(*osfs.FS)}, nil
}

// OpenDiskFS opens a DiskFS that exists on the filesystem at path.
func OpenDiskFS(path string) (DiskFS, error) {
	fs, err := osfs.New()
	if err != nil {
		return DiskFS{}, err
	}

	fi, err := fs.Stat(path)
	if err != nil {
		return DiskFS{}, fmt.Errorf("path %q doest not exist", path)
	}

	if !fi.IsDir() {
		return DiskFS{}, fmt.Errorf("path %q is not a directory", path)
	}

	fsx, err := fs.Sub(path)
	if err != nil {
		return DiskFS{}, fmt.Errorf("could not get fs.Sub(%q): %s", path, err)
	}
	return DiskFS{fs: fsx.(*osfs.FS)}, nil
}

// Directory retrieves a Directory from path. Changes to the Directory will
// not make changes to disk. If you wish to sync this copy of the Directory
// to disk, use WriteDir().
func (f DiskFS) Directory(name string) (Directory, error) {
	// Just in case they added the array suffix, which they shouldn't.
	name = DirNameFromArray(name)

	originalName := name // This should be the name without the array suffix.
	isArray := false

	// Let's see if the file exists with the name without the array suffix.
	fi, err0 := f.Stat(name)
	if err0 != nil {
		// Since we didn't find it, let's try with the array suffix.
		name = ArrayDirName(name) // Now name holds the name + array suffix.
		var err1 error
		fi, err1 = f.Stat(name)
		if err1 != nil {
			return Directory{}, err0
		}
		// Found it, so it is an array.
		isArray = true
	}

	if !fi.IsDir() {
		return Directory{}, fmt.Errorf("%q is not a directory", name)
	}

	_, file := path.Split(originalName)
	root, _ := NewDir(file) // We aren't passing files here, so error can be skipped
	root.modTime = fi.ModTime()
	if isArray {
		root.isArray = isArray
		root.name = DirNameFromArray(file)
	} else {
		root.name = file
	}

	fs.WalkDir(
		f.fs,
		name,
		func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			dirName, fileName := path.Split(p)
			if d.IsDir() {
				dir := MustNewDir(fileName)
				subDir, err := descTree(root, dirName)
				if err != nil {
					return err
				}
				subDir.dirs[fileName] = dir
				return nil
			}
			b, err := f.fs.ReadFile(p)
			if err != nil {
				return fmt.Errorf("could not read file at %q: %s", p, err)
			}
			file := MustNewFile(fileName, b)
			subDir, err := descTree(root, dirName)
			if err != nil {
				return err
			}
			subDir.files[fileName] = file
			return nil
		},
	)
	return root, nil
}

/*
// WriteDir writes a directory to path. That path must exist, but be empty.
func (f DiskFS) WriteDir(path string, Directory) {

}
*/

func descTree(d Directory, p string) (Directory, error) {
	sp := strings.Split(p, "/")
	var ok bool
	for _, dir := range sp {
		d, ok = d.dirs[dir]
		if !ok {
			return Directory{}, fmt.Errorf("problem descending to directory %q in %q", dir, p)
		}
	}
	return d, nil
}

// Open implements fs.FS.Open().
func (f DiskFS) Open(name string) (fs.File, error) {
	return f.fs.Open(name)
}

// OpenFile represents github.com/gopherfs/fs.OpenFiler. A file can only be opened
// in 0400, 0440, 0404, 0444. A Directory in 0700, 0770, 0707, 0777.
func (f DiskFS) OpenFile(name string, perms fs.FileMode, options ...gopherfs.OFOption) (fs.File, error) {
	fi, err := f.fs.Stat(name)
	if err != nil {
		return nil, err
	}
	switch perms {
	case 0400, 0440, 0404, 0444:
		if fi.IsDir() {
			return nil, fmt.Errorf("a directory can only be opened in 0700, 0770, 0707")
		}
	case 0700, 0770, 0707, 0777:
		if !fi.IsDir() {
			return nil, fmt.Errorf("a file can only be opened in 0400, 0404, 0444")
		}
	default:
		return nil, fmt.Errorf("permissions for a file must be 0400, 0404 or 0444. Directories must be 0700, 0770 or 0707.")
	}
	return f.fs.OpenFile(name, perms, options...)
}

// ReadDir implements fs.ReadDirFS.Read().
func (f DiskFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return f.fs.ReadDir(name)
}

func (f DiskFS) Stat(name string) (fs.FileInfo, error) {
	return f.fs.Stat(name)
}

// ReadFile implemnts fs.ReadFileFS.ReadFile().
func (f DiskFS) ReadFile(name string) ([]byte, error) {
	return f.fs.ReadFile(name)
}

// Sub implements fs.SubFS.Sub().
func (f DiskFS) Sub(dir string) (fs.FS, error) {
	return f.fs.Sub(dir)
}

// Mkdir creates a directory named "p" with permissions "perm".
// perm must be 0700, 0770, 0707 or 0777.
func (f DiskFS) Mkdir(p string, perm fs.FileMode) error {
	switch perm {
	case 0700, 0770, 0707, 0777:
	default:
		return fmt.Errorf("MukdirAll must be called with perm 0700, 0770, 0707 or 0777")
	}
	return f.fs.Mkdir(p, perm)
}

// MkdirAll creates a directory named path, along with any necessary parents, and returns nil, or else returns an error.
// The permission bits perm (before umask) are used for all directories that MkdirAll creates.
// If path is already a directory, MkdirAll does nothing and returns nil.
// perm must be 0700, 0770, 0707, 0777.
// This implements github.com/gopherfs/fs.MkdirAllFS.MkdirAll.
func (f DiskFS) MkdirAll(p string, perm fs.FileMode) error {
	switch perm {
	case 0700, 0770, 0707, 0777:
	default:
		return fmt.Errorf("MukdirAll must be called with perm 0700, 0770, 0707 or 0777")
	}
	return f.fs.MkdirAll(p, perm)
}

// WriteFile writes file with path "name" to the filesystem. If the file
// already exists, this will overwrite the existing file. data must not
// be mutated after it is passed here. perm must be 0400, 0440, 0404 or 0444.
// This implements github.com/gopherfs/fs.Writer .
func (f DiskFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	if _, err := DataType(data); err != nil {
		return err
	}
	return f.fs.WriteFile(name, data, perm)
}

// Remove removes a file or directory (empty) at path "name". This implements
// github.com/gopherfs/fs.Remove.Remove .
func (f DiskFS) Remove(name string) error {
	return f.fs.Remove(name)
}

// RemoveAll removes path and any children it contains. It removes
// everything it can but returns the first error it encounters.
// If the path does not exist, RemoveAll returns nil (no error).
// If there is an error, it will be of type *fs.PathError.
func (f DiskFS) RemoveAll(path string) error {
	return f.fs.RemoveAll(path)
}
