package ranking

import (
	"math"
	"reflect"
	"testing"
	"time"

	"ino/internal/domain"
)

var rankingTime = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

func TestV1UsesTheSixSpecifiedSignalWeights(t *testing.T) {
	policy := V1Policy()
	for _, tt := range []struct {
		name  string
		facts EngagementFacts
		want  float64
	}{
		{"share", EngagementFacts{Shares: 1}, 5},
		{"active save", EngagementFacts{Saves: 1}, 4},
		{"quality watch", EngagementFacts{QualityWatches: 1}, 3},
		{"active like", EngagementFacts{Likes: 1}, 2},
		{"simple view", EngagementFacts{Views: 1}, 0},
		{"quick skip", EngagementFacts{QuickSkips: 1}, -2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := policy.engagement(tt.facts); got != tt.want {
				t.Errorf("engagement=%g, want %g", got, tt.want)
			}
		})
	}
	if len(policy.Rules) != 6 {
		t.Fatalf("rules=%d, want 6", len(policy.Rules))
	}
}

func TestWatchBandsUseExactBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name            string
		watched, length int64
		want            EngagementFacts
	}{
		{"just below 20 percent", 299, 1500, EngagementFacts{QuickSkips: 1}},
		{"exactly 20 percent", 300, 1500, EngagementFacts{}},
		{"just below 70 percent", 1049, 1500, EngagementFacts{}},
		{"exactly 70 percent", 1050, 1500, EngagementFacts{QualityWatches: 1}},
		{"round-up at an indivisible boundary", 14, 71, EngagementFacts{QuickSkips: 1}},
		{"bigint duration without overflow", 6456360425798343065, math.MaxInt64, EngagementFacts{QualityWatches: 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := watchFacts(tt.watched, tt.length); got != tt.want {
				t.Errorf("watch facts=%+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPersonalWatchUsesHighestSessionOnceAndIgnoresInterestEvents(t *testing.T) {
	ranker := NewWeightedFeedRanker(V1Policy())
	signals := domain.FeedSignals{
		Activity: []domain.FeedActivity{
			activity(3, 7, "watch_interest", rankingTime, "books"),
			activity(2, 8, "like_added", rankingTime, "music"),
		},
		Watches: []domain.FeedWatch{
			{ReelID: 7, WatchedMS: 700, DurationMS: 1000, Categories: []string{"books"}},
			{ReelID: 7, WatchedMS: 700, DurationMS: 1000, Categories: []string{"books"}},
			{ReelID: 7, WatchedMS: 100, DurationMS: 1000, Categories: []string{"books"}},
			{ReelID: 9, WatchedMS: 100, DurationMS: 1000, Categories: []string{"sport"}},
		},
	}
	got := ranker.Preferences(signals, rankingTime)
	want := domain.FeedPreferences{
		Affinity:            map[string]float64{"books": .75, "music": 1, "sport": -.5},
		StrongestCategories: []string{"music", "books"},
		UsefulReels:         3,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("preferences=%+v, want %+v", got, want)
	}
}

func TestUnlikeAndUnsaveCancelOlderSignals(t *testing.T) {
	ranker := NewWeightedFeedRanker(V1Policy())
	old := rankingTime.Add(-time.Hour)
	got := ranker.Preferences(domain.FeedSignals{Activity: []domain.FeedActivity{
		activity(1, 10, "like_added", old, "books"),
		activity(4, 11, "save_removed", rankingTime, "music"),
		activity(2, 11, "save_added", old, "music"),
		activity(3, 10, "like_removed", rankingTime, "books"),
		activity(5, 12, "view_counted", rankingTime, "sport"),
	}}, rankingTime)
	if got.UsefulReels != 0 || len(got.Affinity) != 0 || len(got.StrongestCategories) != 0 {
		t.Errorf("removed and zero-weight signals produced preferences: %+v", got)
	}
}

func TestAffinityDecaysActivityDeduplicatesSharesAndSplitsCategories(t *testing.T) {
	ranker := NewWeightedFeedRanker(V1Policy())
	got := ranker.Preferences(domain.FeedSignals{Activity: []domain.FeedActivity{
		activity(1, 10, "save_added", rankingTime, "books", "books", "sport"),
		activity(2, 11, "save_added", rankingTime.Add(-7*24*time.Hour), "music"),
		activity(3, 12, "share_created", rankingTime, "garden"),
		activity(4, 12, "share_created", rankingTime, "garden"),
	}}, rankingTime)
	want := domain.FeedPreferences{
		Affinity:            map[string]float64{"books": .4, "sport": .4, "music": .4, "garden": 1},
		StrongestCategories: []string{"garden", "books", "music", "sport"},
		UsefulReels:         3,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("preferences=%+v, want %+v", got, want)
	}
}

func TestUsersWithDifferentInterestsRankIdenticalCandidatesDifferently(t *testing.T) {
	ranker := NewWeightedFeedRanker(V1Policy())
	candidates := []domain.FeedCandidate{
		{ID: 1, CreatedAt: rankingTime, Categories: []string{"books"}},
		{ID: 2, CreatedAt: rankingTime, Categories: []string{"music"}},
	}
	for _, tt := range []struct {
		name    string
		signals domain.FeedSignals
		want    []int64
	}{
		{"books viewer", domain.FeedSignals{Activity: []domain.FeedActivity{activity(1, 8, "save_added", rankingTime, "books")}}, []int64{1, 2}},
		{"music viewer", domain.FeedSignals{Activity: []domain.FeedActivity{activity(2, 9, "save_added", rankingTime, "music")}}, []int64{2, 1}},
		{"cold start", domain.FeedSignals{}, []int64{2, 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ranker.Rank(candidates, ranker.Preferences(tt.signals, rankingTime), rankingTime); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("order=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestRankBreaksTiesByCreationThenID(t *testing.T) {
	ranker := NewWeightedFeedRanker(V1Policy())
	newer := rankingTime.Add(time.Minute)
	candidates := []domain.FeedCandidate{
		{ID: 5, CreatedAt: rankingTime},
		{ID: 1, CreatedAt: newer},
		{ID: 2, CreatedAt: newer},
	}
	if got := ranker.Rank(candidates, domain.FeedPreferences{}, rankingTime); !reflect.DeepEqual(got, []int64{2, 1, 5}) {
		t.Errorf("order=%v, want [2 1 5]", got)
	}
}

func TestPolicyExtensionUsesTheSameRankingLoop(t *testing.T) {
	candidates := []domain.FeedCandidate{
		{ID: 1, CreatedAt: rankingTime, Likes: 1},
		{ID: 2, CreatedAt: rankingTime, Views: 20},
	}
	policy := V1Policy()
	if got := NewWeightedFeedRanker(policy).Rank(candidates, domain.FeedPreferences{}, rankingTime); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf("V1 order=%v, want [1 2]", got)
	}
	policy.Rules = append(policy.Rules, ScoringRule{
		Name: "test view bonus", Weight: 1,
		Count: func(facts EngagementFacts) float64 { return facts.Views },
	})
	if got := NewWeightedFeedRanker(policy).Rank(candidates, domain.FeedPreferences{}, rankingTime); !reflect.DeepEqual(got, []int64{2, 1}) {
		t.Errorf("extended order=%v, want [2 1]", got)
	}
}

func TestScoreUsesFreshnessHalfLifeLogScaleAndPersonalCap(t *testing.T) {
	ranker := NewWeightedFeedRanker(V1Policy())
	candidate := domain.FeedCandidate{
		CreatedAt: rankingTime.Add(-48 * time.Hour), Shares: 1, Categories: []string{"books"},
	}
	base := .55*.5 + .45*math.Log1p(5)/math.Log1p(100)
	assertScore := func(name string, preferences domain.FeedPreferences, want float64) {
		t.Helper()
		if got := ranker.score(candidate, preferences, rankingTime); math.Abs(got-want) > 1e-12 {
			t.Errorf("%s score=%g, want %g", name, got, want)
		}
	}
	assertScore("public", domain.FeedPreferences{}, base)
	assertScore("one useful reel", domain.FeedPreferences{UsefulReels: 1, Affinity: map[string]float64{"books": 1}}, .93*base+.07)
	assertScore("personal cap", domain.FeedPreferences{UsefulReels: 10, Affinity: map[string]float64{"books": 1}}, .65*base+.35)

	candidate = domain.FeedCandidate{CreatedAt: rankingTime, QuickSkips: 1000}
	assertScore("negative saturation", domain.FeedPreferences{}, .55-.45)
	candidate = domain.FeedCandidate{CreatedAt: rankingTime, Shares: 1000}
	assertScore("positive saturation", domain.FeedPreferences{}, 1)
}

func activity(id, reelID int64, kind string, at time.Time, categories ...string) domain.FeedActivity {
	return domain.FeedActivity{
		ActivityEvent: domain.ActivityEvent{ID: id, ReelID: reelID, Type: kind, Time: at},
		Categories:    categories,
	}
}
