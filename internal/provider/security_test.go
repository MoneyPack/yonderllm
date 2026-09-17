package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestErrorsRedactExactCredentialEvenWithoutKnownPrefix(t *testing.T) {
	const key = "short.custom!key"
	for _, status := range []int{200, 400, 401, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				body := `{"error":{"message":"credential=` + key + ` rejected"}}`
				if status == 200 {
					body = frame(body)
				}
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			p := NewChatCompat("stub", srv.URL, key)
			_, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
			if err == nil || strings.Contains(err.Error(), key) {
				t.Fatalf("credential leaked: %v", err)
			}
		})
	}
}

func TestProviderDoesNotFollowRedirectWithCredentials(t *testing.T) {
	requests := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(401) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer origin.Close()
	p := NewChatCompat("stub", origin.URL, "secret")
	_, _, err := collect(p.Stream(context.Background(), Request{Model: "tiny"}))
	if err == nil || requests != 0 {
		t.Fatalf("followed credentialed redirect: requests=%d err=%v", requests, err)
	}
}
