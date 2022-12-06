package jsonfs

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/kylelemons/godebug/pretty"
)

var largeJSON string

func init() {
	b, err := os.ReadFile("large.json")
	if err != nil {
		panic(err)
	}
	largeJSON = ByteSlice2String(b)
}

func TestMarshalJSON(t *testing.T) {
	d, err := UnmarshalJSON(strings.NewReader(jsonText))
	if err != nil {
		panic(err)
	}

	file := &bytes.Buffer{}
	if err := MarshalJSON(file, d); err != nil {
		panic(err)
	}

	got, err := UnmarshalJSON(file)
	if err != nil {
		panic(err)
	}

	config := pretty.Config{IncludeUnexported: false}
	if diff := config.Compare(d, got); diff != "" {
		t.Errorf("TestMarshal: -want/+got:\n%s", diff)
	}
}

func BenchmarkMarshalJSONSmall(b *testing.B) {
	d, err := UnmarshalJSON(strings.NewReader(jsonText))
	if err != nil {
		panic(err)
	}
	file := &bytes.Buffer{}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		file.Reset()
		b.StartTimer()
		if err := MarshalJSON(file, d); err != nil {
			panic(err)
		}
	}
}

func BenchmarkMarshalJSONLarge(b *testing.B) {
	d, err := UnmarshalJSON(strings.NewReader(largeJSON))
	if err != nil {
		panic(err)
	}
	file := &bytes.Buffer{}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		file.Reset()
		b.StartTimer()
		if err := MarshalJSON(file, d); err != nil {
			panic(err)
		}
	}
}

func BenchmarkMarshalJSONStdlibSmall(b *testing.B) {
	m := map[string]any{}
	if err := json.Unmarshal(UnsafeGetBytes(jsonText), &m); err != nil {
		panic(err)
	}
	file := &bytes.Buffer{}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		file.Reset()
		b.StartTimer()

		buff, err := json.Marshal(m)
		if err != nil {
			panic(err)
		}
		if _, err := file.Write(buff); err != nil {
			panic(err)
		}
	}
}

func BenchmarkMarshalJSONStdlibLarge(b *testing.B) {
	m := map[string]any{}
	if err := json.Unmarshal(UnsafeGetBytes(largeJSON), &m); err != nil {
		panic(err)
	}
	file := &bytes.Buffer{}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		file.Reset()
		b.StartTimer()

		buff, err := json.Marshal(m)
		if err != nil {
			panic(err)
		}
		if _, err := file.Write(buff); err != nil {
			panic(err)
		}
	}
}
