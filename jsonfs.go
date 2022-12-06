/*
Package jsonfs provides a JSON marshal/unmarshaller that treats JSON objects
as directories and JSON values (bools, numbers or strings) as files.

This is an alternative to various other structures for dealing with JSON that
either use maps or structs to represent data. This is particularly great for doing
discovery on JSON data or manipulating JSON.

Each file is read-only. You can switch out files in a directory in order to make
updates.

A File represents a JSON basic value of bool, integer, float, null or string.

A Directory represents a JSON object or array. Because a directory can be an
object or array, a directory that represents an array has _.array._ appened to
the name in some filesystems. If creating an array using filesytem tools,
use ArrayDirName("name_of_your_array"), which will append the correct suffix.
You always opened the file with simply the name.  This also means that naming
an object (not array) _.array._ will cause unexpected results.

This is a thought experiment. However, it is quite performant, but does have
the unattractive nature of being more verbose. Also, you may find problems if
your JSON dict keys have invalid characters for the filesystem you are
running on AND you use the diskfs filesystem. For the memfs, this is mostly
not a problem, except for /. You cannot use / in your keys.

# Benchmarks

We are slower and allocate more memory.  I'm going to have to spend some time
optimising the memory use. I had some previous benchmarks that showed this
was faster.  But I had a mistake that became obvious with using a large file,
(unless I was 700,000x faster on unmarshal, and I'm not that good).

	BenchmarkUnmarshalSmall-10            	  132848	      8384 ns/op	   11185 B/op	     167 allocs/op
	BenchmarkStandardUnmarshalSmall-10    	  215502	      5484 ns/op	    2672 B/op	      66 allocs/op
	BenchmarkUnmarshalLarge-10            	       5	 227486309 ns/op	318321257 B/op	 4925295 allocs/op
	BenchmarkStandardUnmarshalLarge-10    	       6	 185993493 ns/op	99390094 B/op	 1749996 allocs/op

# Important notes

  - This doesn't support numbers larger than an Int64. If you need that, you need to use a string.
  - This doesn't support anything other than decimal notation, but the JSON standard does. If someone needs it I'll add it.
  - This does not have []byte conversion to string as the standard lib provides.
  - There are likely bugs in here.

# Examples

Example of unmarshalling a JSON file:

	f, err := os.Open("some/file/path.json")
	if err != nil {
		// Do something
	}
	dir, err := UnmarshalJSON(ctx, f)
	if err != nil {
		// Do something
	}

Example of creating a JSON object via the library:

	dir := MustNewDir(
		"",
		MustNewFile("First Name", "John"),
		MustNewFile("Last Name", "Doak"),
		MustNewDir(
			"Identities",
			MustNewFile("EmployeeID", 10),
			MustNewFile("SSNumber", "999-99-9999"),
		),
	)

Example of marshaling a Directory:

	f, err := os.OpenFile("some/file/path.json",  os.O_CREATE+os.O_RDWR, 0700)
	if err != nil {
		// Do something
	}
	defer f.Close()

	if err := MarshalJSON(f, dir); err != nil {
		// Do something
	}

Example of getting a JSON value by field name:

	f, err := dir.GetFile("Identities/EmployeeID")
	if err != nil {
		// Do something
	}

Same example, but we are okay with zero values if the field doesn't exist:

	f, _ := dir.GetFile("Identities/EmployeeID")

Get the type a File holds:

	t := f.JSONType()

Get a value the easiest way when you aren't sure what it is:

	v := f.Any()
	// Now you have to switch on types nil, string, bool, int64 or float
	// In order to use it.

Get a value from a Directory when you care about all the details (uck):

	f, err := dir.GetFile("Identities/EmployeeID")
	if err != nil {
		fmt.Println("EmployeeID was not set")
	} else if f.Type() == FTNull {
		fmt.Println("EmployeeID was explicitly not set")
	}else {
		id, err := f.Int()
		if err != nil {
			// Do something
		}
		fmt.Println("EmployeeID is ", id)
	}

Get a value from a Directory when zero values will do if set to null or doesn't exist:

	f, _ := dir.GetFile("Identities/EmployeeID")
	fmt.Println(f.StringOrZV())
	// There is also BoolOrZV(), IntOrZV(), ...

Put the value in an fs.FS and walk the JSON:

	// Note: this example can be found in examples/dirwalk
	fsys := NewFSFromDir(dir)

	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Fatal(err)
		}
		p = path.Clean(p)

		switch x := d.(type) {
		case jsonfs.File:
			fmt.Printf("%s:%s:%v\n", p, x.JSONType(), x.Any())
		case jsonfs.Directory:
			fmt.Printf("%s/\n", p)
		}
		return nil
	})
*/
package jsonfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gopherfs "github.com/gopherfs/fs"
)

// FS details the interfaces that a filesytem must have in order to
// be used by jsonfs purposes. We do not honor filesytems interfaces outside
// this package at this time.
type FS interface {
	fs.FS
	fs.ReadDirFS
	fs.ReadFileFS
	fs.StatFS
	fs.SubFS

	gopherfs.MkdirAllFS
	gopherfs.Remove
	gopherfs.Writer
}

// DirectoryFS provides methods for reading and writing a Directory to a
// Filesystem.
type DirectoryFS interface {
	// Directory retrieves a Directory from an FS.
	Directory(path string) (Directory, error)
}

//go:generate stringer -type=FileType

// FileType represents the data stored in a File.
type FileType uint8

const (
	FTNull   FileType = 0
	FTBool   FileType = 1
	FTInt    FileType = 2
	FTFloat  FileType = 3
	FTString FileType = 4
)

// File represents a value in JSON. This can be a string, bool or number.
// All files are readonly.
type File struct {
	name      string
	modTime   time.Time
	t         FileType
	value     []byte
	readValue *[]byte
}

// NewFile creates a new file named "name" with value []byte. Files created
// with NewFile cannot have .Read() called, as this only works when opened
// from FS or a Directory.  This simply is used to help construct a JSON value.
// value can be any type of int, string, bool or float. A nil value stands for
// a JSON null.
func NewFile(name string, value any) (File, error) {
	var b []byte
	var t FileType
	switch x := value.(type) {
	case int8:
		t = FTInt
		b = UnsafeGetBytes(strconv.FormatInt(int64(x), 10))
	case int16:
		t = FTInt
		b = UnsafeGetBytes(strconv.FormatInt(int64(x), 10))
	case int32:
		t = FTInt
		b = UnsafeGetBytes(strconv.FormatInt(int64(x), 10))
	case int64:
		t = FTInt
		b = UnsafeGetBytes(strconv.FormatInt(int64(x), 10))
	case int:
		t = FTInt
		b = UnsafeGetBytes(strconv.FormatInt(int64(x), 10))
	case float32:
		t = FTFloat
		b = UnsafeGetBytes(strconv.FormatFloat(float64(x), 'f', -1, 32))
	case float64:
		t = FTFloat
		b = UnsafeGetBytes(strconv.FormatFloat(float64(x), 'f', -1, 64))
	case bool:
		t = FTBool
		if x {
			b = []byte("true")
		} else {
			b = []byte("false")
		}
	case string:
		t = FTString
		b = make([]byte, 0, len(x)+2)
		b = append(b, doubleQuote)
		b = append(b, UnsafeGetBytes(x)...)
		b = append(b, doubleQuote)
	case nil:
		t = FTNull
		b = []byte("null")
	default:
		return File{}, fmt.Errorf("%T is not a supported type", x)
	}

	return File{
		name:    name,
		t:       t,
		value:   b,
		modTime: time.Now(),
	}, nil
}

// MustNewFile is like NewFile except any error panics.
func MustNewFile(name string, value any) File {
	f, err := NewFile(name, value)
	if err != nil {
		panic(err)
	}
	return f
}

func (f File) isFileOrDir() {}

// Stat implements fs.File.Stat().
func (f File) Stat() (fs.FileInfo, error) {
	return f.stat()
}

// stat is the same as Stat(), but doesn't do an allocation because we
// are implementing an interface in Stat().
func (f File) stat() (FileInfo, error) {
	return FileInfo{
		name:    f.name,
		size:    int64(len(f.value)),
		mode:    0444,
		modTime: f.modTime,
		isDir:   false,
	}, nil
}

// Read implements fs.File.Read(). It is not thread-safe.
func (f File) Read(dst []byte) (int, error) {
	if len(*f.readValue) == 0 {
		return 0, io.EOF
	}

	i := copy(dst, *f.readValue)
	*f.readValue = (*f.readValue)[i:]
	return i, nil
}

// Close closes the file.
func (f File) Close() error {
	return nil
}

// JSONType indicates the JSON type of the file.
func (f File) JSONType() FileType {
	return f.t
}

// Info implements fs.DirEntry.Info().
func (f File) Info() (fs.FileInfo, error) {
	return f.stat() // escape
}

// IsDir implements fs.DirEntry.IsDir().
func (f File) IsDir() bool {
	return false
}

// Name implements fs.DirEntry.Name().
func (f File) Name() string {
	return f.name
}

// Type implements fs.DirEntry.Type().
func (f File) Type() fs.FileMode {
	return 0444
}

// Bool returns a file's value if it is a bool.
func (f File) Bool() (bool, error) {
	if f.t != FTBool {
		return false, fmt.Errorf("was %v, not bool", f.t)
	}
	switch ByteSlice2String(f.value) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("malformed bool value")
}

func (f File) BoolorZV() bool {
	if f.t == FTNull {
		return false
	}

	b, err := f.Bool()
	if err != nil {
		return false
	}
	return b
}

// Float returns a file's value if it is a float.
func (f File) Float() (float64, error) {
	if f.t != FTFloat {
		return 0.0, fmt.Errorf("was %v, not float", f.t)
	}
	s := ByteSlice2String(f.value)
	return strconv.ParseFloat(s, 64)
}

func (f File) FloatOrZV() float64 {
	if f.t == FTNull {
		return 0.0
	}

	fl, err := f.Float()
	if err != nil {
		return 0.0
	}
	return fl
}

// Int returns a file's value if it is a int.
func (f File) Int() (int64, error) {
	if f.t != FTInt {
		return 0, fmt.Errorf("was %v, not int", f.t)
	}
	s := ByteSlice2String(f.value)
	return strconv.ParseInt(s, 10, 64)
}

func (f File) IntOrZV() int64 {
	if f.t == FTNull {
		return 0.0
	}
	i, err := f.Int()
	if err != nil {
		return 0
	}
	return i
}

// String returns a file's value if it is a string.
func (f File) String() (string, error) {
	if f.t != FTString {
		return "", fmt.Errorf("was %v, not string", f.t)
	}
	return ByteSlice2String(f.value), nil
}

func (f File) StringOrZV() string {
	if f.t == FTNull {
		return ""
	}
	s, err := f.String()
	if err != nil {
		return ""
	}
	return s
}

func (f File) Any() any {
	switch f.t {
	case FTBool:
		x, _ := f.Bool()
		return x
	case FTInt:
		x, _ := f.Int()
		return x
	case FTNull:
		return nil
	case FTString:
		x, _ := f.String()
		return x
	case FTFloat:
		x, _ := f.Float()
		return x
	}
	return nil
}

// EncodeJSON outputs the file data as into the writer.
func (f File) EncodeJSON(w io.Writer) error {
	switch f.t {
	case FTString:
		err := WriteOut(w, doubleQuote)
		if err != nil {
			return err
		}
		err = WriteOut(w, f.value)
		if err != nil {
			return err
		}
		err = WriteOut(w, doubleQuote)
		if err != nil {
			return err
		}
		return nil
	}
	return WriteOut(w, f.value)
}

type ObjType uint8

const (
	OTUnknown = 0
	OTFile    = 1
	OTDir     = 2
)

type Object struct {
	Type ObjType
	File File
	Dir  Directory
}

// Directory represents an object or array in JSON nomenclature.
type Directory struct {
	name    string
	modTime time.Time
	objs    map[string]Object

	isArray bool

	mu *sync.RWMutex
}

// NewDir creates a new Directory with name and includes the files
// and directories passed. All passed Directories and Files must have a name. A top
// level directory does not have to have a name.
func NewDir(name string, filesOrDirs ...any) (Directory, error) {
	d := newDir(name, time.Now())
	d.isArray = true
	for _, fd := range filesOrDirs {
		switch x := fd.(type) {
		case File:
			if x.name == "" {
				return Directory{}, fmt.Errorf("a passed File had no name")
			}
			d.objs[x.name] = Object{Type: OTFile, File: x}
		case Directory:
			if x.name == "" {
				return Directory{}, fmt.Errorf("a passed Directory had no name")
			}
			d.objs[x.name] = Object{Type: OTDir, Dir: x}
		default:
			return Directory{}, fmt.Errorf("%T is not a supported type", fd)
		}
	}
	return d, nil
}

// MustNewDir is like NewDirectory, but errors cause a panic.
func MustNewDir(name string, filesOrDirs ...any) Directory {
	d, err := NewDir(name, filesOrDirs...)
	if err != nil {
		panic(err)
	}
	return d
}

// NewArray creates a new Directory that represents a JSON array.
// filesOrDirs that have names will have them overridden.
func NewArray(name string, filesOrDirs ...any) (Directory, error) {
	d := newDir(name, time.Now()) // escape: maps
	for i, fd := range filesOrDirs {
		switch x := fd.(type) {
		case Directory:
			x.name = strconv.Itoa(i)
			d.objs[x.name] = Object{Type: OTDir, Dir: x}
		case File:
			x.name = strconv.Itoa(i)
			d.objs[x.name] = Object{Type: OTFile, File: x}
		default:
			return Directory{}, fmt.Errorf("%T is not a supported type", fd)
		}
	}
	return d, nil
}

// MustNewArray is like NewArray, but errors cause a panic.
func MustNewArray(name string, filesOrDirs ...any) Directory {
	d, err := NewArray(name, filesOrDirs...)
	if err != nil {
		panic(err)
	}
	return d
}

func newDir(name string, modTime time.Time) Directory {
	return Directory{
		name:    name,
		modTime: time.Time{},
		objs:    map[string]Object{},
	}
}

func (d Directory) isFileOrDir() {}

// Name implements fs.DirEntry.Name().
func (d Directory) Name() string {
	return d.name
}

// IsDir implements fs.DirEntry.IsDir().
func (d Directory) IsDir() bool {
	return true
}

// type implements fs.DirEntry.Type().
func (d Directory) Type() fs.FileMode {
	return fs.ModeDir + 0444
}

func (d Directory) info() (FileInfo, error) {
	return FileInfo{
		name:    d.name,
		size:    0,
		mode:    d.Type(),
		modTime: d.modTime,
		isDir:   true,
	}, nil
}

// Info implements fs.DirEntry.Info().
func (d Directory) Info() (fs.FileInfo, error) { // escape
	return d.info()
}

// Stat implements fs.File.Stat().
func (d Directory) Stat() (fs.FileInfo, error) {
	return d.info()
}

// Read implements fs.File.Read(). This will panic as it does on a filesystem.
func (d Directory) Read([]byte) (int, error) {
	panic(fmt.Sprintf("panic: read %s: is a directory", d.name))
}

// ReadDir implememnts fs.ReadDirFile.ReadDir().
func (d Directory) ReadDir(n int) ([]fs.DirEntry, error) {
	if d.mu != nil {
		d.mu.RLock()
		defer d.mu.RUnlock()
	}

	de := make([]fs.DirEntry, 0, len(d.objs))
	for _, obj := range d.objs {
		//obj{t: otFile, file: x}
		if n > 0 {
			if len(de) == n {
				break
			}
		}
		switch obj.Type {
		case OTFile:
			de = append(de, obj.File)
		case OTDir:
			de = append(de, obj.Dir)
		}
	}

	if len(de) == 0 && n > 0 {
		return de, io.EOF
	}
	sort.Slice(
		de,
		func(i, j int) bool {
			return de[i].Name() < de[j].Name()
		},
	)
	return de, nil
}

// Close implememnts fs.ReadDirFile.Close().
func (d Directory) Close() error {
	return nil
}

// GetDir gets a sub directory with the path "name".
func (d Directory) GetDir(name string) (Directory, error) {
	if d.mu != nil {
		d.mu.RLock()
		defer d.mu.RUnlock()
	}

	name = strings.TrimPrefix(path.Clean(name), "/")
	if !fs.ValidPath(name) {
		return Directory{}, fmt.Errorf("invalid name for a path as reported by fs.ValidPath()")
	}

	if name == "." {
		return d, nil
	}

	dir := d

	p := strings.Split(name, "/")
	if len(p) == 0 {
		return Directory{}, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("path is invalid")}
	}

	for i := 0; i < len(p)-1; i++ {
		v, ok := dir.objs[p[i]]
		if !ok {
			return Directory{}, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("could not find directory %q", strings.Join(p, "/"))}
		}
		if v.Type != OTDir {
			return Directory{}, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("could not find directory %q, %q was a file not a directory", strings.Join(p, "/"), strings.Join(p[:i+1], "/"))}
		}
		dir = v.Dir
	}
	fn := p[len(p)-1]
	o, ok := dir.objs[fn]
	if ok && o.Type == OTDir {
		return o.Dir, nil
	}
	return Directory{}, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("could not find directory %q", "/"+name)}
}

// GetFile gets a file located at path "name".
func (d Directory) GetFile(name string) (File, error) {
	if d.mu != nil {
		d.mu.RLock()
		defer d.mu.RUnlock()
	}

	name = strings.TrimPrefix(path.Clean(name), "/")
	if !fs.ValidPath(name) {
		return File{}, fmt.Errorf("invalid name for a path as reported by fs.ValidPath()")
	}

	if name == "." {
		return File{}, fmt.Errorf("'.' is not a valid file name")
	}

	dir := d

	dirName, fileName := path.Split(name)
	if dirName != "" {
		dd, err := d.GetDir(dirName)
		if err != nil {
			return File{}, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("could not find directory %q", dirName)}
		}
		dir = dd
	}
	o, ok := dir.objs[fileName]
	if ok && o.Type == OTFile {
		return o.File, nil
	}
	return File{}, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("could not find directory %q", "/"+name)}
}

func (d Directory) GetObjects() chan Object {
	ch := make(chan Object, 1)
	go func() {
		for _, o := range d.objs {
			ch <- o
		}
	}()
	return ch
}

// Remove removes a file or directory (empty) in this directory.
func (d Directory) Remove(name string) error {
	return d.remove(name, false)
}

// RemoveAll removes a file or directory contained in this directory.
func (d Directory) RemoveAll(name string) error {
	return d.remove(name, true)
}

func (d Directory) remove(name string, children bool) error {
	if d.mu != nil {
		d.mu.RLock()
		defer d.mu.RUnlock()
	}

	o, ok := d.objs[name]
	if !ok {
		return fmt.Errorf("file/directory(%s) was not found", name)
	}
	switch o.Type {
	case OTDir:
		if len(o.Dir.objs) != 0 && !children {
			return fmt.Errorf("directory(%s) was not empty", name)
		}
		delete(o.Dir.objs, name)
	case OTFile:
		delete(d.objs, name)
	default:
		panic("unsuported object type")
	}
	return nil
}

// WriteFile writes file "name" with "data" to this Directory.
func (d Directory) WriteFile(name string, data []byte) error {
	if d.mu != nil {
		d.mu.RLock()
		defer d.mu.RUnlock()
	}

	name = path.Clean(name)
	dir, file := path.Split(name)
	if dir != "" {
		return fmt.Errorf("name(%s) must not be a path, just a filename", name)
	}

	f, err := dataToFile(file, data)
	if err != nil {
		return err
	}

	d.objs[name] = Object{Type: OTFile, File: f}
	return nil
}

// Len is how many items in the Directory.
func (d Directory) Len() int {
	if d.mu != nil {
		d.mu.RLock()
		defer d.mu.RUnlock()
	}

	return len(d.objs)
}

// Set will set sub directories or files in the Directory. If a file or
// Directory already exist, it will be overwritten. This does not work
// if the Directory is an array.
func (d Directory) Set(filesOrDirs ...any) error {
	if d.isArray {
		return errors.New("Set() does not work on arrays")
	}

	// valide that file and directory names + types before we do anything.
	for _, fd := range filesOrDirs {
		switch x := fd.(type) {
		case File:
			if x.name == "" {
				return fmt.Errorf("a passed File had no name")
			}
		case Directory:
			if x.name == "" {
				return fmt.Errorf("a passed Directory had no name")
			}
		default:
			return fmt.Errorf("%T is not a supported type", fd)
		}
	}
	// Make the updates.
	for _, fd := range filesOrDirs {
		switch x := fd.(type) {
		case File:
			d.objs[x.name] = Object{Type: OTFile, File: x}
		case Directory:
			d.objs[x.name] = Object{Type: OTDir, Dir: x}
		}
	}
	return nil
}

// EncodeJSON encodes the Directory as JSON into the io.Writer passed.
func (d Directory) EncodeJSON(w io.Writer) error {
	if d.isArray {
		return fmt.Errorf("must encode from a Directory that is not an array")
	}
	return d.encodeJSONDict(w)
}

func (d Directory) encodeJSONArray(w io.Writer) error {
	if err := WriteOut(w, openBracket); err != nil {
		return err
	}

	for i := 0; i < len(d.objs); i++ {
		is := strconv.Itoa(i)
		o, ok := d.objs[is]
		if !ok {
			return fmt.Errorf("Directory was not a valid array, missing array index %d", i)
		}

		switch o.Type {
		case OTFile:
			if err := o.File.EncodeJSON(w); err != nil {
				return err
			}
		case OTDir:
			if o.Dir.isArray {
				if err := o.Dir.encodeJSONArray(w); err != nil {
					return err
				}
			} else {
				if err := o.Dir.encodeJSONDict(w); err != nil {
					return err
				}
			}
		}

		if i < len(d.objs)-1 {
			if err := WriteOut(w, comma); err != nil {
				return err
			}
		}
	}
	if err := WriteOut(w, closeBracket); err != nil {
		return err
	}
	return nil
}

func (d Directory) encodeJSONDict(w io.Writer) error {
	if err := WriteOut(w, openBrace); err != nil {
		return err
	}

	l := len(d.objs)
	i := 0
	for _, o := range d.objs {
		if err := WriteOut(w, doubleQuote); err != nil {
			return err
		}
		switch o.Type {
		case OTFile:
			if err := WriteOut(w, o.File.name); err != nil {
				return err
			}
		case OTDir:
			if err := WriteOut(w, o.Dir.name); err != nil {
				return err
			}
		}
		if err := WriteOut(w, doubleQuote); err != nil {
			return err
		}
		if err := WriteOut(w, colon); err != nil {
			return err
		}
		switch o.Type {
		case OTFile:
			if err := o.File.EncodeJSON(w); err != nil {
				return err
			}
		case OTDir:
			if o.Dir.isArray {
				if err := o.Dir.encodeJSONArray(w); err != nil {
					return err
				}
			} else {
				if err := o.Dir.encodeJSONDict(w); err != nil {
					return err
				}
			}
		}
		if i < l-1 {
			if err := WriteOut(w, comma); err != nil {
				return err
			}
		}
		i++
	}

	if err := WriteOut(w, closeBrace); err != nil {
		return err
	}
	return nil
}

// FileOrDir stands can hold a File or Directory.
type FileOrDir interface {
	isFileOrDir()
}

// ArraySet sets the value at index to fd. A nil value passed as fd will
// result in a null value being set.
func ArraySet[FD FileOrDir](array Directory, index int, fd FD) error {
	if !array.isArray {
		return fmt.Errorf("cannot call ArraySet() on a Dictionary that is not an array")
	}

	indexS := strconv.Itoa(index)
	if index >= array.Len() {
		return fmt.Errorf("index is out of bounds")
	}

	switch x := any(fd).(type) {
	case Directory:
		if _, ok := array.objs[indexS]; ok {
			array.objs[indexS] = Object{Type: OTDir, Dir: x}
		}
	case File:
		if _, ok := array.objs[indexS]; ok {
			array.objs[indexS] = Object{Type: OTFile, File: x}
		}
	case nil:
		f := File{t: FTNull, value: unsafeGetBytes("null")}
		if _, ok := array.objs[indexS]; ok {
			array.objs[indexS] = Object{Type: OTFile, File: f}
		}
	}
	return nil
}

// Append appends to the Directory array all filesOrDirs passed. A nil
// FileOrDir value will append a JSON null.
func Append[FD FileOrDir](array Directory, filesOrDirs ...FD) error {
	if !array.isArray {
		return fmt.Errorf("cannot append to a Dictionary that is not an array")
	}

	start := len(array.objs)
	for i, fd := range filesOrDirs {
		index := strconv.Itoa(i + start)
		switch x := any(fd).(type) {
		case Directory:
			x.name = index
			array.objs[index] = Object{Type: OTDir, Dir: x}
		case File:
			x.name = index
			array.objs[index] = Object{Type: OTFile, File: x}
		case nil:
			f := File{t: FTNull, value: unsafeGetBytes("null")}
			array.objs[index] = Object{Type: OTFile, File: f}
		}
	}
	return nil
}

// CP will make a copy of the File or Directory and return it. The modtime of
// the new directory and its files is the same as the old one.
func CP[FD FileOrDir](fileOrDir FD) FD {
	// This reuses fileOrDir, which is actually a copy of the Directory or
	// File that came in. We only replace the pointers which are still
	// pointing at the same locations.
	switch x := any(fileOrDir).(type) {
	case Directory:
		if x.mu != nil {
			x.mu.RLock()
			defer x.mu.RUnlock()
		}

		x.objs = make(map[string]Object, len(x.objs))
		if x.mu != nil {
			x.mu = &sync.RWMutex{}
		}
		for k, v := range x.objs {
			switch v.Type {
			case OTDir:
				x.objs[k] = Object{Type: OTDir, Dir: CP(v.Dir)}
			case OTFile:
				x.objs[k] = Object{Type: OTFile, File: CP(v.File)}
			}
		}
	case File:
		b := make([]byte, len(x.value))
		copy(b, x.value)
		x.value = b
		x.readValue = nil
	}
	return fileOrDir
}

func dataToFile(name string, data []byte) (File, error) {
	ft, err := DataType(data)
	if err != nil {
		return File{}, err
	}

	return File{
		name:    name,
		modTime: time.Now(),
		t:       ft,
		value:   data,
	}, nil
}

const arraySuffix = `_.array._`

func ArrayDirName(name string) string {
	return name + arraySuffix
}

func DirNameFromArray(name string) string {
	return strings.TrimSuffix(name, arraySuffix)
}
