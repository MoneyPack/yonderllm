package config

import "testing"

func TestExplicitZeroDailyCapIsHonored(t *testing.T) {
	cfg, err := Load(writeConfig(t, "daily_cap = 0"))
	if err != nil || cfg.DailyCap != 0 {
		t.Fatalf("cap=%d error=%v", cfg.DailyCap, err)
	}
}

func TestRuntimeSettingsAndBounds(t *testing.T) {
	cfg, err := Load(writeConfig(t, `retry_attempts = 2
retry_backoff_ms = 25
request_timeout_seconds = 60
output_format = "json"
[providers.groq]
omit_stream_options = true
[providers.groq.header_env]
X-Project = "PROJECT_HEADER"`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RetryAttempts != 2 || cfg.RetryBackoffMS != 25 || cfg.RequestTimeoutSeconds != 60 || cfg.OutputFormat != "json" || !cfg.Providers["groq"].OmitStreamOptions || cfg.Providers["groq"].HeaderEnv["X-Project"] != "PROJECT_HEADER" {
		t.Fatal("settings not merged")
	}
	for _, body := range []string{`retry_attempts = 4`, `retry_backoff_ms = -1`, `request_timeout_seconds = 3601`, `output_format = "xml"`, "[providers.groq.header_env]\nAuthorization = \"KEY\"", "[providers.groq]\ncontext_window = -1"} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

// context_window is optional and per provider: setting it on one provider
// leaves the others at zero, which the session reads as "unknown".
func TestContextWindowIsPerProviderAndOptional(t *testing.T) {
	cfg, err := Load(writeConfig(t, "[providers.groq]\ncontext_window = 131072\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers["groq"].ContextWindow; got != 131072 {
		t.Errorf("groq context_window = %d, want 131072", got)
	}
	if got := cfg.Providers["gemini"].ContextWindow; got != 0 {
		t.Errorf("gemini context_window = %d, want 0 (unset)", got)
	}
	// Setting only the window must not erase the built-in model or URL.
	if cfg.Providers["groq"].Model == "" || cfg.Providers["groq"].BaseURL == "" {
		t.Errorf("merging context_window dropped other groq fields: %+v", cfg.Providers["groq"])
	}
}

// Defaults hands out a fresh value each time, so a caller that edits the
// result cannot change what the next Load starts from.
func TestDefaultsIsACopy(t *testing.T) {
	d := Defaults()
	d.Providers["groq"] = ProviderConfig{Model: "edited"}
	if Defaults().Providers["groq"].Model == "edited" {
		t.Fatal("editing Defaults() leaked into the built-in table")
	}
}
