package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerSetsAuthorizationHeader(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := Bearer("s3cr3t").Apply(request); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if got, want := request.Header.Get("Authorization"), "Bearer s3cr3t"; got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
}

func TestBearerRejectsEmptyToken(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := Bearer("").Apply(request); err == nil {
		t.Fatal("Apply accepted an empty token")
	}
}

func TestBasicSetsCredentials(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := Basic("alice", "open sesame").Apply(request); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	username, password, ok := request.BasicAuth()
	if !ok {
		t.Fatal("request does not carry basic authentication")
	}
	if username != "alice" || password != "open sesame" {
		t.Fatalf("credentials = %q/%q, want alice/open sesame", username, password)
	}
}

func TestHeaderSetsSingleHeader(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := Header("X-API-Key", "key-1").Apply(request); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if got := request.Header.Get("X-API-Key"); got != "key-1" {
		t.Fatalf("X-API-Key = %q, want key-1", got)
	}
}

func TestHeaderRejectsEmptyName(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := Header("", "value").Apply(request); err == nil {
		t.Fatal("Apply accepted an empty header name")
	}
}

func TestHeadersSetsEveryHeader(t *testing.T) {
	t.Parallel()

	headers := map[string]string{"X-Key": "value", "X-Tenant": "acme"}
	authenticator := Headers(headers)
	headers["X-Key"] = "mutated"

	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := authenticator.Apply(request); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if got := request.Header.Get("X-Key"); got != "value" {
		t.Fatalf("X-Key = %q, want the value captured at construction", got)
	}
	if got := request.Header.Get("X-Tenant"); got != "acme" {
		t.Fatalf("X-Tenant = %q, want acme", got)
	}
}

func TestHeadersRejectsEmptyName(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := Headers(map[string]string{"": "value"}).Apply(request); err == nil {
		t.Fatal("Apply accepted an empty header name")
	}
}

func TestNoopLeavesRequestUnchanged(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := Noop().Apply(request); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(request.Header) != 0 {
		t.Fatalf("headers = %v, want none", request.Header)
	}
}

func TestAuthenticatorFunc(t *testing.T) {
	t.Parallel()

	var seen *http.Request
	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	var authenticator Authenticator = AuthenticatorFunc(func(incoming *http.Request) error {
		seen = incoming
		incoming.Header.Set("X-Seen", "yes")
		return nil
	})
	if err := authenticator.Apply(request); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if seen != request {
		t.Fatal("AuthenticatorFunc did not receive the request")
	}
	if got := request.Header.Get("X-Seen"); got != "yes" {
		t.Fatalf("X-Seen = %q, want yes", got)
	}
}
