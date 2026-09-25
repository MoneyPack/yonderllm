package cli

import (
	"encoding/json"
	"testing"
)

func TestVersionJSONIsAvailableWithoutProviderConfiguration(t *testing.T) {
	r := runBare(t, "version", "--json")
	wantCode(t, r, 0)
	var info map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &info); err != nil {
		t.Fatal(err)
	}
	if info["version"] != resolvedVersion() || info["schema_version"] != float64(1) || info["go_version"] == "" || info["platform"] == "" {
		t.Fatal(info)
	}
}

func TestResolvedVersionKeepsAStampedValue(t *testing.T) {
	prev := Version
	t.Cleanup(func() { Version = prev })

	Version = "0.2.0"
	if got := resolvedVersion(); got != "0.2.0" {
		t.Fatalf("stamped Version ignored: got %q", got)
	}

	Version = "dev"
	if got := resolvedVersion(); got != "dev" {
		// A checkout build reports "(devel)" in build info, which must stay as
		// "dev". A go-install binary would report a module version instead;
		// that path is exercised by the release smoke test on a stamped build.
		t.Fatalf("unstamped checkout should report dev, got %q", got)
	}
}
