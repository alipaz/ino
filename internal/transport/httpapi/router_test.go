package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		err    error
		want   int
		status string
	}{
		{"live despite database outage", "/livez", errors.New("password=secret"), 200, "alive"},
		{"ready", "/readyz", nil, 200, "ready"},
		{"database unavailable", "/readyz", errors.New("password=secret"), 503, "not_ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			router := NewRouter(slog.New(slog.NewJSONHandler(&logs, nil)), func(context.Context) error {
				if tc.path == "/livez" {
					t.Fatal("liveness accessed database")
				}
				return tc.err
			})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != tc.want {
				t.Fatalf("status = %d; want %d", w.Code, tc.want)
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["status"] != tc.status {
				t.Fatalf("body = %v", body)
			}
			if strings.Contains(w.Body.String()+logs.String(), "secret") {
				t.Fatal("database error leaked")
			}
			var entry map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry); err != nil {
				t.Fatalf("request log: %v", err)
			}
			if entry["status"] != float64(tc.want) || entry["method"] != "GET" {
				t.Fatalf("request log = %v", entry)
			}
		})
	}
}

func TestReadinessBoundsDatabaseCheck(t *testing.T) {
	var logs bytes.Buffer
	called := false
	router := NewRouter(slog.New(slog.NewJSONHandler(&logs, nil)), func(ctx context.Context) error {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Fatal("readiness needs a deadline of at most one second")
		}
		<-ctx.Done()
		return ctx.Err()
	})
	start := time.Now()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if !called || w.Code != http.StatusServiceUnavailable {
		t.Fatalf("called = %v; status = %d", called, w.Code)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("readiness did not finish promptly")
	}
}

func TestReadinessPropagatesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	router := NewRouter(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), func(ctx context.Context) error {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("request cancellation lost")
		}
		return ctx.Err()
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(ctx))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestPanicRecoveryUsesStructuredLogging(t *testing.T) {
	var logs bytes.Buffer
	router := NewRouter(slog.New(slog.NewJSONHandler(&logs, nil)), func(context.Context) error { panic("password=secret") })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz?token=secret", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", w.Code)
	}
	if strings.Contains(logs.String()+w.Body.String(), "secret") {
		t.Fatal("panic or query leaked")
	}
	decoder := json.NewDecoder(&logs)
	for range 2 {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			t.Fatalf("structured panic/request logs: %v", err)
		}
	}
}
