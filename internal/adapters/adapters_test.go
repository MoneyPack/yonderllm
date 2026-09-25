package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoneyPack/yonderllm/internal/config"
	"github.com/MoneyPack/yonderllm/internal/provider"
)

// The factory must apply every provider setting the config knows about. This
// is the test that would have caught the copy which forgot omit_stream_options.
func TestNewAppliesEveryProviderSetting(t *testing.T) {
	var (
		gotAuth, gotTenant string
		gotStreamOptions   bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get("X-Tenant")
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		_, gotStreamOptions = body["stream_options"]
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	t.Setenv("ADAPTERS_TEST_KEY", "adapter-key")
	t.Setenv("ADAPTERS_TEST_TENANT", "tenant-42")
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "provider = 'local'\nfallbacks = []\n[providers.local]\nbase_url = '" + srv.URL + "'\nmodel = 'm'\napi_key_env = 'ADAPTERS_TEST_KEY'\nomit_stream_options = true\n[providers.local.header_env]\nX-Tenant = 'ADAPTERS_TEST_TENANT'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	p := New("local", cfg.Providers["local"])
	if p.Name() != "local" {
		t.Errorf("Name() = %q, want local", p.Name())
	}
	for _, err := range p.Stream(context.Background(), provider.Request{Model: "m"}) {
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
	}
	if gotAuth != "Bearer adapter-key" {
		t.Errorf("Authorization = %q, want the configured key", gotAuth)
	}
	if gotTenant != "tenant-42" {
		t.Errorf("X-Tenant = %q, want the header_env value", gotTenant)
	}
	if gotStreamOptions {
		t.Error("stream_options was sent despite omit_stream_options = true")
	}
}

// Extra options are the caller's, applied last so they can override what the
// config derived — here, the HTTP client.
func TestNewAppliesExtraOptionsLast(t *testing.T) {
	var called bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return nil, http.ErrHandlerTimeout
	})}
	p := New("x", config.ProviderConfig{BaseURL: "http://unreachable.invalid/v1"}, provider.WithHTTPClient(client))
	for _, err := range p.Stream(context.Background(), provider.Request{Model: "m"}) {
		if err == nil {
			t.Fatal("expected the injected transport's error")
		}
	}
	if !called {
		t.Error("the injected HTTP client was not used")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
