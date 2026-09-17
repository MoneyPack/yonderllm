package provider

import (
	"context"
	"strings"
	"testing"
)

func TestStreamReportsEmbeddedErrorAfterHeartbeats(t *testing.T) {
	srv, _ := sseServer(t, []string{": heartbeat\n\n", frame(`{"error":{"code":400,"message":"upstream error sk-abcdefghijklmnopqrstuv","type":"server_error"}}`), "data: [DONE]\n\n"})
	p := NewChatCompat("stub", srv.URL, "", WithHTTPClient(srv.Client()))
	_, _, err := collect(p.Stream(context.Background(), Request{Model: "test"}))
	if err == nil || !strings.Contains(err.Error(), "upstream error") {
		t.Fatalf("embedded error = %v", err)
	}
	if strings.Contains(err.Error(), "sk-abcdefghijklmnopqrstuv") {
		t.Fatal("error exposed key")
	}
}

func TestStreamErrorDoesNotFlushPendingTool(t *testing.T) {
	srv, _ := sseServer(t, []string{
		frame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"write","arguments":"{}"}}]}}]}`),
		frame(`{"error":{"message":"failed"}}`), "data: [DONE]\n\n",
	})
	p := NewChatCompat("stub", srv.URL, "", WithHTTPClient(srv.Client()))
	_, chunks, err := collect(p.Stream(context.Background(), Request{Model: "test"}))
	if err == nil {
		t.Fatal("error ignored")
	}
	for _, chunk := range chunks {
		if len(chunk.ToolCalls) > 0 {
			t.Fatal("failed stream released tool call")
		}
	}
}

func TestStreamErrorRedactsCredentialFromDiagnostics(t *testing.T) {
	secret := "sk-abcdefghijklmnopqrstuv"
	srv, _ := sseServer(t, []string{frame(`{"error":{"message":"bad ` + secret + `"}}`)})
	p := NewChatCompat("stub", srv.URL, "", WithHTTPClient(srv.Client()))
	_, _, err := collect(p.Stream(context.Background(), Request{Model: "test"}))
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("diagnostic = %v", err)
	}
}

func TestCompatibilityCanOmitStreamOptions(t *testing.T) {
	srv, seen := sseServer(t, []string{"data: [DONE]\n\n"})
	p := NewChatCompat("strict", srv.URL, "", WithoutStreamOptions())
	_, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
	if err != nil {
		t.Fatal(err)
	}
	if seen.StreamOptions != nil {
		t.Fatal("strict server received stream_options")
	}
}

func TestStreamUnexpectedEOFDoesNotFlushTools(t *testing.T) {
	srv, _ := sseServer(t, []string{frame(`{"choices":[{"delta":{"content":"partial","tool_calls":[{"index":0,"id":"call","function":{"name":"write","arguments":"{}"}}]}}]}`)})
	p := NewChatCompat("stub", srv.URL, "", WithHTTPClient(srv.Client()))
	text, chunks, err := collect(p.Stream(context.Background(), Request{Model: "test"}))
	if text != "partial" || err == nil {
		t.Fatalf("text=%q error=%v", text, err)
	}
	for _, c := range chunks {
		if len(c.ToolCalls) > 0 {
			t.Fatal("EOF flushed tool call")
		}
	}
}
