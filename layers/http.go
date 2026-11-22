package layers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/edanko/fastconf/auth"
	"github.com/edanko/fastconf/encoding"
	"github.com/edanko/fastconf/internal/doc"
	"github.com/edanko/fastconf/internal/maps"
	"github.com/edanko/fastconf/retry"
)

const (
	defaultHTTPTimeout       = 30 * time.Second
	maxHTTPDocumentBytes     = 8 << 20
	maxHTTPErrorExcerptBytes = 256
	httpCacheSuffix          = ".body"
)

// HTTP is a configuration source that fetches a document over HTTP or HTTPS.
//
// The document is decoded by the explicit encoding, the response Content-Type,
// the URL extension or, as a last resort, as JSON. It is normalized and merged
// into the incoming document, so values of this source override values of
// earlier sources.
//
// A source can keep the last successfully fetched body on disk and use it when
// the remote endpoint fails. The cache is written atomically and keyed by the
// request URL.
type HTTP struct {
	rawURL        string
	method        string
	client        *http.Client
	headers       http.Header
	authenticator auth.Authenticator
	format        encoding.Encoding
	timeout       time.Duration
	policy        retry.Policy
	cacheDir      string
	staleOnError  bool
}

// HTTPOption configures an HTTP source.
type HTTPOption func(*HTTP)

// WithHTTPClient sets the client used for requests. A nil client is ignored.
func WithHTTPClient(client *http.Client) HTTPOption {
	return func(h *HTTP) {
		if client != nil {
			h.client = client
		}
	}
}

// WithHTTPHeaders adds request headers. Calling the option several times
// accumulates headers; later values replace earlier values of the same name.
func WithHTTPHeaders(headers map[string]string) HTTPOption {
	return func(h *HTTP) {
		for name, value := range headers {
			if name == "" {
				continue
			}
			h.headers.Set(name, value)
		}
	}
}

// WithHTTPAuth sets the authenticator applied to every attempt. A nil
// authenticator is ignored.
func WithHTTPAuth(authenticator auth.Authenticator) HTTPOption {
	return func(h *HTTP) {
		if authenticator != nil {
			h.authenticator = authenticator
		}
	}
}

// WithHTTPEncoding forces a document format and disables content based
// detection.
func WithHTTPEncoding(format encoding.Encoding) HTTPOption {
	return func(h *HTTP) {
		if format != "" {
			h.format = format
		}
	}
}

// WithHTTPTimeout bounds the whole load, including retries. Zero disables the
// deadline; negative values are ignored.
func WithHTTPTimeout(timeout time.Duration) HTTPOption {
	return func(h *HTTP) {
		if timeout >= 0 {
			h.timeout = timeout
		}
	}
}

// WithHTTPRetry sets the retry policy applied to failed attempts. Every failed
// attempt is retried, including transport errors, non-2xx responses and
// undecodable bodies; use a policy with MaxAttempts set to one to disable
// retrying.
func WithHTTPRetry(policy retry.Policy) HTTPOption {
	return func(h *HTTP) {
		h.policy = policy
	}
}

// WithHTTPCacheDir enables the last-good body cache in the given directory.
// The directory is created on the first write. An empty directory disables the
// cache.
func WithHTTPCacheDir(directory string) HTTPOption {
	return func(h *HTTP) {
		h.cacheDir = strings.TrimSpace(directory)
	}
}

// WithHTTPStaleOnError makes the source fall back to the cached body when the
// remote endpoint cannot deliver a document.
func WithHTTPStaleOnError(enabled bool) HTTPOption {
	return func(h *HTTP) {
		h.staleOnError = enabled
	}
}

// WithHTTPMethod sets the request method. The default is GET. An empty method
// is ignored.
func WithHTTPMethod(method string) HTTPOption {
	return func(h *HTTP) {
		if method = strings.TrimSpace(method); method != "" {
			h.method = strings.ToUpper(method)
		}
	}
}

// NewHTTP creates an HTTP source for rawURL.
//
// The URL is validated when the source loads, so an invalid URL is reported as
// an error from Load instead of a panic here.
func NewHTTP(rawURL string, options ...HTTPOption) *HTTP {
	source := &HTTP{
		rawURL:  rawURL,
		method:  http.MethodGet,
		client:  http.DefaultClient,
		headers: make(http.Header),
		timeout: defaultHTTPTimeout,
		policy:  retry.Default(),
	}
	for _, option := range options {
		if option != nil {
			option(source)
		}
	}
	return source
}

// HTTPStatusError reports an unsuccessful HTTP response. The excerpt holds a
// bounded, single-line prefix of the response body.
type HTTPStatusError struct {
	// URL is the requested document URL.
	URL string
	// StatusCode is the HTTP status code.
	StatusCode int
	// Status is the HTTP status line, for example "500 Internal Server Error".
	Status string
	// Excerpt is a bounded single-line prefix of the response body.
	Excerpt string
}

// Error implements error.
func (e *HTTPStatusError) Error() string {
	if e.Excerpt == "" {
		return fmt.Sprintf("layers: http: %s: unexpected status %s", e.URL, e.Status)
	}
	return fmt.Sprintf("layers: http: %s: unexpected status %s: %s", e.URL, e.Status, e.Excerpt)
}

// Load implements Layer.
//
// A 304 Not Modified response leaves the incoming document unchanged, because
// the caller already holds the document version named in the conditional
// request.
func (h *HTTP) Load(ctx context.Context, values map[string]any) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := h.validate(); err != nil {
		return nil, err
	}
	document := doc.Normalize(values)

	modifiedAt, conditional := ModifiedAt(ctx)
	if h.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}

	var (
		fetched     map[string]any
		notModified bool
	)
	err := h.policy.Do(ctx, func(attemptCtx context.Context) error {
		fetched = nil
		notModified = false
		response, err := h.request(attemptCtx, modifiedAt, conditional)
		if err != nil {
			return err
		}
		if response.status == http.StatusNotModified {
			notModified = true
			return nil
		}
		format := h.formatFor(response.contentType)
		decoded, err := encoding.Decode(bytes.NewReader(response.body), format)
		if err != nil {
			return fmt.Errorf("layers: http: %s: %w", h.rawURL, err)
		}
		fetched = doc.Normalize(decoded)
		// The cache is an availability aid: a failed write must not fail an
		// otherwise successful load.
		_ = h.writeCache(response.body)
		return nil
	})
	if err != nil {
		if h.staleOnError {
			if stale, cacheErr := h.readCache(); cacheErr == nil {
				maps.Merge(document, stale)
				return document, nil
			}
		}
		return nil, err
	}
	if notModified {
		return document, nil
	}
	maps.Merge(document, fetched)
	return document, nil
}

// validate checks the configured URL.
func (h *HTTP) validate() error {
	if strings.TrimSpace(h.rawURL) == "" {
		return fmt.Errorf("layers: http: empty url")
	}
	parsed, err := url.Parse(h.rawURL)
	if err != nil {
		return fmt.Errorf("layers: http: invalid url %q: %w", h.rawURL, err)
	}
	if parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("layers: http: invalid url %q", h.rawURL)
	}
	return nil
}

// httpResponse is a successful response body together with its metadata.
type httpResponse struct {
	status      int
	body        []byte
	contentType string
}

// request performs one HTTP request. A 304 response is returned without a body.
func (h *HTTP) request(ctx context.Context, modifiedAt time.Time, conditional bool) (*httpResponse, error) {
	request, err := http.NewRequestWithContext(ctx, h.method, h.rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("layers: http: build request: %w", err)
	}
	for name, headerValues := range h.headers {
		for _, value := range headerValues {
			request.Header.Add(name, value)
		}
	}
	if conditional && !modifiedAt.IsZero() {
		request.Header.Set("If-Modified-Since", modifiedAt.UTC().Format(http.TimeFormat))
	}
	if h.authenticator != nil {
		if err := h.authenticator.Apply(request); err != nil {
			return nil, fmt.Errorf("layers: http: authenticate: %w", err)
		}
	}

	response, err := h.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("layers: http: %s: %w", h.rawURL, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusNotModified {
		return &httpResponse{status: response.StatusCode}, nil
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, &HTTPStatusError{
			URL:        h.rawURL,
			StatusCode: response.StatusCode,
			Status:     response.Status,
			Excerpt:    httpErrorExcerpt(response.Body),
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("layers: http: %s: read body: %w", h.rawURL, err)
	}
	if len(body) > maxHTTPDocumentBytes {
		return nil, fmt.Errorf("layers: http: %s: document exceeds %d bytes", h.rawURL, maxHTTPDocumentBytes)
	}
	return &httpResponse{
		status:      response.StatusCode,
		body:        body,
		contentType: response.Header.Get("Content-Type"),
	}, nil
}

// formatFor selects the document format: explicit encoding, Content-Type, URL
// extension, then JSON.
func (h *HTTP) formatFor(contentType string) encoding.Encoding {
	if h.format != "" {
		return h.format
	}
	if format, ok := httpFormatFromContentType(contentType); ok {
		return format
	}
	if format, ok := httpFormatFromURL(h.rawURL); ok {
		return format
	}
	return encoding.JSON
}

// writeCache stores body as the last good document. The write goes through a
// temporary file in the cache directory so readers never observe a partial
// document.
func (h *HTTP) writeCache(body []byte) error {
	if h.cacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(h.cacheDir, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(h.cacheDir, ".http-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() {
		if name != "" {
			_ = os.Remove(name)
		}
	}()
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, h.cachePath()); err != nil {
		return err
	}
	name = ""
	return nil
}

// readCache loads and decodes the last good document. When no explicit format
// is configured it tries the URL format first and then every supported format,
// because the cached body may differ from what the URL suggests.
func (h *HTTP) readCache() (map[string]any, error) {
	if h.cacheDir == "" {
		return nil, fmt.Errorf("layers: http: cache is not configured")
	}
	body, err := os.ReadFile(h.cachePath())
	if err != nil {
		return nil, err
	}
	var firstErr error
	for _, format := range h.cacheFormats() {
		decoded, err := encoding.Decode(bytes.NewReader(body), format)
		if err == nil {
			return doc.Normalize(decoded), nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("layers: http: cached document is empty")
	}
	return nil, firstErr
}

// cacheFormats returns candidate formats for a cached body.
func (h *HTTP) cacheFormats() []encoding.Encoding {
	if h.format != "" {
		return []encoding.Encoding{h.format}
	}
	formats := make([]encoding.Encoding, 0, 4)
	if format, ok := httpFormatFromURL(h.rawURL); ok {
		formats = append(formats, format)
	}
	for _, format := range []encoding.Encoding{encoding.JSON, encoding.YAML, encoding.TOML} {
		if !slices.Contains(formats, format) {
			formats = append(formats, format)
		}
	}
	return formats
}

// cachePath returns the cache file for the source URL.
func (h *HTTP) cachePath() string {
	sum := sha256.Sum256([]byte(h.rawURL))
	return filepath.Join(h.cacheDir, hex.EncodeToString(sum[:16])+httpCacheSuffix)
}

// httpFormatFromContentType maps a Content-Type header to a document format.
func httpFormatFromContentType(contentType string) (encoding.Encoding, bool) {
	if contentType == "" {
		return "", false
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch mediaType {
	case "application/json", "text/json", "application/x-json":
		return encoding.JSON, true
	case "application/yaml", "application/x-yaml", "text/yaml", "text/x-yaml":
		return encoding.YAML, true
	case "application/toml", "application/x-toml", "text/toml":
		return encoding.TOML, true
	}
	switch {
	case strings.HasSuffix(mediaType, "+json"):
		return encoding.JSON, true
	case strings.HasSuffix(mediaType, "+yaml"):
		return encoding.YAML, true
	case strings.HasSuffix(mediaType, "+toml"):
		return encoding.TOML, true
	}
	return "", false
}

// httpFormatFromURL maps a document URL extension to a format.
func httpFormatFromURL(rawURL string) (encoding.Encoding, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	format, err := encoding.FromFilename(parsed.Path)
	if err != nil {
		return "", false
	}
	return format, true
}

// httpErrorExcerpt returns a bounded single-line prefix of an error body.
func httpErrorExcerpt(body io.Reader) string {
	if body == nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(body, maxHTTPErrorExcerptBytes+1))
	if err != nil {
		return ""
	}
	truncated := len(data) > maxHTTPErrorExcerptBytes
	if truncated {
		data = data[:maxHTTPErrorExcerptBytes]
	}
	excerpt := strings.Join(strings.Fields(string(data)), " ")
	if truncated && excerpt != "" {
		excerpt += "..."
	}
	return excerpt
}

// httpModifiedAtKey is the context key used by WithModifiedAt.
type httpModifiedAtKey struct{}

// WithModifiedAt returns a context that carries the modification time of the
// currently loaded document. HTTP sources use it for conditional requests via
// If-Modified-Since.
//
// A nil time returns ctx unchanged.
func WithModifiedAt(ctx context.Context, modifiedAt *time.Time) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if modifiedAt == nil {
		return ctx
	}
	return context.WithValue(ctx, httpModifiedAtKey{}, *modifiedAt)
}

// ModifiedAt returns the modification time carried by ctx. The second result
// reports whether a time is present.
func ModifiedAt(ctx context.Context) (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	modifiedAt, ok := ctx.Value(httpModifiedAtKey{}).(time.Time)
	return modifiedAt, ok
}
