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
	for _, body := range []string{`retry_attempts = 4`, `retry_backoff_ms = -1`, `request_timeout_seconds = 3601`, `output_format = "xml"`, "[providers.groq.header_env]\nAuthorization = \"KEY\""} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
