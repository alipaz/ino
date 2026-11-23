package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestSwaggerRoutes(t *testing.T) {
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	for _, test := range []struct{ path, contentType, contains string }{
		{"/swagger/", "text/html", "openapi.json"},
		{"/swagger/index.html", "text/html", "swagger-ui-bundle.js"},
		{"/swagger/swagger-ui.css", "text/css", ".swagger-ui"},
		{"/swagger/swagger-ui-bundle.js", "javascript", "SwaggerUIBundle"},
		{"/swagger/openapi.json", "application/json", `"openapi": "3.0.3"`},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK ||
				!strings.Contains(response.Header().Get("Content-Type"), test.contentType) ||
				!strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("status=%d content-type=%q expected response containing %q",
					response.Code, response.Header().Get("Content-Type"), test.contains)
			}
		})
	}
}

func TestOpenAPICoversRegisteredRoutes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger, nil)
	RegisterFeedRoutes(router, logger, nil, nil)
	RegisterReelProductRoutes(router, logger, nil)
	RegisterInteractionReadRoutes(router, logger, nil, nil)
	writes := NewReelWriteRoutes(router, logger, nil)
	RegisterReelInteractionRoutes(writes, logger, nil)
	RegisterReelShareRoutes(writes, logger, nil)
	RegisterReelWatchSessionRoutes(writes, logger, nil)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/swagger/openapi.json", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("OpenAPI status = %d, want 200", response.Code)
	}
	var document struct {
		Paths map[string]map[string]json.RawMessage
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	pathParameter := regexp.MustCompile(`:([a-z_]+)`)
	for _, route := range router.Routes() {
		if strings.HasPrefix(route.Path, "/swagger") {
			continue
		}
		path := pathParameter.ReplaceAllString(route.Path, "{$1}")
		method := strings.ToLower(route.Method)
		if len(document.Paths[path][method]) == 0 {
			t.Errorf("OpenAPI missing %s %s", route.Method, path)
		}
		delete(document.Paths[path], method)
	}
	for path, fields := range document.Paths {
		for key := range fields {
			if key != "parameters" {
				t.Errorf("OpenAPI documents an unregistered operation %s %s", key, path)
			}
		}
	}
}
