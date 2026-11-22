package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOAuth2ClientCredentialsCachesToken(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if err := request.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
			return
		}
		if got := request.Form.Get("grant_type"); got != "client_credentials" {
			t.Errorf("grant_type = %q, want client_credentials", got)
		}
		if got := request.Form.Get("scope"); got != "read write" {
			t.Errorf("scope = %q, want %q", got, "read write")
		}
		username, password, ok := request.BasicAuth()
		if !ok || username != "client-id" || password != "client-secret" {
			t.Errorf("basic credentials = %q/%q (present %v)", username, password, ok)
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"access_token":"token-1","token_type":"bearer","expires_in":3600}`)
	}))
	defer server.Close()

	authenticator, err := OAuth2ClientCredentials(OAuth2Config{
		TokenURL:     server.URL,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Scopes:       []string{"read", "write"},
	})
	if err != nil {
		t.Fatalf("OAuth2ClientCredentials: %v", err)
	}

	for range 3 {
		request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
		if err := authenticator.Apply(request); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if got, want := request.Header.Get("Authorization"), "Bearer token-1"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1 cached request", got)
	}
}

func TestOAuth2RefreshesBeforeExpiry(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		issued := requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		if issued == 1 {
			io.WriteString(writer, `{"access_token":"token-1","token_type":"Bearer","expires_in":100}`)
			return
		}
		io.WriteString(writer, `{"access_token":"token-2","token_type":"Bearer","expires_in":100}`)
	}))
	defer server.Close()

	authenticator, err := OAuth2ClientCredentials(OAuth2Config{
		TokenURL:     server.URL,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	})
	if err != nil {
		t.Fatalf("OAuth2ClientCredentials: %v", err)
	}
	tokenSource, ok := authenticator.(*oauth2Authenticator)
	if !ok {
		t.Fatalf("authenticator type = %T", authenticator)
	}
	clock := time.Unix(1_000, 0)
	tokenSource.now = func() time.Time { return clock }

	if token := applyToken(t, authenticator); token != "Bearer token-1" {
		t.Fatalf("initial token = %q", token)
	}
	// The token is valid for 100 seconds and is refreshed ten seconds before
	// expiry, so 89 seconds later it is still cached.
	clock = clock.Add(89 * time.Second)
	if token := applyToken(t, authenticator); token != "Bearer token-1" {
		t.Fatalf("token inside the cache window = %q", token)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}

	// One more second crosses the refresh threshold.
	clock = clock.Add(2 * time.Second)
	if token := applyToken(t, authenticator); token != "Bearer token-2" {
		t.Fatalf("token after the refresh threshold = %q", token)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("token requests = %d, want 2", got)
	}
}

func TestOAuth2CachesTokenWithoutExpiry(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"access_token":"forever","token_type":"Bearer"}`)
	}))
	defer server.Close()

	authenticator, err := OAuth2ClientCredentials(OAuth2Config{
		TokenURL:     server.URL,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	})
	if err != nil {
		t.Fatalf("OAuth2ClientCredentials: %v", err)
	}
	if token := applyToken(t, authenticator); token != "Bearer forever" {
		t.Fatalf("token = %q", token)
	}
	if token := applyToken(t, authenticator); token != "Bearer forever" {
		t.Fatalf("cached token = %q", token)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}
}

func TestOAuth2SingleFlight(t *testing.T) {
	t.Parallel()

	const workers = 12
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		startOnce.Do(func() { close(started) })
		<-release
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"access_token":"shared","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()

	authenticator, err := OAuth2ClientCredentials(OAuth2Config{
		TokenURL:     server.URL,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	})
	if err != nil {
		t.Fatalf("OAuth2ClientCredentials: %v", err)
	}

	var waitGroup sync.WaitGroup
	tokens := make(chan string, workers)
	errors := make(chan error, workers)
	for range workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
			if err := authenticator.Apply(request); err != nil {
				errors <- err
				return
			}
			tokens <- request.Header.Get("Authorization")
		}()
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("token endpoint was never called")
	}
	// Give the remaining callers time to join the in-flight token request.
	time.Sleep(50 * time.Millisecond)
	close(release)
	waitGroup.Wait()
	close(tokens)
	close(errors)

	for err := range errors {
		t.Fatalf("Apply returned error: %v", err)
	}
	count := 0
	for token := range tokens {
		count++
		if token != "Bearer shared" {
			t.Fatalf("Authorization = %q, want Bearer shared", token)
		}
	}
	if count != workers {
		t.Fatalf("successful callers = %d, want %d", count, workers)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("token requests = %d, want a single in-flight request", got)
	}
}

func TestOAuth2FailedTokenIsNotCached(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			writer.WriteHeader(http.StatusUnauthorized)
			io.WriteString(writer, `{"error":"invalid_client"}`)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"access_token":"recovered","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()

	authenticator, err := OAuth2ClientCredentials(OAuth2Config{
		TokenURL:     server.URL,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	})
	if err != nil {
		t.Fatalf("OAuth2ClientCredentials: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := authenticator.Apply(request); err == nil {
		t.Fatal("Apply succeeded although the token endpoint failed")
	}
	if token := applyToken(t, authenticator); token != "Bearer recovered" {
		t.Fatalf("token after a failed attempt = %q", token)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("token requests = %d, want 2", got)
	}
}

func TestOAuth2ErrorPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		body     string
		contains string
	}{
		{
			name:     "unauthorized",
			status:   http.StatusUnauthorized,
			body:     `{"error":"invalid_client"}`,
			contains: "401 Unauthorized",
		},
		{
			name:     "server error without body",
			status:   http.StatusInternalServerError,
			contains: "500 Internal Server Error",
		},
		{
			name:     "malformed json",
			status:   http.StatusOK,
			body:     "this is not json",
			contains: "decode token response",
		},
		{
			name:     "missing access token",
			status:   http.StatusOK,
			body:     `{"token_type":"Bearer"}`,
			contains: "no access token",
		},
		{
			name:     "oauth error document",
			status:   http.StatusOK,
			body:     `{"error":"invalid_scope","error_description":"unknown scope"}`,
			contains: "unknown scope",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(test.status)
				if test.body != "" {
					io.WriteString(writer, test.body)
				}
			}))
			defer server.Close()

			config := OAuth2Config{
				TokenURL:     server.URL,
				ClientID:     "client-id",
				ClientSecret: "client-secret",
			}
			authenticator, err := OAuth2ClientCredentials(config)
			if err != nil {
				t.Fatalf("OAuth2ClientCredentials: %v", err)
			}
			request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
			err = authenticator.Apply(request)
			if err == nil {
				t.Fatal("Apply succeeded, want an error")
			}
			if !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %q, want it to contain %q", err, test.contains)
			}
		})
	}
}

func TestOAuth2NetworkError(t *testing.T) {
	t.Parallel()

	authenticator, err := OAuth2ClientCredentials(OAuth2Config{
		TokenURL:     "http://127.0.0.1:1/token",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		HTTPClient:   &http.Client{Timeout: 2 * time.Second},
	})
	if err != nil {
		t.Fatalf("OAuth2ClientCredentials: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := authenticator.Apply(request); err == nil {
		t.Fatal("Apply succeeded against a closed endpoint")
	} else if !strings.Contains(err.Error(), "token request") {
		t.Fatalf("error = %q, want a token request error", err)
	}
}

func TestOAuth2ConfigValidation(t *testing.T) {
	t.Parallel()

	tests := map[string]OAuth2Config{
		"empty":          {},
		"missing id":     {TokenURL: "http://example.com/token", ClientSecret: "secret"},
		"missing secret": {TokenURL: "http://example.com/token", ClientID: "id"},
		"invalid scheme": {TokenURL: "ftp://example.com/token", ClientID: "id", ClientSecret: "secret"},
		"missing scheme": {TokenURL: "example.com/token", ClientID: "id", ClientSecret: "secret"},
		"relative url":   {TokenURL: "/token", ClientID: "id", ClientSecret: "secret"},
		"broken url":     {TokenURL: "http://[::1]:namedport/", ClientID: "id", ClientSecret: "secret"},
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := OAuth2ClientCredentials(config); err == nil {
				t.Fatalf("OAuth2ClientCredentials(%+v) accepted an invalid config", config)
			}
		})
	}
}

func TestOAuth2NormalisesTokenType(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"access_token":"abc","token_type":"bearer","expires_in":60}`)
	}))
	defer server.Close()

	authenticator, err := OAuth2ClientCredentials(OAuth2Config{
		TokenURL:     server.URL,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	})
	if err != nil {
		t.Fatalf("OAuth2ClientCredentials: %v", err)
	}
	if token := applyToken(t, authenticator); token != "Bearer abc" {
		t.Fatalf("Authorization = %q, want Bearer abc", token)
	}
}

func TestOAuth2UsesRequestContext(t *testing.T) {
	t.Parallel()

	transport := &contextBlockingTransport{started: make(chan struct{})}
	authenticator, err := OAuth2ClientCredentials(OAuth2Config{
		TokenURL:     "http://example.com/token",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		HTTPClient:   &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("OAuth2ClientCredentials: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil).WithContext(ctx)
	go func() {
		select {
		case <-transport.started:
		case <-time.After(2 * time.Second):
		}
		cancel()
	}()
	err = authenticator.Apply(request)
	if err == nil {
		t.Fatal("Apply succeeded after the request context was cancelled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply error = %v, want context.Canceled", err)
	}
}

// contextBlockingTransport blocks a round trip until the request context is
// cancelled, emulating a token endpoint that never answers.
type contextBlockingTransport struct {
	started chan struct{}
	once    sync.Once
}

func (t *contextBlockingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.once.Do(func() { close(t.started) })
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestOAuth2RefreshSkew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		lifetime time.Duration
		want     time.Duration
	}{
		{lifetime: 100 * time.Second, want: 10 * time.Second},
		{lifetime: 10 * time.Minute, want: 30 * time.Second},
		{lifetime: 2 * time.Second, want: time.Second},
		{lifetime: 100 * time.Millisecond, want: 50 * time.Millisecond},
	}
	for _, test := range tests {
		if got := oauth2RefreshSkew(test.lifetime); got != test.want {
			t.Fatalf("oauth2RefreshSkew(%v) = %v, want %v", test.lifetime, got, test.want)
		}
	}
}

func TestCanonicalTokenType(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":       "Bearer",
		"bearer": "Bearer",
		"Bearer": "Bearer",
		"mac":    "Mac",
		"MAC":    "MAC",
	}
	for input, want := range tests {
		if got := canonicalTokenType(input); got != want {
			t.Fatalf("canonicalTokenType(%q) = %q, want %q", input, got, want)
		}
	}
}

// applyToken applies the authenticator to a fresh request and returns the
// Authorization header.
func applyToken(t *testing.T, authenticator Authenticator) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := authenticator.Apply(request); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return request.Header.Get("Authorization")
}
