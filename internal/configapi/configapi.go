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
//	PATCH /v1/config            set several runtime overrides ({"values": {...}})
//	PUT  /v1/config/{path...}   set a runtime override
//	DELETE /v1/config/{path...} delete a runtime override
//	GET  /v1/tenants            tenant codes
//	GET  /v1/tenants/{code}     one tenant configuration
//	PATCH /v1/tenants/{code}/config             set several tenant overrides
//	PUT  /v1/tenants/{code}/config/{path...}    set a tenant override
//	DELETE /v1/tenants/{code}/config/{path...} delete a tenant override
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
		switch request.Method {
		case http.MethodGet:
			h.configuration(writer, request)
		case http.MethodPatch:
			h.setOverridesBulk(writer, request, "")
		default:
			h.methodNotAllowed(writer, http.MethodGet, http.MethodPatch)
		}
	case strings.HasPrefix(path, "/v1/config/"):
		rawPath := strings.TrimPrefix(path, "/v1/config/")
		switch request.Method {
		case http.MethodGet:
			h.value(writer, request, rawPath)
		case http.MethodPut:
			h.setOverride(writer, request, "", rawPath)
		case http.MethodDelete:
			h.deleteOverride(writer, request, "", rawPath)
		default:
			h.methodNotAllowed(writer, http.MethodGet, http.MethodPut, http.MethodDelete)
		}
	case path == "/v1/tenants":
		if h.allowMethod(writer, request, http.MethodGet) {
			h.tenants(writer, request)
		}
	case strings.HasPrefix(path, "/v1/tenants/"):
		remainder := strings.TrimPrefix(path, "/v1/tenants/")
		if code, ok := strings.CutSuffix(remainder, "/config"); ok && code != "" {
			if request.Method != http.MethodPatch {
				h.methodNotAllowed(writer, http.MethodPatch)
				return
			}
			h.setOverridesBulk(writer, request, code)
			return
		}
		if code, rawPath, ok := cutTenantOverride(remainder); ok {
			switch request.Method {
			case http.MethodPut:
				h.setOverride(writer, request, code, rawPath)
			case http.MethodDelete:
				h.deleteOverride(writer, request, code, rawPath)
			default:
				h.methodNotAllowed(writer, http.MethodPut, http.MethodDelete)
			}
			return
		}
		if h.allowMethod(writer, request, http.MethodGet) {
			h.tenant(writer, request, remainder)
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

// viewSource is implemented by sources that can serve a pinned read-only view,
// such as *fastconf.Loader. When available it avoids deep-copying the
// configuration before serializing it.
type viewSource interface {
	View(ctx context.Context) (*fastconf.View, error)
}

// batchOverrideSource is implemented by sources that can apply several
// overrides in one snapshot rebuild.
type batchOverrideSource interface {
	SetOverrides(values map[string]any) error
	SetTenantOverrides(tenantCode string, values map[string]any) error
}

// overrideSource is implemented by sources that support runtime overrides,
// such as *fastconf.Loader. Mutation endpoints return 501 for other sources.
type overrideSource interface {
	SetOverride(configPath string, value any) error
	SetTenantOverride(tenantCode, configPath string, value any) error
	DeleteOverride(configPath string) (bool, error)
	DeleteTenantOverride(tenantCode, configPath string) (bool, error)
}

func (h *handler) configuration(writer http.ResponseWriter, request *http.Request) {
	if view, ok, err := h.view(request.Context()); ok {
		if err != nil {
			h.writeError(writer, http.StatusInternalServerError, err.Error())
			return
		}
		h.writeJSONView(writer, http.StatusOK, view)
		return
	}
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
	if view, ok, err := h.view(request.Context()); ok {
		if err != nil {
			h.writeError(writer, http.StatusInternalServerError, err.Error())
			return
		}
		tenantView, found := view.Tenant(code)
		if !found {
			h.writeError(writer, http.StatusNotFound, "tenant not found")
			return
		}
		h.writeJSONView(writer, http.StatusOK, tenantView)
		return
	}
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

func (h *handler) view(ctx context.Context) (*fastconf.View, bool, error) {
	source, ok := h.source.(viewSource)
	if !ok {
		return nil, false, nil
	}
	view, err := source.View(ctx)
	return view, true, err
}

func (h *handler) writeJSONView(writer http.ResponseWriter, status int, view *fastconf.View) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := view.WriteJSON(writer); err != nil {
		h.logger.Error("fastconf: api encode response", "error", err)
	}
}

func cutTenantOverride(remainder string) (string, string, bool) {
	code, rawPath, ok := strings.Cut(remainder, "/config/")
	if !ok || code == "" || rawPath == "" {
		return "", "", false
	}
	return code, rawPath, true
}

func (h *handler) setOverride(writer http.ResponseWriter, request *http.Request, tenantCode, rawPath string) {
	if !h.requireWriteToken(writer) {
		return
	}
	source, ok := h.source.(overrideSource)
	if !ok {
		h.writeError(writer, http.StatusNotImplemented, "runtime overrides are not supported by this configuration source")
		return
	}
	configPath := strings.ReplaceAll(rawPath, "/", ".")
	if configPath == "" {
		h.writeError(writer, http.StatusBadRequest, "empty configuration path")
		return
	}
	value, ok := h.decodeOverrideValue(writer, request)
	if !ok {
		return
	}
	var err error
	if tenantCode == "" {
		err = source.SetOverride(configPath, value)
	} else {
		err = source.SetTenantOverride(tenantCode, configPath, value)
	}
	if err != nil {
		h.writeOverrideError(writer, err)
		return
	}
	response := map[string]any{"path": configPath, "value": value}
	if tenantCode != "" {
		response["tenant"] = tenantCode
	}
	h.writeJSON(writer, http.StatusOK, response)
}

func (h *handler) setOverridesBulk(writer http.ResponseWriter, request *http.Request, tenantCode string) {
	if !h.requireWriteToken(writer) {
		return
	}
	source, ok := h.source.(batchOverrideSource)
	if !ok {
		h.writeError(writer, http.StatusNotImplemented, "batch runtime overrides are not supported by this configuration source")
		return
	}
	values, ok := h.decodeOverrideBatch(writer, request)
	if !ok {
		return
	}
	var err error
	if tenantCode == "" {
		err = source.SetOverrides(values)
	} else {
		err = source.SetTenantOverrides(tenantCode, values)
	}
	if err != nil {
		h.writeOverrideError(writer, err)
		return
	}
	response := map[string]any{"status": "updated", "count": len(values)}
	if tenantCode != "" {
		response["tenant"] = tenantCode
	}
	h.writeJSON(writer, http.StatusOK, response)
}

func (h *handler) decodeOverrideBatch(writer http.ResponseWriter, request *http.Request) (map[string]any, bool) {
	request.Body = http.MaxBytesReader(writer, request.Body, 1<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var payload struct {
		Values map[string]any `json:"values"`
	}
	if err := decoder.Decode(&payload); err != nil {
		h.writeError(writer, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return nil, false
	}
	if payload.Values == nil {
		h.writeError(writer, http.StatusBadRequest, `body must contain a "values" object`)
		return nil, false
	}
	return payload.Values, true
}

func (h *handler) deleteOverride(writer http.ResponseWriter, _ *http.Request, tenantCode, rawPath string) {
	if !h.requireWriteToken(writer) {
		return
	}
	source, ok := h.source.(overrideSource)
	if !ok {
		h.writeError(writer, http.StatusNotImplemented, "runtime overrides are not supported by this configuration source")
		return
	}
	configPath := strings.ReplaceAll(rawPath, "/", ".")
	if configPath == "" {
		h.writeError(writer, http.StatusBadRequest, "empty configuration path")
		return
	}
	var removed bool
	var err error
	if tenantCode == "" {
		removed, err = source.DeleteOverride(configPath)
	} else {
		removed, err = source.DeleteTenantOverride(tenantCode, configPath)
	}
	if err != nil {
		h.writeOverrideError(writer, err)
		return
	}
	if !removed {
		h.writeError(writer, http.StatusNotFound, "override not found")
		return
	}
	response := map[string]any{"status": "deleted", "path": configPath}
	if tenantCode != "" {
		response["tenant"] = tenantCode
	}
	h.writeJSON(writer, http.StatusOK, response)
}

func (h *handler) decodeOverrideValue(writer http.ResponseWriter, request *http.Request) (any, bool) {
	request.Body = http.MaxBytesReader(writer, request.Body, 1<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var payload struct {
		Value json.RawMessage `json:"value"`
	}
	if err := decoder.Decode(&payload); err != nil {
		h.writeError(writer, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return nil, false
	}
	if len(payload.Value) == 0 {
		h.writeError(writer, http.StatusBadRequest, `body must contain a "value" field`)
		return nil, false
	}
	var value any
	if err := json.Unmarshal(payload.Value, &value); err != nil {
		h.writeError(writer, http.StatusBadRequest, `invalid "value" field: `+err.Error())
		return nil, false
	}
	return value, true
}

func (h *handler) requireWriteToken(writer http.ResponseWriter) bool {
	if h.token != "" {
		return true
	}
	h.writeError(writer, http.StatusForbidden, "configuration mutations require an authentication token")
	return false
}

func (h *handler) writeOverrideError(writer http.ResponseWriter, err error) {
	if errors.Is(err, fastconf.ErrClosed) {
		h.writeError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	h.writeError(writer, http.StatusBadRequest, err.Error())
}

func (h *handler) methodNotAllowed(writer http.ResponseWriter, methods ...string) {
	writer.Header().Set("Allow", strings.Join(methods, ", "))
	h.writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
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
