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
	if info["version"] != Version || info["schema_version"] != float64(1) || info["go_version"] == "" || info["platform"] == "" {
		t.Fatal(info)
	}
}
