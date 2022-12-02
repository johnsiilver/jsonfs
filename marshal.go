package jsonfs

import (
	"bufio"
	"io"
	"sync"
)

var writerPool = sync.Pool{
	New: func() any {
		return bufio.NewWriter(nil)
	},
}

// MarshalJSON takes a Directory and outputs it as JSON to a file writer.
func MarshalJSON(w io.Writer, d Directory) error {
	var b *bufio.Writer
	if _, ok := w.(*bufio.Writer); ok {
		b = w.(*bufio.Writer)
	} else {
		b = writerPool.Get().(*bufio.Writer)
		b.Reset(w)
	}
	defer func() { writerPool.Put(b) }()

	err := d.EncodeJSON(b)
	if err != nil {
		return err
	}
	err = b.Flush()
	return err
}
