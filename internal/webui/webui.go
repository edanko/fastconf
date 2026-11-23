// Package webui serves a small HTMX console for a running configuration
// server: tenant navigation, a readable configuration tree, runtime overrides
// and reload.
package webui

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/edanko/fastconf"
)

//go:embed templates/*.html assets/*
var embedded embed.FS

const rowLimit = 260

// Option configures the console handler.
type Option func(*Handler)

// WithToken protects the console with HTTP Basic authentication. The user name
// is "ui" and the password is the token.
func WithToken(token string) Option {
	return func(h *Handler) { h.token = token }
}

// WithLogger sets the request logger.
func WithLogger(logger *slog.Logger) Option {
	return func(h *Handler) {
		if logger != nil {
			h.logger = logger
		}
	}
}

// Handler serves the configuration console.
type Handler struct {
	loader    *fastconf.Loader
	logger    *slog.Logger
	token     string
	templates *template.Template
	assets    http.Handler
}

// New returns a console handler for loader.
func New(loader *fastconf.Loader, options ...Option) (*Handler, error) {
	if loader == nil {
		return nil, errors.New("webui: nil loader")
	}
	handler := &Handler{
		loader: loader,
		logger: slog.New(slog.DiscardHandler),
	}
	for _, option := range options {
		option(handler)
	}
	templates, err := template.New("webui").Funcs(template.FuncMap{
		"json": func(value any) string {
			encoded, err := json.Marshal(value)
			if err != nil {
				return fmt.Sprint(value)
			}
			return string(encoded)
		},
	}).ParseFS(embedded, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("webui: parse templates: %w", err)
	}
	handler.templates = templates
	assets, err := fs.Sub(embedded, "assets")
	if err != nil {
		return nil, fmt.Errorf("webui: assets: %w", err)
	}
	handler.assets = http.FileServer(http.FS(assets))
	return handler, nil
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !h.authorized(writer, request) {
		return
	}
	header := writer.Header()
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "same-origin")
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	if !strings.HasPrefix(request.URL.Path, "/ui/static/") {
		header.Set("Cache-Control", "no-store")
	}
	// Mutations are only reachable through htmx. The custom header forces a
	// CORS preflight from other origins, which blocks cross-site form posts.
	if request.Method != http.MethodGet && request.Method != http.MethodHead &&
		request.Header.Get("HX-Request") != "true" {
		http.Error(writer, "htmx request required", http.StatusForbidden)
		return
	}
	path := request.URL.Path
	switch {
	case path == "/ui" || path == "/ui/":
		h.page(writer, request)
	case strings.HasPrefix(path, "/ui/static/"):
		writer.Header().Set("Cache-Control", "public, max-age=3600")
		http.StripPrefix("/ui/static/", h.assets).ServeHTTP(writer, request)
	case path == "/ui/scope":
		h.scope(writer, request)
	case path == "/ui/overrides":
		h.overrides(writer, request)
	case path == "/ui/reload":
		h.reload(writer, request)
	default:
		h.logger.WarnContext(request.Context(), "fastconf: console unknown path", "path", path)
		http.NotFound(writer, request)
	}
}

func (h *Handler) authorized(writer http.ResponseWriter, request *http.Request) bool {
	if h.token == "" {
		return true
	}
	user, password, ok := request.BasicAuth()
	if ok && user == "ui" && subtle.ConstantTimeCompare([]byte(password), []byte(h.token)) == 1 {
		return true
	}
	writer.Header().Set("WWW-Authenticate", `Basic realm="fastconf console", charset="UTF-8"`)
	http.Error(writer, "unauthorized", http.StatusUnauthorized)
	return false
}

func (h *Handler) page(writer http.ResponseWriter, request *http.Request) {
	data, err := h.pageData(request.Context(), request.URL.Query().Get("tenant"), "")
	if err != nil {
		h.renderError(writer, request, err)
		return
	}
	h.render(writer, request, "page", data)
}

func (h *Handler) scope(writer http.ResponseWriter, request *http.Request) {
	tenant := strings.TrimSpace(request.URL.Query().Get("tenant"))
	// The scope endpoint returns an HTML fragment for htmx to swap. A direct
	// navigation to it (new tab, copied URL, no JavaScript) must reach the full
	// console instead of rendering a bare fragment.
	if request.Header.Get("HX-Request") != "true" {
		target := "/ui/"
		if tenant != "" {
			target += "?tenant=" + url.QueryEscape(tenant)
		}
		http.Redirect(writer, request, target, http.StatusSeeOther)
		return
	}
	data, err := h.pageData(request.Context(), tenant, strings.TrimSpace(request.URL.Query().Get("q")))
	if err != nil {
		h.renderError(writer, request, err)
		return
	}
	data.Swap = true
	h.render(writer, request, "scope", data)
}

func (h *Handler) overrides(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodPost:
		h.applyOverride(writer, request)
	case http.MethodDelete:
		h.deleteOverride(writer, request)
	default:
		writer.Header().Set("Allow", "POST, DELETE")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) applyOverride(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		h.renderToast(writer, request, toastData{Kind: "error", Message: "Could not read the form."})
		return
	}
	tenant := strings.TrimSpace(request.FormValue("tenant"))
	configPath := strings.TrimSpace(request.FormValue("path"))
	raw := strings.TrimSpace(request.FormValue("value"))
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		h.renderScopeWithToast(writer, request, tenant, toastData{Kind: "error", Message: "Invalid JSON value: " + err.Error()})
		return
	}
	var err error
	if tenant == "" {
		err = h.loader.SetOverride(configPath, value)
	} else {
		err = h.loader.SetTenantOverride(tenant, configPath, value)
	}
	if err != nil {
		h.renderScopeWithToast(writer, request, tenant, toastData{Kind: "error", Message: err.Error()})
		return
	}
	h.renderScopeWithToast(writer, request, tenant, toastData{Kind: "ok", Message: "Override applied."})
}

func (h *Handler) deleteOverride(writer http.ResponseWriter, request *http.Request) {
	tenant := strings.TrimSpace(request.URL.Query().Get("tenant"))
	configPath := strings.TrimSpace(request.URL.Query().Get("path"))
	var (
		removed bool
		err     error
	)
	if tenant == "" {
		removed, err = h.loader.DeleteOverride(configPath)
	} else {
		removed, err = h.loader.DeleteTenantOverride(tenant, configPath)
	}
	if err != nil {
		h.renderScopeWithToast(writer, request, tenant, toastData{Kind: "error", Message: err.Error()})
		return
	}
	if !removed {
		h.renderScopeWithToast(writer, request, tenant, toastData{Kind: "error", Message: "Override not found."})
		return
	}
	h.renderScopeWithToast(writer, request, tenant, toastData{Kind: "ok", Message: "Override removed."})
}

func (h *Handler) reload(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := h.loader.Reload(request.Context()); err != nil {
		h.renderToast(writer, request, toastData{Kind: "error", Message: err.Error()})
		return
	}
	writer.Header().Set("HX-Trigger", "configChanged")
	h.renderToast(writer, request, toastData{Kind: "ok", Message: "Configuration reloaded."})
}

func (h *Handler) pageData(ctx context.Context, tenant, query string) (pageData, error) {
	codes, err := h.loader.TenantCodes(ctx)
	if err != nil {
		return pageData{}, err
	}
	scope, err := h.scopeData(ctx, tenant, strings.TrimSpace(query), len(codes))
	if err != nil {
		return pageData{}, err
	}
	return pageData{Tenant: tenant, Codes: codes, Scope: scope}, nil
}

func (h *Handler) scopeData(ctx context.Context, tenant, query string, tenantCount int) (scopeData, error) {
	scope := scopeData{
		Tenant:      tenant,
		IsTenant:    tenant != "",
		TenantCount: tenantCount,
		Filter:      query,
	}
	var (
		object    map[string]any
		overrides map[string]any
		err       error
	)
	if tenant == "" {
		object, err = h.loader.Map(ctx)
		if err != nil {
			return scopeData{}, err
		}
		overrides = h.loader.Overrides()
		scope.Eyebrow = "root configuration"
		scope.Title = "Root configuration"
		scope.Subtitle = "The merged configuration that every tenant inherits, after sources, hierarchy and placeholders."
	} else {
		object, err = h.loader.TenantMap(ctx, tenant)
		if err != nil {
			return scopeData{}, err
		}
		overrides = h.loader.TenantOverrides(tenant)
		scope.Eyebrow = "tenant"
		scope.Title = tenant
		scope.Subtitle = "Complete configuration for this tenant, including values inherited from the root."
	}
	rows, matched, total := flatten(object, "", 0, tenant == "", query, rowLimit)
	scope.Rows = rows
	scope.Matched = matched
	scope.Total = total
	scope.KeyCount = total
	scope.Overrides = overrideRows(overrides)
	scope.OverrideCount = len(scope.Overrides)
	return scope, nil
}

func (h *Handler) render(writer http.ResponseWriter, request *http.Request, name string, data any) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(writer, name, data); err != nil {
		h.logger.ErrorContext(request.Context(), "fastconf: console render", "template", name, "error", err)
	}
}

func (h *Handler) renderToast(writer http.ResponseWriter, request *http.Request, toast toastData) {
	h.render(writer, request, "toast", toast)
}

func (h *Handler) renderScopeWithToast(writer http.ResponseWriter, request *http.Request, tenant string, toast toastData) {
	data, err := h.pageData(request.Context(), tenant, "")
	if err != nil {
		h.renderToast(writer, request, toastData{Kind: "error", Message: err.Error()})
		return
	}
	data.Swap = true
	var buffer bytes.Buffer
	if err := h.templates.ExecuteTemplate(&buffer, "scope", data); err != nil {
		h.logger.ErrorContext(request.Context(), "fastconf: console render", "template", "scope", "error", err)
		http.Error(writer, "render error", http.StatusInternalServerError)
		return
	}
	if err := h.templates.ExecuteTemplate(&buffer, "toast", toast); err != nil {
		h.logger.ErrorContext(request.Context(), "fastconf: console render", "template", "toast", "error", err)
		http.Error(writer, "render error", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write(buffer.Bytes())
}

func (h *Handler) renderError(writer http.ResponseWriter, request *http.Request, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, fastconf.ErrTenantNotFound) {
		status = http.StatusNotFound
	}
	h.logger.WarnContext(request.Context(), "fastconf: console request failed", "error", err)
	http.Error(writer, err.Error(), status)
}

type pageData struct {
	Tenant string
	Codes  []string
	Scope  scopeData
	Swap   bool
}

type scopeData struct {
	Tenant        string
	IsTenant      bool
	Eyebrow       string
	Title         string
	Subtitle      string
	Rows          []configRow
	Matched       int
	Total         int
	KeyCount      int
	TenantCount   int
	Filter        string
	Overrides     []overrideRow
	OverrideCount int
}

type configRow struct {
	Path  string
	Key   string
	Value string
	Type  string
	Depth int
}

type overrideRow struct {
	Path  string
	Value string
}

type toastData struct {
	Kind    string
	Message string
}

func flatten(object map[string]any, prefix string, depth int, skipTenants bool, filter string, limit int) ([]configRow, int, int) {
	rows := make([]configRow, 0, 16)
	matched := 0
	total := 0
	var walk func(values map[string]any, prefix string, depth int)
	walk = func(values map[string]any, prefix string, depth int) {
		childKeys := make([]string, 0, len(values))
		for key := range values {
			childKeys = append(childKeys, key)
		}
		sort.Strings(childKeys)
		for _, key := range childKeys {
			if depth == 0 && skipTenants && key == "tenants" {
				continue
			}
			value := values[key]
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			total++
			preview, typeName := valuePreview(value)
			if filter == "" || strings.Contains(strings.ToLower(path), filter) || strings.Contains(strings.ToLower(preview), filter) {
				matched++
				if len(rows) < limit {
					rows = append(rows, configRow{Path: path, Key: key, Value: preview, Type: typeName, Depth: depth})
				}
			}
			if child, ok := value.(map[string]any); ok && depth < 8 {
				walk(child, path, depth+1)
			}
		}
	}
	walk(object, prefix, depth)
	return rows, matched, total
}

func valuePreview(value any) (string, string) {
	switch typed := value.(type) {
	case nil:
		return "null", "null"
	case bool:
		return strconv.FormatBool(typed), "boolean"
	case string:
		if len(typed) > 100 {
			return strconv.Quote(typed[:97] + "..."), "string"
		}
		return strconv.Quote(typed), "string"
	case int:
		return strconv.Itoa(typed), "number"
	case int64:
		return strconv.FormatInt(typed, 10), "number"
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), "number"
	case map[string]any:
		return fmt.Sprintf("{%d keys}", len(typed)), "object"
	case []any:
		return fmt.Sprintf("[%d items]", len(typed)), "list"
	default:
		return fmt.Sprint(value), "value"
	}
}

func overrideRows(values map[string]any) []overrideRow {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]overrideRow, 0, len(keys))
	for _, key := range keys {
		encoded, err := json.Marshal(values[key])
		if err != nil {
			encoded = fmt.Append(nil, values[key])
		}
		text := string(encoded)
		if len(text) > 160 {
			text = text[:157] + "..."
		}
		rows = append(rows, overrideRow{Path: key, Value: text})
	}
	return rows
}
