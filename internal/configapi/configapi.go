// Package configapi exposes loaded configuration over HTTP.
//
// The handler serves the configuration root, tenant configurations, value
// lookups and an explicit reload endpoint. It is transport-only: loading,
// merging, caching and validation belong to the configuration loader.
package configapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/edanko/fastconf"
)

// Source provides configuration data to the handler. It is implemented by
// *fastconf.Loader.
type Source interface {
	// Map returns a deep copy of the configuration root.
	Map(ctx context.Context) (map[string]any, error)
	// TenantMap returns a deep copy of one tenant configuration.
	TenantMap(ctx context.Context, tenantCode string) (map[string]any, error)
	// TenantCodes returns the sorted tenant codes.
	TenantCodes(ctx context.Context) ([]string, error)
	// Get returns the value at a dot-separated path.
	Get(ctx context.Context, path string) (any, bool, error)
	// Reload reloads the configuration.
	Reload(ctx context.Context) error
}

// Option configures a configuration API handler.
type Option func(*handler)

// WithLogger sets the request logger. Requests are logged at info level.
func WithLogger(logger *slog.Logger) Option {
	return func(h *handler) {
		if logger != nil {
			h.logger = logger
		}
	}
}

// WithToken requires every request to carry "Authorization: Bearer <token>".
// An empty token disables authentication.
func WithToken(token string) Option {
	return func(h *handler) {
		h.token = token
	}
}

// New returns an http.Handler that serves configuration from source.
//
// Endpoints:
//
//	GET  /healthz               health probe
//	GET  /v1/config             configuration root
//	GET  /v1/config/{path...}   value lookup, slash-separated path
//	GET  /v1/tenants            tenant codes
//	GET  /v1/tenants/{code}     one tenant configuration
//	POST /v1/reload             reload configuration
func New(source Source, options ...Option) http.Handler {
	h := &handler{
		source: source,
		logger: slog.New(slog.DiscardHandler),
	}
	for _, option := range options {
		option(h)
	}

	return h
}

type handler struct {
	source Source
	logger *slog.Logger
	token  string
}

func (h *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	recorder := &statusWriter{ResponseWriter: writer}

	defer func() {
		if recovered := recover(); recovered != nil {
			h.logger.ErrorContext(request.Context(), "fastconf: api panic",
				"method", request.Method, "path", request.URL.Path, "panic", recovered)
			if !recorder.written {
				h.writeError(recorder, http.StatusInternalServerError, "internal server error")
			}
		}
		h.logger.InfoContext(request.Context(), "fastconf: api request",
			"method", request.Method,
			"path", request.URL.Path,
			"status", recorder.statusCode(),
			"duration", time.Since(started),
		)
	}()

	if !h.authorized(request) {
		h.writeError(recorder, http.StatusUnauthorized, "unauthorized")
		return
	}
	h.route(recorder, request)
}

// route dispatches requests explicitly so that unknown paths, wrong methods
// and handler errors all produce the same JSON error shape.
func (h *handler) route(writer http.ResponseWriter, request *http.Request) {
	path := request.URL.Path
	switch {
	case path == "/healthz":
		if h.allowMethod(writer, request, http.MethodGet) {
			h.health(writer, request)
		}
	case path == "/v1/config":
		if h.allowMethod(writer, request, http.MethodGet) {
			h.configuration(writer, request)
		}
	case strings.HasPrefix(path, "/v1/config/"):
		if h.allowMethod(writer, request, http.MethodGet) {
			h.value(writer, request, strings.TrimPrefix(path, "/v1/config/"))
		}
	case path == "/v1/tenants":
		if h.allowMethod(writer, request, http.MethodGet) {
			h.tenants(writer, request)
		}
	case strings.HasPrefix(path, "/v1/tenants/"):
		if h.allowMethod(writer, request, http.MethodGet) {
			h.tenant(writer, request, strings.TrimPrefix(path, "/v1/tenants/"))
		}
	case path == "/v1/reload":
		if h.allowMethod(writer, request, http.MethodPost) {
			h.reload(writer, request)
		}
	default:
		h.writeError(writer, http.StatusNotFound, "not found")
	}
}

func (h *handler) allowMethod(writer http.ResponseWriter, request *http.Request, method string) bool {
	if request.Method == method {
		return true
	}
	writer.Header().Set("Allow", method)
	h.writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
	return false
}

func (h *handler) authorized(request *http.Request) bool {
	if h.token == "" {
		return true
	}
	header := request.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(h.token)) == 1
}

func (h *handler) health(writer http.ResponseWriter, _ *http.Request) {
	h.writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) configuration(writer http.ResponseWriter, request *http.Request) {
	values, err := h.source.Map(request.Context())
	if err != nil {
		h.writeError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	h.writeJSON(writer, http.StatusOK, values)
}

func (h *handler) value(writer http.ResponseWriter, request *http.Request, rawPath string) {
	configPath := strings.ReplaceAll(rawPath, "/", ".")
	if configPath == "" {
		h.writeError(writer, http.StatusBadRequest, "empty configuration path")
		return
	}
	value, found, err := h.source.Get(request.Context(), configPath)
	if err != nil {
		h.writeError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		h.writeError(writer, http.StatusNotFound, "configuration path not found")
		return
	}
	h.writeJSON(writer, http.StatusOK, map[string]any{"path": configPath, "value": value})
}

func (h *handler) tenants(writer http.ResponseWriter, request *http.Request) {
	codes, err := h.source.TenantCodes(request.Context())
	if err != nil {
		h.writeError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	h.writeJSON(writer, http.StatusOK, map[string]any{"tenants": codes})
}

func (h *handler) tenant(writer http.ResponseWriter, request *http.Request, code string) {
	values, err := h.source.TenantMap(request.Context(), code)
	if errors.Is(err, fastconf.ErrTenantNotFound) {
		h.writeError(writer, http.StatusNotFound, "tenant not found")
		return
	}
	if err != nil {
		h.writeError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	h.writeJSON(writer, http.StatusOK, values)
}

func (h *handler) reload(writer http.ResponseWriter, request *http.Request) {
	if err := h.source.Reload(request.Context()); err != nil {
		h.writeError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	h.writeJSON(writer, http.StatusOK, map[string]string{"status": "reloaded"})
}

func (h *handler) writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		h.logger.Error("fastconf: api encode response", "error", err)
	}
}

func (h *handler) writeError(writer http.ResponseWriter, status int, message string) {
	h.writeJSON(writer, status, map[string]string{"error": message})
}

// statusWriter records the status code for request logging.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.written {
		return
	}
	w.status = status
	w.written = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if !w.written {
		w.status = http.StatusOK
		w.written = true
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusWriter) statusCode() int {
	if !w.written {
		return http.StatusOK
	}
	return w.status
}
