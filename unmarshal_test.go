package jsonfs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/kylelemons/godebug/pretty"
	"github.com/nsf/jsondiff"
)

var jsonText = `
{"widget": {
    "debug": "on",
    "window": {
        "title": "Sample Konfabulator Widget",
        "name": "main_window",
        "width": 500,
        "height": 500
    },
    "image": { 
        "src": "Images/Sun.png",
        "name": "sun1",
        "hOffset": 250,
        "vOffset": 250,
        "alignment": "center"},
    "text": {
		"array": [1, "hello", 2.3, true, false, {"say": "hello"}]
    }
}}    
`

// TestUnmarshal tests the full unmarshal of a JSON string we want to handle.
func TestUnmarshal(t *testing.T) {
	r := strings.NewReader(jsonText)
	d, err := UnmarshalJSON(r)
	if err != nil {
		panic(err)
	}

	if err := compareKeys(d, []string{"widget"}); err != nil {
		t.Fatalf("TestUnmarshal: level0 keys: %s", err)
	}
	d, _ = d.GetDir("widget")
	level1 := d

	if err := compareKeys(d, []string{"debug", "image", "text", "window"}); err != nil {
		t.Fatalf("TestUnmarshal: level1 keys: %s", err)
	}
	d, _ = d.GetDir("window")
	if err := compareKeys(d, []string{"height", "name", "title", "width"}); err != nil {
		t.Fatalf("TestUnmarshal: level2 keys: %s", err)
	}

	f, err := d.GetFile("height")
	if err != nil {
		t.Fatalf("TestUnmarshal: widget.height() had error: %s", err)
	}
	i, err := f.Int()
	if err != nil {
		t.Fatalf("TestUnmarshal: could not get Int for widget/height: %s", err)
	}
	if i != 500 {
		t.Fatalf("TestUnmarshal: widget/height: got %d, want 500", i)
	}

	f, err = d.GetFile("name")
	if err != nil {
		t.Fatalf("TestUnmarshal: widget.name() had error: %s", err)
	}
	s, err := f.String()
	if err != nil {
		t.Fatalf("TestUnmarshal: could not get Int for widget/name: %s", err)
	}
	if s != "main_window" {
		t.Fatalf("TestUnmarshal: widget/name: got %s, want 'main_window'", s)
	}

	d, _ = level1.GetDir("image")
	if err := compareKeys(d, []string{"alignment", "hOffset", "name", "src", "vOffset"}); err != nil {
		t.Fatalf("TestUnmarshal: level2 keys: %s", err)
	}

	f, err = d.GetFile("alignment")
	if err != nil {
		t.Fatalf("TestUnmarshal: image.alignment had error: %s", err)
	}
	s, err = f.String()
	if err != nil {
		t.Fatalf("TestUnmarshal: could not get Int for image/alignment: %s", err)
	}
	if s != "center" {
		t.Fatalf("TestUnmarshal: image/alignment: got %s, want 'center'", s)
	}

	d, _ = level1.GetDir("text")
	if err := compareKeys(d, []string{"array"}); err != nil {
		t.Fatalf("TestUnmarshal: level3 keys: %s", err)
	}

	d, _ = d.GetDir("array")
	if err := compareKeys(d, []string{"0", "1", "2", "3", "4", "5"}); err != nil {
		t.Fatalf("TestUnmarshal: array keys: %s", err)
	}

	if d.Len() != 6 {
		t.Fatalf("TestUnmarshal: Directory.Size() was %d, want 6", d.Len())
	}
	f, err = d.GetFile("0")
	if err != nil {
		t.Fatalf("TestUnmarshal: array item 0: %s", err)
	}
	i, err = f.Int()
	if err != nil {
		t.Fatalf("TestUnmarshal: could not get Int array[0]: %s", err)
	}
	if i != 1 {
		t.Fatalf("TestUnmarshal: array[0]: got %d, want 1", i)
	}
	f, err = d.GetFile("1")
	if err != nil {
		t.Fatalf("TestUnmarshal: array item 1: %s", err)
	}
	s, err = f.String()
	if err != nil {
		t.Fatalf("TestUnmarshal: could not get String array[1]: %s", err)
	}
	if s != "hello" {
		t.Fatalf("TestUnmarshal: array[1]: got %s, want 'hello'", s)
	}
	f, err = d.GetFile("2")
	if err != nil {
		t.Fatalf("TestUnmarshal: array item 2: %s", err)
	}
	fl, err := f.Float()
	if err != nil {
		t.Fatalf("TestUnmarshal: could not get String array[2]: %s", err)
	}
	if fl != 2.3 {
		t.Fatalf("TestUnmarshal: array[2]: got %v, want '2.3'", fl)
	}
	f, err = d.GetFile("3")
	if err != nil {
		t.Fatalf("TestUnmarshal: array item 3: %s", err)
	}
	bl, err := f.Bool()
	if err != nil {
		t.Fatalf("TestUnmarshal: could not get String array[3]: %s", err)
	}
	if bl != true {
		t.Fatalf("TestUnmarshal: array[3]: got %v, want 'true'", bl)
	}
	f, err = d.GetFile("4")
	if err != nil {
		t.Fatalf("TestUnmarshal: array item 4: %s", err)
	}
	bl, err = f.Bool()
	if err != nil {
		t.Fatalf("TestUnmarshal: could not get String array[4]: %s", err)
	}
	if bl != false {
		t.Fatalf("TestUnmarshal: array[4]: got %v, want 'false'", bl)
	}
	d, err = d.GetDir("5")
	if err != nil {
		t.Fatalf("TestUnmarshal: array item 5: %s", err)
	}
	f, err = d.GetFile("say")
	if err != nil {
		t.Fatalf("TestUnmarshal: array[5].Say error: %s", err)
	}
	s, err = f.String()
	if err != nil {
		t.Fatalf("TestUnmarshal: could not get String array[5].Say: %s", err)
	}
	if s != "hello" {
		t.Fatalf("TestUnmarshal: array[5].Say: got %s, want 'hello'", s)
	}
}

func TestLargeFile(t *testing.T) {
	m := map[string]any{}
	if err := json.Unmarshal(unsafeGetBytes(largeJSON), &m); err != nil {
		panic(err)
	}

	r := strings.NewReader(largeJSON)
	d, err := UnmarshalJSON(r)
	if err != nil {
		panic(err)
	}

	var k int
	var entry any
	for k, entry = range m["Data"].([]any) {
		e := entry.(map[string]any)

		id := e["id"].(string)

		f, err := d.GetFile(fmt.Sprintf("Data/%d/id", k))
		if err != nil {
			panic(err)
		}

		if f.StringOrZV() != id {
			t.Fatalf("did not equal %s == %s", f.StringOrZV(), id)
		}
	}

	f := bytes.Buffer{}
	if err := MarshalJSON(&f, d); err != nil {
		panic(err)
	}

	diff, _ := jsondiff.Compare(UnsafeGetBytes(largeJSON), f.Bytes(), &jsondiff.Options{})
	if diff != jsondiff.FullMatch {
		t.Fatalf("TestLargeFile: got diff %v", diff)
	}
}

func TestIsQuote(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{`\"`, false},
		{`\\"`, true},
		{`\\\"`, false},
		{`\\\\"`, true},
	}

	for _, test := range tests {
		got := isQuote(UnsafeGetBytes(test.input))
		if got != test.want {
			t.Errorf("TestIsQuoteOrNot(%s): got %v, want %v", test.input, got, test.want)
		}
	}
}

func BenchmarkUnmarshalSmall(b *testing.B) {
	r := strings.NewReader(jsonText)
	b.ReportAllocs()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		r.Reset(jsonText)
		b.StartTimer()
		_, err := UnmarshalJSON(r)
		if err != nil {
			panic(err)
		}
	}
}

func BenchmarkUnmarshalLarge(b *testing.B) {
	r := strings.NewReader(largeJSON)
	b.ReportAllocs()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		r.Reset(largeJSON)
		b.StartTimer()

		_, err := UnmarshalJSON(r)
		if err != nil {
			panic(err)
		}
	}
}

func BenchmarkStandardUnmarshalSmall(b *testing.B) {
	r := strings.NewReader(jsonText)
	b.ReportAllocs()

	m := map[string]any{}
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		r.Reset(jsonText)
		b.StartTimer()
		if err := json.Unmarshal(unsafeGetBytes(jsonText), &m); err != nil {
			panic(err)
		}
	}
}

func BenchmarkStandardUnmarshalLarge(b *testing.B) {
	r := strings.NewReader(largeJSON)
	b.ReportAllocs()

	m := map[string]any{}
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		r.Reset(largeJSON)
		b.StartTimer()
		if err := json.Unmarshal(unsafeGetBytes(largeJSON), &m); err != nil {
			panic(err)
		}
	}
}

/*
func BenchmarkStandardUnmarshalDecoderLarge(b *testing.B) {
	b.ReportAllocs()

	dec := json.NewDecoder()

	m := map[string]any{}
	for i := 0; i < b.N; i++ {
		if err := json.Unmarshal(unsafeGetBytes(largeJSON), &m); err != nil {
			panic(err)
		}
	}
}
*/

func compareKeys(d Directory, want []string) error {
	got, err := d.ReadDir(0)
	if err != nil {
		panic(err)
	}
	gotNames := dirEntryToNames(got)
	if diff := pretty.Compare(want, gotNames); diff != "" {
		return fmt.Errorf("TestUnmarshal: level0 keys: -want/+got:\n%s", diff)
	}
	return nil
}

func dirEntryToNames(s []fs.DirEntry) []string {
	gotNames := make([]string, 0, len(s))
	for _, de := range s {
		gotNames = append(gotNames, de.Name())
	}
	return gotNames
}
