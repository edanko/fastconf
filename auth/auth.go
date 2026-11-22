// Package auth provides HTTP request authenticators for remote configuration
// sources.
//
// Every authenticator implements Authenticator: it mutates an outgoing
// *http.Request, usually by adding headers, and reports configuration errors
// (for example an empty token) through Apply.
//
// The package depends only on the standard library. OAuth2 client-credentials
// tokens are acquired directly from the configured token endpoint; responses
// are cached and refreshed before they expire.
package auth

import (
	"fmt"
	"maps"
	"net/http"
)

// Authenticator prepares an outgoing HTTP request.
//
// Apply must be safe to call for every request of a retry loop and is allowed
// to perform network calls, which is how OAuth2 tokens are acquired. Returning
// an error aborts the request that is being prepared.
type Authenticator interface {
	Apply(request *http.Request) error
}

// AuthenticatorFunc adapts a function into an Authenticator.
type AuthenticatorFunc func(request *http.Request) error

// Apply implements Authenticator.
func (f AuthenticatorFunc) Apply(request *http.Request) error {
	if f == nil {
		return nil
	}
	return f(request)
}

// Bearer authenticates requests with an OAuth2-style bearer token in the
// Authorization header.
func Bearer(token string) Authenticator {
	return bearerAuthenticator{token: token}
}

type bearerAuthenticator struct {
	token string
}

func (b bearerAuthenticator) Apply(request *http.Request) error {
	if b.token == "" {
		return fmt.Errorf("auth: bearer: empty token")
	}
	request.Header.Set("Authorization", "Bearer "+b.token)
	return nil
}

// Basic authenticates requests with HTTP basic authentication.
func Basic(username, password string) Authenticator {
	return basicAuthenticator{username: username, password: password}
}

type basicAuthenticator struct {
	username string
	password string
}

func (b basicAuthenticator) Apply(request *http.Request) error {
	request.SetBasicAuth(b.username, b.password)
	return nil
}

// Header authenticates requests by setting a single header.
func Header(name, value string) Authenticator {
	return headerAuthenticator{name: name, value: value}
}

type headerAuthenticator struct {
	name  string
	value string
}

func (h headerAuthenticator) Apply(request *http.Request) error {
	if h.name == "" {
		return fmt.Errorf("auth: header: empty header name")
	}
	request.Header.Set(h.name, h.value)
	return nil
}

// Headers authenticates requests by setting several headers. The map is
// copied, so later changes to the caller's map do not affect the
// authenticator.
func Headers(headers map[string]string) Authenticator {
	copied := make(map[string]string, len(headers))
	maps.Copy(copied, headers)
	return headersAuthenticator{headers: copied}
}

type headersAuthenticator struct {
	headers map[string]string
}

func (h headersAuthenticator) Apply(request *http.Request) error {
	for name, value := range h.headers {
		if name == "" {
			return fmt.Errorf("auth: headers: empty header name")
		}
		request.Header.Set(name, value)
	}
	return nil
}

// Noop returns an authenticator that leaves requests unchanged. It is used by
// FromConfig when no authentication type is configured.
func Noop() Authenticator { return noopAuthenticator{} }

type noopAuthenticator struct{}

func (noopAuthenticator) Apply(*http.Request) error { return nil }
