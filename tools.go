package jsonfs

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"strconv"
	"sync"
	"time"
	"unicode"
	"unsafe"

	"github.com/johnsiilver/pools/memory/ptrpool"
)

// readerPool holds *bufio.Readers for reuse.
var readerPool = sync.Pool{
	New: func() any {
		return bufio.NewReader(nil)
	},
}

var dictPool *ptrpool.Pool[dictSM]

func init() {
	var err error
	dictPool, err = ptrpool.New(
		ptrpool.FreeList{
			Base: 10,
			Grow: ptrpool.Grow{
				Maximum:         1000,
				MeasurementSpan: 1 * time.Second,
				Grower:          (&ptrpool.BasicGrower{}).Grower,
			},
		},
		func() *dictSM {
			return newDictSM(nil, "")
		},
	)
	if err != nil {
		panic(err)
	}
}

var arrayPool *ptrpool.Pool[arraySM]

func init() {
	var err error
	arrayPool, err = ptrpool.New(
		ptrpool.FreeList{
			Base: 10,
			Grow: ptrpool.Grow{
				Maximum:         1000,
				MeasurementSpan: 1 * time.Second,
				Grower:          (&ptrpool.BasicGrower{}).Grower,
			},
		},
		func() *arraySM {
			return newArray(nil, "", time.Time{})
		},
	)
	if err != nil {
		panic(err)
	}
}

/*
var diffPool *diffsize.Pool[*[]byte]

func init() {
	var err error
	diffPool, err = diffsize.New[*[]byte](
		diffsize.Sizes{
			{Size: 4, ConstBuff: 1000, SyncPool: true},
			{Size: 5, ConstBuff: 1000, SyncPool: true},
			{Size: 20, ConstBuff: 100, SyncPool: true},
			{Size: 50, ConstBuff: 100, SyncPool: true},
			{Size: 100, ConstBuff: 100, SyncPool: true, MaxSize: 200},
		},
	)
	if err != nil {
		panic(err)
	}
}
*/

// ValueCheck tells what the next JSON value in a *bufio.Reader is.
func ValueCheck(b *bufio.Reader) (next, error) {
	SkipSpace(b)

	x, err := b.Peek(1)
	if err != nil {
		return 0, err
	}
	r := rune(x[0])

	switch {
	case r == openBrace:
		return msgNext, nil
	case r == openBracket:
		return arrayNext, nil
	case r == doubleQuote:
		return stringNext, nil
	case unicode.IsNumber(r):
		return numNext, nil
	// bool case
	case r == 't':
		return trueNext, nil
	case r == 'f':
		return falseNext, nil
	// null case
	case r == 'n':
		return nullNext, nil
	}

	return 0, fmt.Errorf("unexpected value type after key, got %q after quote", r)
}

// DataType is a more expensive version of ValueCheck for just file data.
func DataType(data []byte) (FileType, error) {
	b := bytes.NewBuffer(data)

	r, _, err := b.ReadRune()
	if err != nil {
		return 0, fmt.Errorf("could not read rune in data: %s", err)
	}

	switch r {
	case 't':
		if len(data) == 4 {
			if ByteSlice2String(data) == "true" {
				return FTBool, nil
			}
		}
	case 'f':
		if len(data) == 5 {
			if ByteSlice2String(data) == "false" {
				return FTBool, nil
			}
		}
	case 'n':
		if len(data) == 4 {
			if ByteSlice2String(data) == "null" {
				return FTNull, nil
			}
		}
	}

	if unicode.IsNumber(r) {
		seenDot := false
		for {
			r, _, err := b.ReadRune()
			if err != nil {
				if err == io.EOF {
					t := FTInt
					if seenDot {
						t = FTFloat
					}
					return t, nil
				}
				return 0, fmt.Errorf("could not read rune in WriteFile data: %s", err)
			}
			switch {
			case unicode.IsNumber(r):
			case r == '.':
				if !seenDot {
					seenDot = true
					continue
				}
			}
			break
		}
	}
	return FTString, nil
}

// SskipSpace skips all spaces in the reader.
func SkipSpace(b *bufio.Reader) {
	for {
		r, _, err := b.ReadRune()
		if err != nil {
			b.UnreadRune()
			return
		}
		if unicode.IsSpace(r) {
			continue
		}
		b.UnreadRune()
		return
	}
}

var writeOutPool = make(chan []byte, 10)

func init() {
	for i := 0; i < cap(writeOutPool); i++ {
		b := make([]byte, 1)
		writeOutPool <- b
	}
}

type Writeable interface {
	rune | string | []byte
}

// WriteOut writes to "w" all "values".
func WriteOut[S Writeable](w io.Writer, values ...S) error {
	for _, v := range values {
		var b []byte
		switch x := any(v).(type) {
		case rune:
			// This prevents unneccesary allocations in this case.
			b := <-writeOutPool
			b[0] = byte(x)
			if _, err := w.Write(b); err != nil {
				return err
			}
			writeOutPool <- b
			continue
		case string:
			b = UnsafeGetBytes(x)
		case []byte:
			b = x
		}

		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// IsArray reads a directory in fsys at path to determine if it is an array.
// Note: I wish there was a better way, but it would have to be portable
// across filesystems and has to deal with people doing adhoc writes to the
// filesystem.
func IsArray(fsys fs.ReadDirFS, path string) (bool, error) {
	entries, err := fsys.ReadDir(path)
	if err != nil {
		return false, err
	}
	expect := make([]bool, len(entries))
	for _, e := range entries {
		x, err := strconv.Atoi(e.Name())
		if err != nil {
			return false, nil
		}
		if x > len(entries) || x < 0 {
			return false, nil
		}
		expect[x] = true
	}
	for _, b := range expect {
		if !b {
			return false, nil
		}
	}
	return true, nil
}

// UnsafeGetBytes extracts the []byte from a string. Use cautiously.
func UnsafeGetBytes(s string) []byte {
	return (*[0x7fff0000]byte)(unsafe.Pointer(
		(*reflect.StringHeader)(unsafe.Pointer(&s)).Data),
	)[:len(s):len(s)]
}

func ByteSlice2String(bs []byte) string {
	return *(*string)(unsafe.Pointer(&bs))
}
