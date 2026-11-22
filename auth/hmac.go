package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"strings"
	"time"
)

// Default header names used by the HMAC authenticator.
const (
	// DefaultHMACSignatureHeader carries the request signature.
	DefaultHMACSignatureHeader = "X-Signature"
	// DefaultHMACTimestampHeader carries the RFC 3339 timestamp that the
	// signature covers.
	DefaultHMACTimestampHeader = "X-Timestamp"
)

// HMACOption configures the HMAC authenticator.
type HMACOption func(*hmacAuthenticator)

// WithHMACHeader overrides the header that receives the signature. Empty names
// are ignored.
func WithHMACHeader(name string) HMACOption {
	return func(a *hmacAuthenticator) {
		if name != "" {
			a.signatureHeader = name
		}
	}
}

// WithHMACTimestampHeader overrides the header that receives the timestamp.
// Empty names are ignored.
func WithHMACTimestampHeader(name string) HMACOption {
	return func(a *hmacAuthenticator) {
		if name != "" {
			a.timestampHeader = name
		}
	}
}

// WithHMACAlgorithm replaces the hash algorithm. It must return a fresh hash
// for every call, like sha256.New. The algorithm name is derived from the hash
// output size and is prefixed to the signature value. A nil function keeps the
// default SHA-256 algorithm.
func WithHMACAlgorithm(algorithm func() hash.Hash) HMACOption {
	return func(a *hmacAuthenticator) {
		if algorithm != nil {
			a.algorithm = algorithm
			a.algorithmName = hmacAlgorithmName(algorithm())
		}
	}
}

// WithHMACClock replaces the time source used for the timestamp. It exists for
// deterministic tests; a nil function keeps time.Now.
func WithHMACClock(clock func() time.Time) HMACOption {
	return func(a *hmacAuthenticator) {
		if clock != nil {
			a.clock = clock
		}
	}
}

// HMAC signs every request with a shared secret.
//
// The signature covers three newline-separated fields:
//
//	HTTP method
//	request URI (path and query, as sent on the request line)
//	RFC 3339 timestamp, UTC with nanosecond precision
//
// For a GET request to /config.yaml?tenant=acme at 2026-01-02T03:04:05Z the
// signed text is:
//
//	GET\n/config.yaml?tenant=acme\n2026-01-02T03:04:05Z
//
// Apply writes the timestamp to the timestamp header (default X-Timestamp) and
// "<algorithm>=<hex digest>" to the signature header (default X-Signature),
// for example:
//
//	X-Timestamp: 2026-01-02T03:04:05Z
//	X-Signature: sha256=3b1f...9ac
//
// The verifier must recompute the digest from the received method, request URI
// and timestamp header, compare it in constant time, and reject timestamps
// outside its clock-skew window. Request bodies are not signed, so the scheme
// fits document fetches that have no body.
func HMAC(secret []byte, options ...HMACOption) Authenticator {
	authenticator := &hmacAuthenticator{
		secret:          append([]byte(nil), secret...),
		signatureHeader: DefaultHMACSignatureHeader,
		timestampHeader: DefaultHMACTimestampHeader,
		algorithm:       sha256.New,
		algorithmName:   "sha256",
		clock:           time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(authenticator)
		}
	}
	if authenticator.algorithm == nil {
		authenticator.algorithm = sha256.New
		authenticator.algorithmName = "sha256"
	}
	if authenticator.clock == nil {
		authenticator.clock = time.Now
	}
	return authenticator
}

type hmacAuthenticator struct {
	secret          []byte
	signatureHeader string
	timestampHeader string
	algorithm       func() hash.Hash
	algorithmName   string
	clock           func() time.Time
}

// Apply implements Authenticator.
func (a *hmacAuthenticator) Apply(request *http.Request) error {
	if request == nil {
		return fmt.Errorf("auth: hmac: nil request")
	}
	if request.URL == nil {
		return fmt.Errorf("auth: hmac: request has no url")
	}
	timestamp := a.clock().UTC().Format(time.RFC3339Nano)
	canonical := strings.Join([]string{request.Method, request.URL.RequestURI(), timestamp}, "\n")

	digest := hmac.New(a.algorithm, a.secret)
	digest.Write([]byte(canonical))
	signature := a.algorithmName + "=" + hex.EncodeToString(digest.Sum(nil))

	request.Header.Set(a.timestampHeader, timestamp)
	request.Header.Set(a.signatureHeader, signature)
	return nil
}

// hmacAlgorithmName maps a hash output size to the conventional algorithm
// name. Unknown sizes fall back to "hmac".
func hmacAlgorithmName(digest hash.Hash) string {
	if digest == nil {
		return "sha256"
	}
	switch digest.Size() {
	case 20:
		return "sha1"
	case 32:
		return "sha256"
	case 48:
		return "sha384"
	case 64:
		return "sha512"
	default:
		return "hmac"
	}
}
