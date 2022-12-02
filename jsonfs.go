/*
Package jsonfs provides a JSON marshal/unmarshaller that treats JSON objects
as directories and JSON values (bools, numbers or strings) as files.

This is an alternative to various other structures for dealing with JSON that
either use maps or structs to represent data. This is particularly great for doing
discovery on JSON data or manipulating JSON.

Each file is read-only. You can switch out files in a directory in order to make
updates.

A File represents a JSON basic value of bool, integer, float, null or string.

A Directory represents a JSON object or array.

This is a thought experiment, but should be both performant and safe to use.
However, it does have the unattractive nature of being verbose for operations,
especially when you aren't sure if the value will be set or set to null.

# Benchmarks

Here are the benchmarks comparing the stdlib/json using maps against this
package:

	BenchmarkMarshalJSON-10          	  263043	      4403 ns/op	       0 B/op	       0 allocs/op
	BenchmarkMarshalJSONStdlib-10    	  365203	      3216 ns/op	    2304 B/op	      51 allocs/op
	BenchmarkUnmarshal-10            	 6532909	       186.5 ns/op	     256 B/op	       4 allocs/op
	BenchmarkStandardUnmarshal-10    	  302988	      3922 ns/op	    2672 B/op	      66 allocs/op

In our marshal, we are about 1000 nanoseconds longer. But we are also allocation free,
which I think will be a longer term benefit.

In our unmarshal, we are about 21x the speed with a 17x reduction in allocations.

// # Important notes

- This doesn't support numbers larger than an Int64. If you need that, you need to use a string.
- This doesn't support anything other than decimal notation, but the JSON standard does. If someone needs it I'll add it.
- This does not have []byte conversion to string as the standard lib provides.
- There are likely bugs.

// # Examples

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

	obj := MustNewDir(
		"",
		MustNewFile("First Name", "John"),
		MustNewFile("Last Name", "Doak"),
		MustNewDir(
			"Identities",
			MustNewFile("EmployeeID", 10),
			MustNewFile("SSNumber", "999-99-9999"),
		)
	)

Example of marshaling a Directory:

	f, err := os.OpenFile("some/file/path.json",  os.O_CREATE+os.O_RDWR, 0700)
	if err != nil {
		// Do something
	}
	defer f.Close()

	if err := MarshalJSON(f, obj); err != nil {
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

	fsys := NewFSFromDir(dir)

	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Fatal(err)
		}
		p = path.Clean(p)

		switch x := d.(type) {
		case File:
			fmt.Printf("%s:%s:%v", p, x.Type(), x.Any())
		case Directory:
			fmt.Printf("%s:%s:%v", p)
		}
		return nil
	})
*/
package jsonfs

import (
	"bytes"
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
	"unicode"
	"unsafe"
)

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

// fileInfo implements fs.FileInfo.
type fileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func (f fileInfo) Name() string {
	return f.name
}

func (f fileInfo) Size() int64 {
	return f.size
}

func (f fileInfo) Mode() fs.FileMode {
	return f.mode
}

func (f fileInfo) ModTime() time.Time {
	return f.modTime
}

func (f fileInfo) IsDir() bool {
	return f.isDir
}

func (f fileInfo) Sys() any {
	return nil
}

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
		b = unsafeGetBytes(strconv.FormatInt(int64(x), 10))
	case int16:
		t = FTInt
		b = unsafeGetBytes(strconv.FormatInt(int64(x), 10))
	case int32:
		t = FTInt
		b = unsafeGetBytes(strconv.FormatInt(int64(x), 10))
	case int64:
		t = FTInt
		b = unsafeGetBytes(strconv.FormatInt(int64(x), 10))
	case float32:
		t = FTFloat
		b = unsafeGetBytes(strconv.FormatFloat(float64(x), 'f', -1, 32))
	case float64:
		t = FTFloat
		b = unsafeGetBytes(strconv.FormatFloat(float64(x), 'f', -1, 64))
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
		b = append(b, unsafeGetBytes(x)...)
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
	return fileInfo{
		name:    f.name,
		size:    int64(len(f.value)),
		mode:    0444,
		modTime: f.modTime,
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
	return f.Stat()
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
	switch byteSlice2String(f.value) {
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
	s := byteSlice2String(f.value)
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
	s := byteSlice2String(f.value)
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
	return byteSlice2String(f.value), nil
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
		err := writeOut(w, doubleQuote)
		if err != nil {
			return err
		}
		err = writeOut(w, f.value)
		if err != nil {
			return err
		}
		err = writeOut(w, doubleQuote)
		if err != nil {
			return err
		}
		return nil
	}
	return writeOut(w, f.value)
}

// Directory represents an object or array in JSON nomenclature.
type Directory struct {
	name    string
	modTime time.Time
	dirs    map[string]Directory
	files   map[string]File

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
			d.files[x.name] = x
		case Directory:
			if x.name == "" {
				return Directory{}, fmt.Errorf("a passed Directory had no name")
			}
			d.dirs[x.name] = x
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
	d := newDir(name, time.Now())
	for i, fd := range filesOrDirs {
		switch x := fd.(type) {
		case Directory:
			x.name = strconv.Itoa(i)
			d.dirs[strconv.Itoa(i)] = x
		case File:
			x.name = strconv.Itoa(i)
			d.files[strconv.Itoa(i)] = x
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
		dirs:    map[string]Directory{},
		files:   map[string]File{},
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

// Info implements fs.DirEntry.Info().
func (d Directory) Info() (fs.FileInfo, error) {
	return fileInfo{
		name:    d.name,
		size:    0,
		mode:    d.Type(),
		modTime: d.modTime,
		isDir:   true,
	}, nil
}

// Stat implements fs.File.Stat().
func (d Directory) Stat() (fs.FileInfo, error) {
	return d.Info()
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

	de := make([]fs.DirEntry, 0, len(d.dirs)+len(d.files))
	for _, dir := range d.dirs {
		if n > 0 {
			if len(de) == n {
				break
			}
		}
		de = append(de, dir)
	}
	for _, file := range d.files {
		if n > 0 {
			if len(de) == n {
				break
			}
		}
		de = append(de, file)
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

	fsys := NewFSFromDir(d)
	file, err := fsys.Open(name)
	if err != nil {
		return Directory{}, err
	}
	fi, _ := file.Stat()
	if !fi.IsDir() {
		return Directory{}, fmt.Errorf("%q is a file not a directory", name)
	}
	return file.(Directory), nil
}

// GetFile gets a file located at path "name".
func (d Directory) GetFile(name string) (File, error) {
	if d.mu != nil {
		d.mu.RLock()
		defer d.mu.RUnlock()
	}

	fsys := NewFSFromDir(d)
	file, err := fsys.Open(name)
	if err != nil {
		return File{}, err
	}
	fi, _ := file.Stat()
	if fi.IsDir() {
		return File{}, fmt.Errorf("%q is a directory", name)
	}
	return file.(File), nil
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

	if _, ok := d.files[name]; ok {
		delete(d.files, name)
		return nil
	}
	if _, ok := d.dirs[name]; ok {
		if len(d.files)+len(d.dirs) != 0 {
			if !children {
				return fmt.Errorf("directory not empty")
			}
		}
		delete(d.dirs, name)
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
	o, err := dataToFile(file, data)
	if err != nil {
		return err
	}

	d.files[name] = o
	return nil
}

// Len is how many items in the Directory.
func (d Directory) Len() int {
	if d.mu != nil {
		d.mu.RLock()
		defer d.mu.RUnlock()
	}

	return len(d.dirs) + len(d.files)
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
			d.files[x.name] = x
		case Directory:
			d.dirs[x.name] = x
		}
	}
	return nil
}

// EncodeJSON encodes the Directory as JSON into the io.Writer passed.
func (d Directory) EncodeJSON(w io.Writer) error {
	if d.isArray {
		return d.encodeJSONArray(w)
	}
	return d.encodeJSONDict(w)
}

func (d Directory) encodeJSONArray(w io.Writer) error {
	if err := writeOut(w, openBracket); err != nil {
		return err
	}

	for i := 0; i < len(d.files)+len(d.dirs); i++ {
		is := strconv.Itoa(i)
		fv, ok := d.files[is]
		if ok {
			if err := fv.EncodeJSON(w); err != nil {
				return err
			}
		} else {
			fd, ok := d.dirs[is]
			if !ok {
				return fmt.Errorf("Directory was not a valid array, missing array index %d", i)
			}
			if err := fd.EncodeJSON(w); err != nil {
				return err
			}
		}
		if i < len(d.dirs)+len(d.files)-1 {
			if err := writeOut(w, comma); err != nil {
				return err
			}
		}
	}
	if err := writeOut(w, closeBracket); err != nil {
		return err
	}
	return nil
}

func (d Directory) encodeJSONDict(w io.Writer) error {
	if err := writeOut(w, openBrace); err != nil {
		return err
	}

	filesExist := len(d.files) > 0
	dirsExist := len(d.dirs) > 0

	i := 0
	for _, file := range d.files {
		if err := writeOut(w, doubleQuote); err != nil {
			return err
		}
		if err := writeOut(w, file.name); err != nil {
			return err
		}
		if err := writeOut(w, doubleQuote); err != nil {
			return err
		}
		if err := writeOut(w, colon); err != nil {
			return err
		}
		if err := file.EncodeJSON(w); err != nil {
			return err
		}
		if i < len(d.files)-1 {
			if err := writeOut(w, comma); err != nil {
				return err
			}
		}
		i++
	}

	if filesExist && dirsExist {
		if err := writeOut(w, comma); err != nil {
			return err
		}
	}

	i = 0
	for _, dir := range d.dirs {
		if err := writeOut(w, doubleQuote); err != nil {
			return err
		}
		if err := writeOut(w, dir.name); err != nil {
			return err
		}
		if err := writeOut(w, doubleQuote); err != nil {
			return err
		}
		if err := writeOut(w, colon); err != nil {
			return err
		}
		if err := dir.EncodeJSON(w); err != nil {
			return err
		}
		if i < len(d.dirs)-1 {
			if err := writeOut(w, comma); err != nil {
				return err
			}
		}
		i++
	}
	if err := writeOut(w, closeBrace); err != nil {
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
		if _, ok := array.dirs[indexS]; ok {
			array.dirs[indexS] = x
		}
		if _, ok := array.files[indexS]; ok {
			delete(array.files, indexS)
			array.dirs[indexS] = x
		}
	case File:
		if _, ok := array.files[indexS]; ok {
			array.files[indexS] = x
		}
		if _, ok := array.dirs[indexS]; ok {
			delete(array.dirs, indexS)
			array.files[indexS] = x
		}
	case nil:
		f := File{t: FTNull, value: unsafeGetBytes("null")}
		if _, ok := array.files[indexS]; ok {
			array.files[indexS] = f
		}
		if _, ok := array.dirs[indexS]; ok {
			delete(array.dirs, indexS)
			array.files[indexS] = f
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

	start := len(array.dirs) + len(array.files)
	for i, fd := range filesOrDirs {
		index := strconv.Itoa(i + start)
		switch x := any(fd).(type) {
		case Directory:
			x.name = index
			array.dirs[index] = x
		case File:
			x.name = index
			array.files[index] = x
		case nil:
			array.files[index] = File{t: FTNull, value: unsafeGetBytes("null")}
		}
	}
	return nil
}

type writeable interface {
	rune | string | []byte
}

var writeOutPool = make(chan []byte, 10)

func init() {
	for i := 0; i < cap(writeOutPool); i++ {
		b := make([]byte, 1)
		writeOutPool <- b
	}
}

func writeOut[S writeable](w io.Writer, values ...S) error {
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
			b = unsafeGetBytes(x)
		case []byte:
			b = x
		}

		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}

func byteSlice2String(bs []byte) string {
	return *(*string)(unsafe.Pointer(&bs))
}

// FS implements:
// - fs.FS
// - fs.ReadDirFS
// - fs.ReadFileFS
// - fs.StatFS
// - fs.SubFS
type FS struct {
	root Directory
}

// NewFSFromDir creates a new FS from a Directory that will act as the root.
func NewFSFromDir(dir Directory) *FS {
	return &FS{root: dir}
}

// Open implements fs.FS.Open().
func (f *FS) Open(name string) (fs.File, error) {
	name = strings.TrimPrefix(path.Clean(name), "/")
	if !fs.ValidPath(name) {
		return File{}, fmt.Errorf("invalid name for a path as reported by fs.ValidPath()")
	}

	p := strings.Split(name, "/")
	if len(p) == 0 {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("path is invalid")}
	}

	d := f.root
	for i := 0; i < len(p)-1; i++ {
		v, ok := d.dirs[p[i]]
		if !ok {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("could not find directory %q", strings.Join(p, "/"))}
		}
		d = v
	}
	fn := p[len(p)-1]
	v, ok := d.files[fn]
	if ok {
		return v, nil
	}
	d, ok = d.dirs[fn]
	if ok {
		return d, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("could not find file %q", "/"+name)}
}

// ReadDir implements fs.ReadDirFS.Read().
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
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

func (f *FS) Stat(name string) (fs.FileInfo, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	return file.Stat()
}

// ReadFile implemnts fs.ReadFileFS.ReadFile().
func (f *FS) ReadFile(name string) ([]byte, error) {
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
func (f *FS) Sub(dir string) (fs.FS, error) {
	file, err := f.Open(dir)
	if err != nil {
		return nil, err
	}
	fi, _ := file.Stat()
	if !fi.IsDir() {
		return nil, &fs.PathError{Op: "Sub", Path: dir, Err: errors.New("not a directory")}
	}

	return NewFSFromDir(file.(Directory)), nil
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
func (f *FS) MkdirAll(p string, perm fs.FileMode) error {
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

	d := f.root
	for i := 0; i < len(sp)-1; i++ {
		if _, ok := d.files[sp[i]]; ok {
			return &fs.PathError{Op: "mkdirall", Path: p, Err: fmt.Errorf("%q is a file", strings.Join(sp[:i+1], "/"))}
		}
		v, ok := d.dirs[sp[i]]
		if !ok {
			dir := Directory{name: sp[i], modTime: time.Now()}
			d.dirs[sp[i]] = dir
			d = dir
			continue
		}
		d = v
	}
	return nil
}

// WriteFile writes file with path "name" to the filesystem. If the file
// already exists, this will overwrite the existing file. data must not
// be mutated after it is passed here. perm must be 0444.
// This implements github.com/gopherfs/fs.Writer .
func (f *FS) WriteFile(name string, data []byte, perm fs.FileMode) error {
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
		d = f.root
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
func (f *FS) Remove(name string) error {
	return f.remove(name, false)
}

// RemoveAll removes path and any children it contains. It removes
// everything it can but returns the first error it encounters.
// If the path does not exist, RemoveAll returns nil (no error).
// If there is an error, it will be of type *fs.PathError.
func (f *FS) RemoveAll(path string) error {
	return f.remove(path, true)
}

func (f *FS) remove(name string, children bool) error {
	name = strings.TrimPrefix(path.Clean(name), "/")
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: "remove", Path: name, Err: fmt.Errorf("invalid name for a path as reported by fs.ValidPath()")}
	}

	p := strings.Split(name, "/")
	if len(p) == 0 {
		return &fs.PathError{Op: "remove", Path: name, Err: fmt.Errorf("path is invalid")}
	}

	d := f.root
	for i := 0; i < len(p)-1; i++ {
		v, ok := d.dirs[p[i]]
		if !ok {
			return &fs.PathError{Op: "remove", Path: name, Err: fmt.Errorf("could not find directory %q", strings.Join(p, "/"))}
		}
		d = v
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

func dataToFile(name string, data []byte) (File, error) {
	b := bytes.NewBuffer(data)

	r, _, err := b.ReadRune()
	if err != nil {
		return File{}, fmt.Errorf("could not read rune in WriteFile data: %s", err)
	}

	switch r {
	case openBrace:
		return File{}, fmt.Errorf("file cannot be a JSON object")
	case openBracket:
		return File{}, fmt.Errorf("file cannot be a JSON array")
	case 't':
		if len(data) == 4 {
			if byteSlice2String(data) == "true" {
				return File{
					name:    name,
					modTime: time.Now(),
					t:       FTBool,
					value:   data,
				}, nil
			}
		}
	case 'f':
		if len(data) == 5 {
			if byteSlice2String(data) == "false" {
				return File{
					name:    name,
					modTime: time.Now(),
					t:       FTBool,
					value:   data,
				}, nil
			}
		}
	case 'n':
		if len(data) == 4 {
			if byteSlice2String(data) == "null" {
				return File{
					name:    name,
					modTime: time.Now(),
					t:       FTNull,
					value:   data,
				}, nil
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
					return File{
						name:    name,
						modTime: time.Now(),
						t:       t,
						value:   data,
					}, nil
				}
				return File{}, fmt.Errorf("could not read rune in WriteFile data: %s", err)
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
	return File{name: name, modTime: time.Now(), t: FTString, value: data}, nil
}
