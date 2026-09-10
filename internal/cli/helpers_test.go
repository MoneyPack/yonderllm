// Shared test harness for the cli package.
//
// Every command in this package is exercised through the same door the real
// binary uses: Execute, with explicit streams. Nothing here reaches into
// production code for a test-only seam. Instead a stub HTTP server speaks the
// chat-completions wire format, and a temporary config.toml points the groq
// provider at it. The environment is scrubbed so a developer's real API keys
// or YONDERLLM_* overrides cannot change a result.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testKeyEnv is the environment variable the temporary config nominates for
// the groq provider, so tests never depend on GROQ_API_KEY being set.
const testKeyEnv = "YONDERLLM_TEST_KEY"

// stubRequest is the subset of the chat-completions request body that tests
// care about. It mirrors the wire shape produced by provider.ChatCompat.
type stubRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Stream    bool   `json:"stream"`
	Messages  []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// stub is a fake provider backend. It answers /chat/completions with a
// server-sent event stream and /models with a catalogue, and records the last
// chat request it saw so tests can assert on what the session sent.
type stub struct {
	server *httptest.Server
	frames []string
	models []stubModel
	last   stubRequest
	seen   bool
}

// stubModel mirrors the subset of a chat-completions catalogue entry the
// adapter reads. Pricing is a pointer so a test can withhold it entirely and
// exercise the unknown tier, which is the shape real backends that publish no
// rates return.
type stubModel struct {
	ID            string       `json:"id"`
	ContextWindow int          `json:"context_window,omitempty"`
	Pricing       *stubPricing `json:"pricing,omitempty"`
}

// stubPricing carries the decimal per-token strings real backends send.
type stubPricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

// cheapPricing is comfortably under the cheap threshold: $0.07 and $0.30 per
// million tokens.
func cheapPricing() *stubPricing {
	return &stubPricing{Prompt: "0.00000007", Completion: "0.00000030"}
}

// paidPricing is comfortably over it: $2.00 and $4.00 per million tokens.
func paidPricing() *stubPricing {
	return &stubPricing{Prompt: "0.00000200", Completion: "0.00000400"}
}

// freePricing is a genuinely-free published rate, which is not the same as no
// rate at all.
func freePricing() *stubPricing {
	return &stubPricing{Prompt: "0", Completion: "0"}
}

// newStub starts a backend that streams the given text as a single content
// delta and offers a small default catalogue.
func newStub(t *testing.T, deltas ...string) *stub {
	t.Helper()

	frames := []string{rolePrimingFrame()}
	for _, d := range deltas {
		frames = append(frames, contentFrame(d))
	}
	frames = append(frames, doneFrame())

	return newStubWithFrames(t, frames)
}

// newStubWithFrames starts a backend that replays exactly the frames given,
// for tests that need malformed or unusual streams.
func newStubWithFrames(t *testing.T, frames []string) *stub {
	t.Helper()

	s := &stub{
		frames: frames,
		models: []stubModel{
			{ID: "llama-3.1-8b-instant", ContextWindow: 131072, Pricing: cheapPricing()},
			{ID: "llama-3.3-70b-versatile", ContextWindow: 32768, Pricing: paidPricing()},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", s.handleChat(t))
	mux.HandleFunc("/models", s.handleModels(t))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected path %q", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})

	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *stub) handleChat(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &s.last); err != nil {
			t.Errorf("request body is not valid JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.seen = true

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}
		for _, f := range s.frames {
			if _, err := io.WriteString(w, f); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *stub) handleModels(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(map[string]any{"data": s.models}); err != nil {
			t.Errorf("encoding model list: %v", err)
		}
	}
}

// rolePrimingFrame is the contentless opening frame real backends send; the
// adapter drops it.
func rolePrimingFrame() string {
	return frame(`{"choices":[{"delta":{"role":"assistant"}}]}`)
}

func contentFrame(text string) string {
	payload, err := json.Marshal(map[string]any{
		"choices": []any{
			map[string]any{"delta": map[string]any{"content": text}},
		},
	})
	if err != nil {
		panic(err)
	}
	return frame(string(payload))
}

// doneFrame closes the stream with a finish reason, usage, and the sentinel.
func doneFrame() string {
	stop := frame(`{"choices":[{"delta":{},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
	return stop + frame("[DONE]")
}

func frame(payload string) string {
	return "data: " + payload + "\n\n"
}

// harness bundles a stub backend with a temporary config file and gives tests
// a single entry point for running the CLI.
type harness struct {
	stub       *stub
	configPath string
	dir        string
}

// scrubEnv neutralises anything the developer's shell might be carrying, so a
// real API key or a YONDERLLM_* override cannot change a result. The stub's
// own key variable is set in its place.
func scrubEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"YONDERLLM_PROVIDER",
		"YONDERLLM_MODEL",
		"YONDERLLM_MODE",
		"YONDERLLM_CONFIG",
		"GROQ_API_KEY",
		"GEMINI_API_KEY",
		"OPENROUTER_API_KEY",
		"SURPLUS_API_KEY",
	} {
		t.Setenv(key, "")
	}
	t.Setenv(testKeyEnv, "test-key")
}

// newHarness scrubs the environment, writes a config that points groq at the
// stub, and returns the pieces a test needs to run commands.
func newHarness(t *testing.T, s *stub) *harness {
	t.Helper()

	scrubEnv(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Only the fields under test are overridden; mergeTOML layers the rest of
	// the defaults underneath. An explicit empty fallback list keeps a failing
	// stub from silently retrying against a real provider.
	body := fmt.Sprintf(`fallbacks = []

[providers.groq]
base_url = %q
model = "llama-3.1-8b-instant"
api_key_env = %q
`, s.server.URL, testKeyEnv)

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	return &harness{stub: s, configPath: path, dir: dir}
}

// result captures everything one CLI invocation produced.
type result struct {
	code   int
	stdout string
	stderr string
}

// run executes the CLI with the harness config prepended to the arguments.
func (h *harness) run(t *testing.T, args ...string) result {
	t.Helper()
	return h.runWithInput(t, "", args...)
}

// runWithInput is run with something on standard input, for the piped-prompt
// paths in ask and run.
func (h *harness) runWithInput(t *testing.T, stdin string, args ...string) result {
	t.Helper()

	full := append([]string{"--config", h.configPath}, args...)
	var out, errOut bytes.Buffer
	code := Execute(full, strings.NewReader(stdin), &out, &errOut)

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// runBare executes the CLI without a config flag, for tests about config
// discovery itself. YONDERLLM_CONFIG still points into the temp directory so
// nothing touches the developer's real home.
func runBare(t *testing.T, args ...string) result {
	t.Helper()

	var out, errOut bytes.Buffer
	code := Execute(args, strings.NewReader(""), &out, &errOut)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// wantContains fails the test unless every fragment appears in the text.
func wantContains(t *testing.T, label, text string, fragments ...string) {
	t.Helper()
	for _, f := range fragments {
		if !strings.Contains(text, f) {
			t.Errorf("%s does not contain %q\n--- %s ---\n%s", label, f, label, text)
		}
	}
}

// wantNotContains fails the test if any fragment appears in the text.
func wantNotContains(t *testing.T, label, text string, fragments ...string) {
	t.Helper()
	for _, f := range fragments {
		if strings.Contains(text, f) {
			t.Errorf("%s unexpectedly contains %q\n--- %s ---\n%s", label, f, label, text)
		}
	}
}

// wantCode fails the test unless the exit code matches, reporting both streams
// so a surprise failure explains itself.
func wantCode(t *testing.T, r result, code int) {
	t.Helper()
	if r.code != code {
		t.Errorf("exit code = %d, want %d\n--- stdout ---\n%s\n--- stderr ---\n%s",
			r.code, code, r.stdout, r.stderr)
	}
}

// decodeJSON parses stdout as a single JSON document.
func decodeJSON[T any](t *testing.T, text string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("output is not valid JSON: %v\n--- output ---\n%s", err, text)
	}
	return v
}

// decodeNDJSON parses stdout as one JSON document per non-empty line.
func decodeNDJSON[T any](t *testing.T, text string) []T {
	t.Helper()
	var out []T
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("line is not valid JSON: %v\n--- line ---\n%s", err, line)
		}
		out = append(out, v)
	}
	return out
}
