package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"ino/internal/domain"
)

func TestFeedCursorValidatesScopeSignatureExpiryAndMaximumSize(t *testing.T) {
	cursors, err := NewFeedCursor([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	category := strings.Repeat("a", 64)
	ids := make([]int64, domain.MaxFeedCandidates)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	token, err := cursors.encode(feedBrowse{viewerID: 8, category: category, ids: ids, position: 199, expiresAt: now.Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != MaxFeedCursorSize || MaxFeedCursorSize != 2291 {
		t.Fatalf("max cursor size = %d, want 2291 bytes", len(token))
	}
	browse, err := cursors.decode(token, 8, category, now.Add(9*time.Minute))
	if err != nil || browse.position != 199 || !slices.Equal(browse.ids, ids) {
		t.Fatalf("decode max cursor: %+v, %v", browse, err)
	}

	tampered := []byte(token)
	if tampered[10] == 'A' {
		tampered[10] = 'B'
	} else {
		tampered[10] = 'A'
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	payload := raw[:len(raw)-sha256.Size]
	binary.BigEndian.PutUint16(payload[17:19], uint16(len(ids)))
	invalidPosition := base64.RawURLEncoding.EncodeToString(append(payload, cursors.sign(payload)...))
	for _, tc := range []struct {
		name, token, category string
		viewerID              int64
		now                   time.Time
		want                  error
	}{
		{"wrong viewer", token, category, 9, now, ErrInvalidFeedCursor},
		{"public viewer", token, category, 0, now, ErrInvalidFeedCursor},
		{"wrong filter", token, "outdoors", 8, now, ErrInvalidFeedCursor},
		{"tampered", string(tampered), category, 8, now, ErrInvalidFeedCursor},
		{"signed out-of-range position", invalidPosition, category, 8, now, ErrInvalidFeedCursor},
		{"too large", token + "A", category, 8, now, ErrInvalidFeedCursor},
		{"truncated", token[:len(token)-1], category, 8, now, ErrInvalidFeedCursor},
		{"expired", token, category, 8, now.Add(10 * time.Minute), ErrExpiredFeedCursor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cursors.decode(tc.token, tc.viewerID, tc.category, tc.now); !errors.Is(err, tc.want) {
				t.Fatalf("decode returned %v, want %v", err, tc.want)
			}
		})
	}
}

func TestFeedContinuationKeepsOrderAndSkipsIneligiblePositions(t *testing.T) {
	cursors, err := NewFeedCursor([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &feedFixture{ordering: []int64{1, 2, 3, 4, 5, 6}, hidden: make(map[int64]bool)}
	feed := NewFeed(fixture, fixture, fixture, fixture, cursors)
	request := FeedRequest{ViewerID: 8, Category: "outdoors", Limit: 2}
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)

	first, err := feed.Explore(context.Background(), request, now)
	if err != nil || first.NextCursor == nil || !slices.Equal(feedItemIDs(first.Items), []int64{1, 2}) {
		t.Fatalf("first page = %+v, %v", first, err)
	}

	fixture.ordering = []int64{6, 4, 2, 1, 5, 3}
	fixture.hidden[3] = true
	request.Cursor = *first.NextCursor
	second, err := feed.Explore(context.Background(), request, now.Add(8*time.Minute))
	if err != nil || second.NextCursor == nil || !slices.Equal(feedItemIDs(second.Items), []int64{4, 5}) {
		t.Fatalf("second page = %+v, %v", second, err)
	}

	request.Cursor = *second.NextCursor
	if _, err := feed.Explore(context.Background(), request, now.Add(10*time.Minute)); !errors.Is(err, ErrExpiredFeedCursor) {
		t.Fatalf("continuation extended original expiry: %v", err)
	}
	third, err := feed.Explore(context.Background(), request, now.Add(9*time.Minute))
	if err != nil || third.NextCursor != nil || !slices.Equal(feedItemIDs(third.Items), []int64{6}) {
		t.Fatalf("last page = %+v, %v", third, err)
	}

	if fixture.ranks != 1 || fixture.signalLoads != 1 || fixture.pageLoads != 3 {
		t.Fatalf("load counts: ranking=%d, signals=%d, pages=%d", fixture.ranks, fixture.signalLoads, fixture.pageLoads)
	}
}

func feedItemIDs(items []domain.FeedReel) []int64 {
	ids := make([]int64, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}

type feedFixture struct {
	ordering                      []int64
	hidden                        map[int64]bool
	ranks, signalLoads, pageLoads int
}

func (fixture *feedFixture) LoadFeedSignals(context.Context, int64) (domain.FeedSignals, error) {
	fixture.signalLoads++
	return domain.FeedSignals{}, nil
}
func (*feedFixture) LoadFeedCandidates(context.Context, string, []string) ([]domain.FeedCandidate, error) {
	return nil, nil
}
func (*feedFixture) Preferences(domain.FeedSignals, time.Time) domain.FeedPreferences {
	return domain.FeedPreferences{}
}
func (fixture *feedFixture) Rank([]domain.FeedCandidate, domain.FeedPreferences, time.Time) []int64 {
	fixture.ranks++
	return fixture.ordering
}
func (*feedFixture) ReadFeedOrder(ctx context.Context, _ int64, _, _ string, load func(context.Context) ([]int64, error)) ([]int64, error) {
	return load(ctx)
}
func (fixture *feedFixture) ReadFeedPage(_ context.Context, _ int64, _, _ string, ids []int64, limit int) (domain.FeedPage, error) {
	fixture.pageLoads++
	page := domain.FeedPage{}
	for i, id := range ids {
		if fixture.hidden[id] {
			continue
		}
		if len(page.Items) == limit {
			return page, nil
		}
		page.Items = append(page.Items, domain.FeedReel{ID: id})
		page.Consumed = i + 1
	}
	page.Consumed = len(ids)
	return page, nil
}
