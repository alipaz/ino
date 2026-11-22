package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ino/internal/domain"
	"ino/internal/usecase"
)

type interactionReadStub struct {
	identity func(context.Context, int64) (bool, error)
	state    func(context.Context, int64, int64) (domain.InteractionState, bool, error)
	recent   func(context.Context, int64) ([]domain.ActivityEvent, error)
}

func (stub interactionReadStub) UserExists(ctx context.Context, userID int64) (bool, error) {
	return stub.identity(ctx, userID)
}

func (stub interactionReadStub) ReadInteractionState(ctx context.Context, userID, reelID int64) (domain.InteractionState, bool, error) {
	return stub.state(ctx, userID, reelID)
}

func (stub interactionReadStub) ListRecentInteractions(ctx context.Context, userID int64) ([]domain.ActivityEvent, error) {
	return stub.recent(ctx, userID)
}

func TestInteractionReadRoutes(t *testing.T) {
	const statePath = "/api/v1/reels/7/interaction-state"
	const recentPath = "/api/v1/me/interactions/recent"

	tests := []struct {
		name, path, user, body string
		status                 int
		identityErr, readErr   error
		stateMissing           bool
		events                 []domain.ActivityEvent
	}{
		{name: "missing identity", path: statePath, status: 401, body: `{"error":"invalid_user_identity"}`},
		{name: "malformed identity", path: recentPath, user: "+1", status: 401, body: `{"error":"invalid_user_identity"}`},
		{name: "unknown identity", path: recentPath, user: "2", status: 401, body: `{"error":"invalid_user_identity"}`},
		{name: "invalid reel ID", path: "/api/v1/reels/0/interaction-state", user: "1", status: 400, body: `{"error":"invalid_reel_id"}`},
		{name: "published reel with false flags", path: statePath, user: "1", status: 200, body: `{"liked":false,"saved":false}`},
		{name: "unpublished reel", path: statePath, user: "1", status: 404, body: `{"error":"reel_not_found"}`, stateMissing: true},
		{name: "empty activity history", path: recentPath, user: "1", status: 200, body: `{"events":[]}`},
		{
			name: "activity response preserves newest first order", path: recentPath, user: "1", status: 200,
			body: `{"events":[{"id":9,"user_id":1,"reel_id":7,"type":"view","event_time":"1970-01-01T00:00:03Z"},{"id":8,"user_id":1,"reel_id":6,"type":"save","event_time":"1970-01-01T00:00:02Z"}]}`,
			events: []domain.ActivityEvent{
				{ID: 9, UserID: 1, ReelID: 7, Type: "view", Time: time.Unix(3, 0)},
				{ID: 8, UserID: 1, ReelID: 6, Type: "save", Time: time.Unix(2, 0)},
			},
		},
		{name: "identity fallback unavailable", path: statePath, user: "1", status: 503, body: `{"error":"read_unavailable"}`, identityErr: fmt.Errorf("private details: %w", usecase.ErrReadUnavailable)},
		{name: "state fallback unavailable", path: statePath, user: "1", status: 503, body: `{"error":"read_unavailable"}`, readErr: fmt.Errorf("private details: %w", usecase.ErrReadUnavailable)},
		{name: "recent fallback unavailable", path: recentPath, user: "1", status: 503, body: `{"error":"read_unavailable"}`, readErr: usecase.ErrReadUnavailable},
		{name: "unclassified read failure", path: recentPath, user: "1", status: 500, body: `{"error":"internal_error"}`, readErr: fmt.Errorf("private details")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := interactionReadStub{
				identity: func(_ context.Context, userID int64) (bool, error) {
					return userID == 1, test.identityErr
				},
				state: func(_ context.Context, userID, reelID int64) (domain.InteractionState, bool, error) {
					if userID != 1 || reelID != 7 {
						t.Fatalf("unexpected identity/reel (%d, %d) reached state reader", userID, reelID)
					}
					return domain.InteractionState{}, !test.stateMissing, test.readErr
				},
				recent: func(_ context.Context, userID int64) ([]domain.ActivityEvent, error) {
					if userID != 1 {
						t.Fatalf("unexpected identity %d reached recent reader", userID)
					}
					return test.events, test.readErr
				},
			}

			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			router := NewRouter(logger, func(context.Context) error { return nil })
			RegisterInteractionReadRoutes(router, logger, stub, usecase.NewInteractionReads(stub, stub))
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.user != "" {
				request.Header.Set("X-User-ID", test.user)
			}

			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status || !sameReelResponse(response.Body.String(), test.body) {
				t.Fatalf("got %d %s; want %d %s", response.Code, response.Body, test.status, test.body)
			}
		})
	}
}
