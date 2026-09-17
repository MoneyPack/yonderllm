package config

import (
	"strings"
	"testing"
)

func TestEndpointRejectsEmbeddedCredentials(t *testing.T) {
	for _, endpoint := range []string{"https://user:secret@example.com/v1", "https://example.com/v1?api_key=secret", "file:///tmp/socket"} {
		_, err := Load(writeConfig(t, `[providers.groq]
base_url = "`+endpoint+`"`))
		if err == nil {
			t.Fatalf("accepted unsafe endpoint %s", endpoint)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("error leaked URL credentials")
		}
	}
}
