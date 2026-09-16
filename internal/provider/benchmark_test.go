package provider

import (
	"encoding/json"
	"testing"
)

func BenchmarkStreamFrame(b *testing.B) {
	data := []byte(`{"choices":[{"delta":{"content":"hello"}}]}`)
	b.ReportAllocs()
	for b.Loop() {
		var frame chatChunk
		if err := json.Unmarshal(data, &frame); err != nil {
			b.Fatal(err)
		}
	}
}
