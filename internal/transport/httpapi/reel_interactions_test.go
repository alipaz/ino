package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"ino/internal/domain"
	"ino/internal/usecase"
)

type interactionHTTPStore struct {
	mutations []string
}

func (*interactionHTTPStore) UserExists(_ context.Context, userID int64) (bool, error) {
	return userID == 1, nil
}

func (store *interactionHTTPStore) SetActiveReelInteraction(_ context.Context, userID, reelID int64, kind domain.InteractionKind, active bool) error {
	if userID != 1 {
		panic("unexpected user ID")
	}
	if reelID == 5 {
		return usecase.ErrReelNotFound
	}
	if reelID == 6 {
		return errors.New("sensitive db error")
	}
	action := "set"
	if !active {
		action = "clear"
	}
	store.mutations = append(store.mutations, action+" "+string(kind))
	return nil
}

func (store *interactionHTTPStore) RecordReelView(_ context.Context, _, _ int64, date time.Time) error {
	if date.Location() != time.UTC || date.Hour() != 0 || date.Minute() != 0 || date.Second() != 0 || date.Nanosecond() != 0 {
		panic("expected UTC date")
	}
	store.mutations = append(store.mutations, "view")
	return nil
}

func (*interactionHTTPStore) RecordReelShare(_ context.Context, senderID, reelID, recipientID int64, key string) (domain.ReelShare, error) {
	if recipientID == 3 {
		return domain.ReelShare{}, usecase.ErrRecipientNotFound
	}
	if key == "conflict" {
		return domain.ReelShare{}, usecase.ErrShareKeyConflict
	}
	return domain.ReelShare{ID: 8, SenderID: senderID, ReelID: reelID, RecipientID: recipientID, IdempotencyKey: key, CreatedAt: time.Unix(0, 0).UTC()}, nil
}

func (*interactionHTTPStore) PersistWatchProgress(_ context.Context, _, reelID int64, _ string, _ int64) (int64, int64, error) {
	if reelID == 2 {
		return 0, 0, usecase.ErrPlaybackReelConflict
	}
	return 21000, 30000, nil
}

func TestInteractionHTTPRoutes(t *testing.T) {
	store := &interactionHTTPStore{}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	router := NewRouter(logger, func(context.Context) error { return nil })
	routes := NewReelWriteRoutes(router, logger, store)
	RegisterReelInteractionRoutes(routes, logger, usecase.NewReelInteractions(store))
	RegisterReelShareRoutes(routes, logger, usecase.NewReelShares(store))
	RegisterReelWatchSessionRoutes(routes, logger, usecase.NewReelWatchSessions(store))

	for _, tc := range []struct {
		name, method, path, user, key, body string
		status                              int
		response                            string
	}{
		{"missing caller", "POST", "1/like", "", "", "", 401, `{"error":"invalid_user_identity"}`},
		{"malformed caller", "POST", "1/like", "+1", "", "", 401, `{"error":"invalid_user_identity"}`},
		{"unknown caller", "POST", "1/like", "2", "", "", 401, `{"error":"invalid_user_identity"}`},
		{"invalid reel", "POST", "0/like", "1", "", "", 400, `{"error":"invalid_reel_id"}`},
		{"unpublished reel", "POST", "5/like", "1", "", "", 404, `{"error":"reel_not_found"}`},
		{"database error", "POST", "6/like", "1", "", "", 500, `{"error":"internal_error"}`},
		{"like", "POST", "1/like", "1", "", "", 204, ""},
		{"unlike", "DELETE", "1/like", "1", "", "", 204, ""},
		{"save", "POST", "1/save", "1", "", "", 204, ""},
		{"unsave", "DELETE", "1/save", "1", "", "", 204, ""},
		{"view", "POST", "1/view", "1", "", "", 204, ""},
		{"missing key", "POST", "1/share", "1", "", `{"recipient_user_id":2}`, 400, `{"error":"invalid_idempotency_key"}`},
		{"long key", "POST", "1/share", "1", strings.Repeat("a", 129), `{"recipient_user_id":2}`, 400, `{"error":"invalid_idempotency_key"}`},
		{"self share", "POST", "1/share", "1", "share-1", `{"recipient_user_id":1}`, 400, `{"error":"self_share"}`},
		{"unknown recipient", "POST", "1/share", "1", "share-1", `{"recipient_user_id":3}`, 404, `{"error":"recipient_not_found"}`},
		{"share conflict", "POST", "1/share", "1", "conflict", `{"recipient_user_id":2}`, 409, `{"error":"idempotency_conflict"}`},
		{"share stored", "POST", "1/share", "1", "share-1", `{"recipient_user_id":2}`, 201, `{"id":8,"sender_id":1,"recipient_id":2,"reel_id":1,"idempotency_key":"share-1","created_at":"1970-01-01T00:00:00Z"}`},
		{"watch missing value", "PUT", "1/watch-sessions/play-1", "1", "", `{}`, 400, `{"error":"invalid_request_body"}`},
		{"watch negative", "PUT", "1/watch-sessions/play-1", "1", "", `{"watched_ms":-1}`, 400, `{"error":"invalid_request_body"}`},
		{"watch fractional", "PUT", "1/watch-sessions/play-1", "1", "", `{"watched_ms":1.5}`, 400, `{"error":"invalid_request_body"}`},
		{"watch trailing JSON", "PUT", "1/watch-sessions/play-1", "1", "", `{"watched_ms":1}{}`, 400, `{"error":"invalid_request_body"}`},
		{"watch session too long", "PUT", "1/watch-sessions/" + strings.Repeat("a", 129), "1", "", `{"watched_ms":1}`, 400, `{"error":"invalid_playback_id"}`},
		{"watch conflict", "PUT", "2/watch-sessions/play-1", "1", "", `{"watched_ms":1}`, 409, `{"error":"playback_reel_conflict"}`},
		{"watch returns persisted time", "PUT", "1/watch-sessions/play-1", "1", "", `{"watched_ms":1000}`, 200, `{"watched_ms":21000,"watch_percentage":70}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "/api/v1/reels/"+tc.path, strings.NewReader(tc.body))
			if tc.user != "" {
				request.Header.Set("X-User-ID", tc.user)
			}
			if tc.key != "" {
				request.Header.Set("Idempotency-Key", tc.key)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status || !sameReelResponse(response.Body.String(), tc.response) {
				t.Fatalf("got %d %s, want %d %s", response.Code, response.Body, tc.status, tc.response)
			}
			if response.Code != http.StatusNoContent && !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("unexpected content type %q", response.Header().Get("Content-Type"))
			}
		})
	}
	if got := strings.Join(store.mutations, ", "); got != "set like, clear like, set save, clear save, view" {
		t.Fatalf("unexpected mutations: %s", got)
	}
}

func sameReelResponse(actual, expected string) bool {
	if expected == "" {
		return actual == ""
	}
	var actualJSON, expectedJSON any
	return json.Unmarshal([]byte(actual), &actualJSON) == nil &&
		json.Unmarshal([]byte(expected), &expectedJSON) == nil &&
		reflect.DeepEqual(actualJSON, expectedJSON)
}
