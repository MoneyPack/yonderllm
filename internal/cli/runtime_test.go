package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MoneyPack/yonderllm/internal/config"
	"github.com/MoneyPack/yonderllm/internal/provider"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestResolverAppliesStrictServerOptionsAndHeaderEnv(t *testing.T) {
	t.Setenv("PROJECT_HEADER", "example-project")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, exists := body["stream_options"]; exists {
			t.Error("stream_options sent")
		}
		if r.Header.Get("X-Project") != "example-project" {
			t.Error("header missing")
		}
		fmt.Fprint(w, doneFrame())
	}))
	defer srv.Close()
	cfg := config.Config{Providers: map[string]config.ProviderConfig{"custom": {BaseURL: srv.URL, Model: "tiny", OmitStreamOptions: true, HeaderEnv: map[string]string{"X-Project": "PROJECT_HEADER"}}}}
	p, err := newResolver(cfg)("custom")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range p.Stream(context.Background(), provider.Request{Model: "tiny"}) {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfiguredJSONCanBeOverridden(t *testing.T) {
	h := newHarness(t, newStub(t, "hello"))
	data, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.configPath, append([]byte("output_format = \"json\"\n"), data...), 0600); err != nil {
		t.Fatal(err)
	}
	r := h.run(t, "run", "hello")
	wantCode(t, r, 0)
	lastEventOfType(t, r.stdout, "done")
	r = h.run(t, "run", "--json=false", "hello")
	wantCode(t, r, 0)
	if r.stdout != "hello\n" {
		t.Fatal(r.stdout)
	}
}
