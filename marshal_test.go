package jsonfs

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kylelemons/godebug/pretty"
)

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

func BenchmarkMarshalJSON(b *testing.B) {
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

func BenchmarkMarshalJSONStdlib(b *testing.B) {
	m := map[string]any{}
	if err := json.Unmarshal([]byte(jsonText), &m); err != nil {
		panic(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(m); err != nil {
			panic(err)
		}
	}
}
