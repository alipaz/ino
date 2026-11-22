package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"ino/internal/domain"
	"ino/internal/usecase"
)

type reelProductReaderFunc func(context.Context, int64) ([]domain.Product, bool, error)

func (read reelProductReaderFunc) ListPublishedReelProducts(ctx context.Context, reelID int64) ([]domain.Product, bool, error) {
	return read(ctx, reelID)
}

func TestListReelProductsRejectsInvalidID(t *testing.T) {
	for _, id := range []string{"abc", "0", "-1", "+1", "1.5", "9223372036854775808"} {
		t.Run(id, func(t *testing.T) {
			response := requestReelProducts(t, id, "", func(context.Context, int64) ([]domain.Product, bool, error) {
				t.Fatal("invalid ID reached the reader")
				return nil, false, nil
			})
			assertProductResponse(t, response, http.StatusBadRequest, `{"error":"invalid_reel_id"}`)
		})
	}
}

func TestListReelProductsMapsResponses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		userID   string
		products []domain.Product
		found    bool
		err      error
		status   int
		body     string
	}{
		{
			name: "published with products", userID: "ignored",
			products: []domain.Product{{ID: 4, Title: "Bottle", Brand: "INO", ImageURL: "https://example.com/bottle", Price: 1299, Category: "drinkware"}},
			found:    true, status: 200,
			body: `{"products":[{"id":4,"title":"Bottle","brand":"INO","image_url":"https://example.com/bottle","price":1299,"category":"drinkware"}]}`,
		},
		{name: "published without products", found: true, status: 200, body: `{"products":[]}`},
		{name: "no published reel", status: 404, body: `{"error":"reel_not_found"}`},
		{name: "bounded fallback unavailable", err: usecase.ErrReadUnavailable, status: 503, body: `{"error":"read_unavailable"}`},
		{name: "unexpected database failure", err: errors.New("postgres: sensitive internal details"), status: 500, body: `{"error":"internal_error"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := requestReelProducts(t, "9223372036854775807", tc.userID, func(_ context.Context, reelID int64) ([]domain.Product, bool, error) {
				if reelID != 9223372036854775807 {
					t.Fatalf("reel ID = %d", reelID)
				}
				return tc.products, tc.found, tc.err
			})
			assertProductResponse(t, response, tc.status, tc.body)
		})
	}
}

func requestReelProducts(t *testing.T, id, userID string, read reelProductReaderFunc) *httptest.ResponseRecorder {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	router := NewRouter(logger, func(context.Context) error { return nil })
	RegisterReelProductRoutes(router, logger, usecase.NewReelProducts(read))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/reels/"+id+"/products", nil).WithContext(ctx)
	if userID != "" {
		request.Header.Set("X-User-ID", userID)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestListReelProductsPassesRequestCancellation(t *testing.T) {
	response := requestReelProducts(t, "1", "", func(ctx context.Context, _ int64) ([]domain.Product, bool, error) {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("request cancellation was lost")
		}
		return nil, true, nil
	})
	assertProductResponse(t, response, 200, `{"products":[]}`)
}

func assertProductResponse(t *testing.T, response *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if response.Code != status || response.Body.String() != body {
		t.Fatalf("response = %d %s, want %d %s", response.Code, response.Body, status, body)
	}
}
