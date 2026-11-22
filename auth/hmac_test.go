package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHMACSignsCanonicalRequest(t *testing.T) {
	t.Parallel()

	secret := []byte("shared-secret")
	fixed := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	authenticator := HMAC(secret, WithHMACClock(func() time.Time { return fixed }))

	request := httptest.NewRequest(http.MethodGet, "http://example.com/config.yaml?tenant=acme", nil)
	if err := authenticator.Apply(request); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if got, want := request.Header.Get(DefaultHMACTimestampHeader), "2026-01-02T03:04:05Z"; got != want {
		t.Fatalf("timestamp header = %q, want %q", got, want)
	}

	canonical := "GET\n/config.yaml?tenant=acme\n2026-01-02T03:04:05Z"
	digest := hmac.New(sha256.New, secret)
	digest.Write([]byte(canonical))
	want := "sha256=" + hex.EncodeToString(digest.Sum(nil))
	if got := request.Header.Get(DefaultHMACSignatureHeader); got != want {
		t.Fatalf("signature header = %q, want %q", got, want)
	}
}

func TestHMACIncludesNanosecondTimestamp(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, time.January, 2, 3, 4, 5, 123456789, time.UTC)
	authenticator := HMAC([]byte("secret"), WithHMACClock(func() time.Time { return fixed }))

	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := authenticator.Apply(request); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if got, want := request.Header.Get(DefaultHMACTimestampHeader), "2026-01-02T03:04:05.123456789Z"; got != want {
		t.Fatalf("timestamp header = %q, want %q", got, want)
	}
}

func TestHMACSignatureDependsOnRequest(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	secret := []byte("shared-secret")
	options := []HMACOption{WithHMACClock(func() time.Time { return fixed })}

	get := httptest.NewRequest(http.MethodGet, "http://example.com/config?tenant=acme", nil)
	if err := HMAC(secret, options...).Apply(get); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	otherTenant := httptest.NewRequest(http.MethodGet, "http://example.com/config?tenant=other", nil)
	if err := HMAC(secret, options...).Apply(otherTenant); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	otherMethod := httptest.NewRequest(http.MethodPost, "http://example.com/config?tenant=acme", nil)
	if err := HMAC(secret, options...).Apply(otherMethod); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	base := get.Header.Get(DefaultHMACSignatureHeader)
	if base == otherTenant.Header.Get(DefaultHMACSignatureHeader) {
		t.Fatal("signature does not cover the query string")
	}
	if base == otherMethod.Header.Get(DefaultHMACSignatureHeader) {
		t.Fatal("signature does not cover the method")
	}
}

func TestHMACCustomHeadersAndAlgorithm(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	secret := []byte("shared-secret")
	authenticator := HMAC(secret,
		WithHMACHeader("X-Custom-Signature"),
		WithHMACTimestampHeader("X-Custom-Timestamp"),
		WithHMACAlgorithm(sha512.New),
		WithHMACClock(func() time.Time { return fixed }),
	)

	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := authenticator.Apply(request); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if got := request.Header.Get(DefaultHMACSignatureHeader); got != "" {
		t.Fatalf("default signature header = %q, want empty", got)
	}
	timestamp := request.Header.Get("X-Custom-Timestamp")
	if timestamp != "2026-01-02T03:04:05Z" {
		t.Fatalf("custom timestamp header = %q", timestamp)
	}
	signature := request.Header.Get("X-Custom-Signature")
	if !strings.HasPrefix(signature, "sha512=") {
		t.Fatalf("custom signature = %q, want sha512 prefix", signature)
	}
	digest := hmac.New(sha512.New, secret)
	digest.Write([]byte("GET\n/config\n" + timestamp))
	if want := "sha512=" + hex.EncodeToString(digest.Sum(nil)); signature != want {
		t.Fatalf("signature = %q, want %q", signature, want)
	}
}

func TestHMACVerifiesOverHTTP(t *testing.T) {
	t.Parallel()

	secret := []byte("shared-secret")
	verified := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		timestamp := request.Header.Get(DefaultHMACTimestampHeader)
		canonical := strings.Join([]string{request.Method, request.URL.RequestURI(), timestamp}, "\n")
		digest := hmac.New(sha256.New, secret)
		digest.Write([]byte(canonical))
		expected := "sha256=" + hex.EncodeToString(digest.Sum(nil))
		if signature := request.Header.Get(DefaultHMACSignatureHeader); !hmac.Equal([]byte(signature), []byte(expected)) {
			verified <- errString("signature mismatch")
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		verified <- nil
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/config.yaml?tenant=acme", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := HMAC(secret).Apply(request); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
	if err := <-verified; err != nil {
		t.Fatalf("server verification failed: %v", err)
	}
}

func TestHMACRejectsRequestWithoutURL(t *testing.T) {
	t.Parallel()

	if err := HMAC([]byte("secret")).Apply(nil); err == nil {
		t.Fatal("Apply accepted a nil request")
	}
	request := &http.Request{Method: http.MethodGet}
	if err := HMAC([]byte("secret")).Apply(request); err == nil {
		t.Fatal("Apply accepted a request without a URL")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
