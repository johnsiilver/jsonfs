package jsonfs

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"sync"
	"time"
	"unicode"
	"unsafe"
)

var bufferPool = sync.Pool{
	New: func() any {
		return bufio.NewReader(nil)
	},
}

const (
	doubleQuote  = '"'
	singleQuote  = '\''
	openBracket  = '['
	closeBracket = ']'
	openBrace    = '{'
	closeBrace   = '}'
	colon        = ':'
	comma        = ','
)

// stateFn is a state function.
type stateFn func(ctx context.Context) stateFn

// UnmarshalJSON unmarshals a single JSON object from an io.Reader. This object
// must be an object inside {}. This should only be used for reading a file
// or single object contained in an io.Reader. We will use a bufio.Reader
// underneath, so this reader is not usable after.
func UnmarshalJSON(r io.Reader) (Directory, error) {
	var b *bufio.Reader
	if _, ok := r.(*bufio.Reader); ok {
		b = r.(*bufio.Reader)
	} else {
		b = bufferPool.Get().(*bufio.Reader)
		b.Reset(r)
	}
	defer func() { bufferPool.Put(b) }()

	m := newMessage(b, "")
	runSM(context.Background(), m.start)
	if m.err != nil {
		return Directory{}, m.err
	}
	return m.dir, nil
}

// Stream is a stream object from UnmarshalStream().
type Stream struct {
	// Dir is the JSON object as a Directory.
	Dir Directory
	// Err indicates that there was an error in the stream.
	Err error
}

// UnmarshalStream unmarshals a stream of JSON objects from a reader.
// This will handle both streams of objects.
func UnmarshalStream(ctx context.Context, r io.Reader) chan Stream {
	var b *bufio.Reader
	if _, ok := r.(*bufio.Reader); ok {
		b = r.(*bufio.Reader)
	} else {
		b = bufferPool.Get().(*bufio.Reader)
		b.Reset(r)
	}
	ch := make(chan Stream, 1)

	go func() {
		defer close(ch)
		for {
			dir, err := UnmarshalJSON(b)
			if err != nil {
				ch <- Stream{Err: err}
				return
			}
			if dir.name == "" && len(dir.files)+len(dir.dirs) == 0 {
				return
			}
			ch <- Stream{Dir: dir}
		}
	}()
	return ch
}

// runSM runs a statemachine starting at stateFn start.
func runSM(ctx context.Context, start stateFn) {
	current := start
	for {
		fn := current(ctx)
		if fn == nil {
			return
		}
		current = fn
	}
}

// messageSM handles JSON objects.
type messageSM struct {
	b         *bufio.Reader
	dir       Directory
	modTime   time.Time
	valueName string
	err       error
}

// newMessage creates a new messageSM statemachine.
func newMessage(b *bufio.Reader, dirName string) messageSM {
	return messageSM{
		b:   b,
		dir: newDir(dirName, time.Time{}), // Directory.modTime set in start()
	}
}

// start is the entry way into the messageSm.
func (m *messageSM) start(ctx context.Context) stateFn {
	m.modTime = time.Now()
	m.dir.modTime = m.modTime
	return m.open
}

// open handles the open bracce.
func (m *messageSM) open(ctx context.Context) stateFn {
	skipSpace(m.b)

	r, _, err := m.b.ReadRune()
	if err != nil {
		if err == io.EOF {
			return nil
		}
		m.err = err
		return nil
	}

	if r != openBrace {
		m.err = fmt.Errorf("object must start with {")
		return nil
	}

	// Special case, empty object {}
	b, err := m.b.Peek(1)
	if err != nil {
		m.err = err
		return nil
	}
	if rune(b[0]) == '}' {
		return nil
	}

	return m.parseKey
}

// parseKey parses an object key.
func (m *messageSM) parseKey(ctx context.Context) stateFn {
	skipSpace(m.b)

	r, _, err := m.b.ReadRune()
	if err != nil {
		m.err = err
		return nil
	}

	if r != '"' {
		m.err = fmt.Errorf("object key expected but did not find open double quote(\"), found %v", r)
		return nil
	}
	m.b.UnreadRune()
	s, err := getString(m.b)
	if err != nil {
		m.err = err
		return nil
	}
	m.valueName = byteSlice2String(s)

	return m.colon
}

// colon parses the colon after an object key.
func (m *messageSM) colon(ctx context.Context) stateFn {
	skipSpace(m.b)

	r, _, err := m.b.ReadRune()
	if err != nil {
		m.err = err
		return nil
	}

	if r != ':' {
		m.err = fmt.Errorf("object key not followed by colon :, was %q", r)
		return nil
	}

	return m.valueCheck
}

// valueCheck tries to determine the value of an object key so that we
// can go to the next state to handle it.
func (m *messageSM) valueCheck(ctx context.Context) stateFn {
	skipSpace(m.b)

	next, err := valueCheck(m.b)
	if err != nil {
		m.err = err
		return nil
	}

	if err := duplicateField(m.dir, m.valueName); err != nil {
		m.err = err
		return nil
	}

	switch next {
	case msgNext:
		nm := newMessage(m.b, m.valueName)
		runSM(ctx, nm.open)
		if nm.err != nil {
			m.err = nm.err
			return nil
		}

		m.dir.dirs[nm.dir.name] = nm.dir
		return m.commaClose
	case arrayNext:
		na := newArray(m.b, m.valueName, m.modTime)
		runSM(ctx, na.start)

		m.dir.dirs[na.dir.name] = na.dir
		return m.commaClose
	case stringNext:
		o, err := decodeString(m.b, m.valueName, m.modTime)
		if err != nil {
			m.err = err
			return nil
		}
		m.dir.files[m.valueName] = o
		return m.commaClose
	case trueNext:
		o, err := decodeBool(m.b, m.valueName, trueNext, m.modTime)
		if err != nil {
			m.err = err
			return nil
		}
		m.dir.files[m.valueName] = o
		return m.commaClose
	case falseNext:
		o, err := decodeBool(m.b, m.valueName, falseNext, m.modTime)
		if err != nil {
			m.err = err
			return nil
		}
		m.dir.files[m.valueName] = o
		return m.commaClose
	case numNext:
		o, err := decodeNumber(m.b, m.valueName, m.modTime)
		if err != nil {
			m.err = err
			return nil
		}
		m.dir.files[m.valueName] = o
		return m.commaClose
	case nullNext:
		o, err := decodeNull(m.b, m.valueName, m.modTime)
		if err != nil {
			m.err = err
			return nil
		}
		m.dir.files[m.valueName] = o
		return m.commaClose
	}

	m.err = fmt.Errorf("unexpected value type after key, got %v", next)
	return nil
}

// commaClose determines if we have another field in an object or object closure.
func (m *messageSM) commaClose(ctx context.Context) stateFn {
	skipSpace(m.b)

	x, err := m.b.Peek(1)
	if err != nil {
		m.err = fmt.Errorf("expecting comma after object or closing brace, found eol")
		return nil
	}
	r := rune(x[0])

	switch r {
	case closeBrace:
		return m.close
	case comma:
		return m.comma
	}
	m.err = fmt.Errorf("expecting a comma after field value or closing brace, got %v", string(r))
	return nil
}

// close handles a closing brace(}) of an object.
func (m *messageSM) close(ctx context.Context) stateFn {
	r, _, err := m.b.ReadRune()
	if err != nil {
		m.err = err
		return nil
	}
	if r != closeBrace {
		m.err = fmt.Errorf("expecting close brace, got %q", r)
		return nil
	}

	return nil
}

// comma handles a comma after a field value of an object.
func (m *messageSM) comma(ctx context.Context) stateFn {
	r, _, err := m.b.ReadRune()
	if err != nil {
		m.err = err
		return nil
	}
	if r != comma {
		m.err = fmt.Errorf("expecting comma, got %q", r)
		return nil
	}
	return m.parseKey
}

type arraySM struct {
	b       *bufio.Reader
	dir     Directory
	modTime time.Time
	item    int
	err     error
}

func newArray(b *bufio.Reader, name string, modTime time.Time) *arraySM {
	a := &arraySM{
		b:       b,
		dir:     newDir(name, modTime),
		modTime: modTime,
	}
	a.dir.isArray = true
	return a
}

func (m *arraySM) start(ctx context.Context) stateFn {
	return m.openBracket
}

func (m *arraySM) openBracket(ctx context.Context) stateFn {
	skipSpace(m.b)

	r, _, err := m.b.ReadRune()
	if err != nil {
		m.err = err
		return nil
	}

	if r != openBracket {
		m.err = fmt.Errorf("expected array open bracket, found %q", r)
		return nil
	}
	// Special case, empty array []
	b, err := m.b.Peek(1)
	if err != nil {
		m.err = err
		return nil
	}
	if rune(b[0]) == ']' {
		return nil
	}

	return m.valueCheck
}

func (m *arraySM) valueCheck(ctx context.Context) stateFn {
	defer func() { m.item++ }()

	skipSpace(m.b)

	next, err := valueCheck(m.b)
	if err != nil {
		m.err = err
		return nil
	}

	valueName := strconv.Itoa(m.item)

	switch next {
	case msgNext:
		nm := newMessage(m.b, strconv.Itoa(m.item))
		runSM(ctx, nm.start)
		if nm.err != nil {
			m.err = nm.err
			return nil
		}

		m.dir.dirs[nm.dir.name] = nm.dir
		return m.commaClose
	case arrayNext:
		na := newArray(m.b, strconv.Itoa(m.item), m.modTime)
		runSM(ctx, na.start)

		m.dir.dirs[na.dir.name] = na.dir
		return m.commaClose
	case stringNext:
		o, err := decodeString(m.b, valueName, m.modTime)
		if err != nil {
			m.err = err
			return nil
		}
		m.dir.files[valueName] = o
		return m.commaClose
	case trueNext:
		o, err := decodeBool(m.b, valueName, trueNext, m.modTime)
		if err != nil {
			m.err = err
			return nil
		}
		m.dir.files[valueName] = o
		return m.commaClose
	case falseNext:
		o, err := decodeBool(m.b, valueName, falseNext, m.modTime)
		if err != nil {
			m.err = err
			return nil
		}
		m.dir.files[valueName] = o
		return m.commaClose
	case numNext:
		o, err := decodeNumber(m.b, valueName, m.modTime)
		if err != nil {
			m.err = err
			return nil
		}
		m.dir.files[valueName] = o
		return m.commaClose
	case nullNext:
		o, err := decodeNull(m.b, valueName, m.modTime)
		if err != nil {
			m.err = err
			return nil
		}
		m.dir.files[valueName] = o
		return m.commaClose
	}

	m.err = fmt.Errorf("unexpected array value type got %v", next)
	return nil
}

// commaClose determines if we have another field in an object or object closure.
func (m *arraySM) commaClose(ctx context.Context) stateFn {
	skipSpace(m.b)

	x, err := m.b.Peek(1)
	if err != nil {
		m.err = fmt.Errorf("expecting comma after array value or closing bracket, found eol")
		return nil
	}
	r := rune(x[0])

	switch r {
	case closeBracket:
		return m.close
	case comma:
		return m.comma
	}
	m.err = fmt.Errorf("expecting a comma after field value or closing bracket, got %v", string(r))
	return nil
}

// close handles a closing bracket(]) of an array.
func (m *arraySM) close(ctx context.Context) stateFn {
	r, _, err := m.b.ReadRune()
	if err != nil {
		m.err = err
		return nil
	}
	if r != closeBracket {
		m.err = fmt.Errorf("expecting close bracket, got %q", r)
		return nil
	}

	return nil
}

// comma handles a comma after a field value of an object.
func (m *arraySM) comma(ctx context.Context) stateFn {
	r, _, err := m.b.ReadRune()
	if err != nil {
		m.err = err
		return nil
	}
	if r != comma {
		m.err = fmt.Errorf("expecting comma, got %q", r)
		return nil
	}
	return m.valueCheck
}

// skipSpace skips all spaces in the reader.
func skipSpace(b *bufio.Reader) {
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

type next int

const (
	unknownNext next = iota
	msgNext
	arrayNext
	stringNext
	trueNext
	falseNext
	numNext
	nullNext
)

func valueCheck(b *bufio.Reader) (next, error) {
	skipSpace(b)

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

func decodeString(b *bufio.Reader, name string, modTime time.Time) (File, error) {
	s, err := getString(b)
	if err != nil {
		return File{}, err
	}

	return File{
		name:    name,
		modTime: modTime,
		t:       FTString,
		value:   s,
	}, nil
}

// decodeBool decodes a boolean value.
func decodeBool(b *bufio.Reader, name string, hint next, modTime time.Time) (File, error) {
	var buff []byte
	if hint == trueNext {
		buff = make([]byte, 4)
	} else {
		buff = make([]byte, 5)
	}

	_, err := b.Read(buff)
	if err != nil {
		return File{}, fmt.Errorf("decoding bool, but unexpected error: %v", err)
	}
	s := byteSlice2String(buff)
	switch {
	case s == "false":
		return File{
			name:    name,
			modTime: modTime,
			value:   buff,
			t:       FTBool,
		}, nil
	case s == "true":
		return File{
			name:    name,
			modTime: modTime,
			value:   buff,
			t:       FTBool,
		}, nil
	}
	return File{}, fmt.Errorf("expected bool value true or false, got %v", s)
}

// decodeNull decodes a null value.
func decodeNull(b *bufio.Reader, name string, modTime time.Time) (File, error) {
	buff := make([]byte, 0, 4)

	_, err := b.Read(buff)
	if err != nil {
		return File{}, fmt.Errorf("decoding null, but unexpected error: %v", err)
	}

	if byteSlice2String(buff) != "null" {
		return File{}, fmt.Errorf("expected null, found %v", byteSlice2String(buff))
	}

	return File{
		name:    name,
		modTime: modTime,
		value:   buff,
		t:       FTNull,
	}, nil
}

// decodeNumber decodes a number value.
// TODO(jdoak): Doesn't handle the whole E or hex value stuff.
func decodeNumber(b *bufio.Reader, name string, modTime time.Time) (File, error) {
	buff := make([]byte, 0, 5)
	float := false
	for {
		r, _, err := b.ReadRune()
		if err != nil {
			return File{}, err
		}
		if !unicode.IsNumber(r) {
			if r != '.' {
				b.UnreadRune()
				break
			}
			buff = append(buff, byte(r))
			float = true
			continue
		}
		buff = append(buff, byte(r))
	}
	if len(buff) == 0 {
		return File{}, fmt.Errorf("expected key to have number, but did not")
	}

	o := File{
		name:    name,
		modTime: modTime,
		value:   buff,
		t:       FTInt,
	}
	if float {
		o.t = FTFloat
	}
	return o, nil
}

// getString gets a string from the Reader. The first character is considered
// to be the quote character. We handle escaping a quote with \.
func getString(b *bufio.Reader) ([]byte, error) {
	quote, _, _ := b.ReadRune()

	s, err := b.ReadBytes('"')
	if err != nil {
		return nil, fmt.Errorf("string did not end with %q: %s", quote, err)
	}

	return s[:len(s)-1], nil
}

func duplicateField(dir Directory, name string) error {
	if _, ok := dir.dirs[name]; ok {
		return fmt.Errorf("had duplicate field named %q", name)
	}
	if _, ok := dir.files[name]; ok {
		return fmt.Errorf("had duplicate field named %q", name)
	}
	return nil
}

// unsafeGetBytes extracts the []byte from a string. Use cautiously.
func unsafeGetBytes(s string) []byte {
	return (*[0x7fff0000]byte)(unsafe.Pointer(
		(*reflect.StringHeader)(unsafe.Pointer(&s)).Data),
	)[:len(s):len(s)]
}
