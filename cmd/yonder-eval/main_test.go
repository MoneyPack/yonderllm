package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOfflineDefaultAndLiveOptIn(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), nil, &out, io.Discard); code != 0 {
		t.Fatalf("offline exit %d", code)
	}
	var r report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Mode != "fixture" || !r.Passed || len(r.Results) != 4 {
		t.Fatalf("unexpected offline report: %+v", r)
	}
	for _, args := range [][]string{{"--live"}, {"--provider", "surplus"}, {"--timeout", "0s"}, {"--max-tokens", "0"}} {
		out.Reset()
		if code := run(context.Background(), args, &out, io.Discard); code != 2 || out.Len() != 0 {
			t.Fatalf("invalid args %v produced exit=%d output=%q", args, code, out.String())
		}
	}
}

// Exercise the explicit live path against HTTP, without a paid endpoint or
// user's config. This catches resolver/settings drift hidden by stub providers.
func TestLiveAgainstLocalHTTPProvider(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-credential" {
			t.Error("wrong endpoint or authentication")
		}
		var request struct {
			Model     string                           `json:"model"`
			MaxTokens int                              `json:"max_tokens"`
			Messages  []struct{ Role, Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if request.Model != "chosen-model" || request.MaxTokens != 256 {
			t.Error("live settings were not applied")
		}
		prompt := request.Messages[0].Content
		answer := "42"
		tool, arguments := "", ""
		switch {
		case strings.Contains(prompt, "JSON"):
			answer = `{"status":"ok"}`
		case strings.Contains(prompt, "lookup"):
			answer, tool, arguments = "amber", "lookup", `{"key":"color"}`
		case strings.Contains(prompt, "request_write"):
			answer, tool, arguments = "denied", "request_write", `{"content":"hello"}`
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if tool != "" && request.Messages[len(request.Messages)-1].Role != "tool" {
			data, err := json.Marshal(map[string]any{"choices": []any{map[string]any{
				"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call-1", "type": "function", "function": map[string]string{"name": tool, "arguments": arguments}}}}, "finish_reason": "tool_calls",
			}}})
			if err != nil {
				t.Error(err)
				return
			}
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
			return
		}
		data, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": answer}, "finish_reason": "stop"}}})
		if err != nil {
			t.Error(err)
			return
		}
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
	}))
	defer srv.Close()
	t.Setenv("YONDER_EVAL_TEST_KEY", "fixture-credential")
	t.Setenv("YONDERLLM_PROVIDER", "")
	t.Setenv("YONDERLLM_MODEL", "")
	t.Setenv("YONDERLLM_MODE", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	text := fmt.Sprintf("provider = 'local'\nfallbacks = []\n[providers.local]\nbase_url = %q\nmodel = 'configured-model'\napi_key_env = 'YONDER_EVAL_TEST_KEY'\n", srv.URL)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"--live", "--config", path, "--provider", "local", "--model", "chosen-model"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("live exit=%d err=%s report=%s", code, stderr.String(), out.String())
	}
	var r report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 7 || r.Mode != "live" || !r.Passed || !r.Cancellation.Observed {
		t.Fatalf("unexpected live report/count: %+v requests=%d", r, requests.Load())
	}
	if strings.Contains(out.String(), "fixture-credential") {
		t.Fatal("credential in report")
	}
}
