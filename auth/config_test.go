package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFromConfigSelectsAuthenticator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     Config
		wantHeader string
		wantValue  string
		wantAuth   bool
		wantError  bool
	}{
		{
			name:       "bearer",
			config:     Config{Type: TypeBearer, Token: "abc"},
			wantHeader: "Authorization", wantValue: "Bearer abc",
		},
		{
			name:       "type is case insensitive",
			config:     Config{Type: "BEARER", Token: "abc"},
			wantHeader: "Authorization", wantValue: "Bearer abc",
		},
		{
			name:     "basic",
			config:   Config{Type: TypeBasic, Username: "alice", Password: "secret"},
			wantAuth: true,
		},
		{
			name:       "header",
			config:     Config{Type: TypeHeader, Header: "X-Key", Value: "value"},
			wantHeader: "X-Key", wantValue: "value",
		},
		{
			name:       "headers",
			config:     Config{Type: TypeHeaders, Headers: map[string]string{"X-Key": "value"}},
			wantHeader: "X-Key", wantValue: "value",
		},
		{
			name:       "hmac",
			config:     Config{Type: TypeHMAC, Secret: "shared-secret"},
			wantHeader: DefaultHMACSignatureHeader,
		},
		{
			name:       "hmac custom headers",
			config:     Config{Type: TypeHMAC, Secret: "shared-secret", SignatureHeader: "X-Custom", TimestampHeader: "X-Time"},
			wantHeader: "X-Custom",
		},
		{
			name:   "none",
			config: Config{Type: TypeNone},
		},
		{
			name:   "empty type",
			config: Config{},
		},
		{
			name:      "unknown type",
			config:    Config{Type: "kerberos"},
			wantError: true,
		},
		{
			name:      "bearer without token",
			config:    Config{Type: TypeBearer},
			wantError: true,
		},
		{
			name:      "basic without username",
			config:    Config{Type: TypeBasic, Password: "secret"},
			wantError: true,
		},
		{
			name:      "header without name",
			config:    Config{Type: TypeHeader, Value: "value"},
			wantError: true,
		},
		{
			name:      "headers without entries",
			config:    Config{Type: TypeHeaders},
			wantError: true,
		},
		{
			name:      "hmac without secret",
			config:    Config{Type: TypeHMAC},
			wantError: true,
		},
		{
			name:      "oauth2 without endpoint",
			config:    Config{Type: TypeOAuth2},
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			authenticator, err := FromConfig(test.config)
			if test.wantError {
				if err == nil {
					t.Fatal("FromConfig accepted an invalid config")
				}
				return
			}
			if err != nil {
				t.Fatalf("FromConfig: %v", err)
			}
			if authenticator == nil {
				t.Fatal("FromConfig returned a nil authenticator")
			}
			request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
			if err := authenticator.Apply(request); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if test.wantAuth {
				if _, _, ok := request.BasicAuth(); !ok {
					t.Fatal("request has no basic credentials")
				}
				return
			}
			if test.wantHeader == "" {
				if len(request.Header) != 0 {
					t.Fatalf("headers = %v, want none", request.Header)
				}
				return
			}
			if got := request.Header.Get(test.wantHeader); test.wantValue != "" && got != test.wantValue {
				t.Fatalf("%s = %q, want %q", test.wantHeader, got, test.wantValue)
			} else if got == "" {
				t.Fatalf("%s header is empty", test.wantHeader)
			}
		})
	}
}

func TestFromConfigOAuth2(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(`{"access_token":"abc","token_type":"Bearer","expires_in":60}`))
	}))
	defer server.Close()

	authenticator, err := FromConfig(Config{
		Type: TypeOAuth2,
		OAuth2: OAuth2Config{
			TokenURL:     server.URL,
			ClientID:     "client-id",
			ClientSecret: "client-secret",
		},
	})
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := authenticator.Apply(request); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer abc" {
		t.Fatalf("Authorization = %q, want Bearer abc", got)
	}
}

func TestFromConfigHMACSignatureIsVerifiable(t *testing.T) {
	t.Parallel()

	authenticator, err := FromConfig(Config{Type: TypeHMAC, Secret: "shared-secret"})
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.com/config", nil)
	if err := authenticator.Apply(request); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if timestamp := request.Header.Get(DefaultHMACTimestampHeader); timestamp == "" {
		t.Fatal("timestamp header is empty")
	}
	if signature := request.Header.Get(DefaultHMACSignatureHeader); !strings.HasPrefix(signature, "sha256=") {
		t.Fatalf("signature = %q, want a sha256 digest", signature)
	}
}
