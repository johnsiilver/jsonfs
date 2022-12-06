package jsonfs

import (
	"io/fs"
	"time"
)

// FileInfo implements fs.FileInfo.
type FileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func (f FileInfo) Name() string {
	return f.name
}

func (f FileInfo) Size() int64 {
	return f.size
}

func (f FileInfo) Mode() fs.FileMode {
	return f.mode
}

func (f FileInfo) ModTime() time.Time {
	return f.modTime
}

func (f FileInfo) IsDir() bool {
	return f.isDir
}

func (f FileInfo) Sys() any {
	return nil
}
