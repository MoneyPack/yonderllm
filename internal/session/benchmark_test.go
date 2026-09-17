package session

import (
	"strings"
	"testing"
	"yonderllm/internal/provider"
)

func BenchmarkHistoryMessages(b *testing.B) {
	h := History{}
	h.SetSystem("Be helpful")
	for range 1000 {
		h.Append(provider.RoleUser, "hello world")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = h.Messages()
	}
}

func BenchmarkHistoryPrompt(b *testing.B) {
	h := History{}
	for range 1000 {
		h.Append(provider.RoleUser, strings.Repeat("hello world ", 100))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = h.Prompt(4096)
	}
}
