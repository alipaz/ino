// Package ranking computes explore feed preferences and ranking without I/O.
package ranking

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"time"

	"ino/internal/domain"
)

const (
	reelFreshnessHalfLife  = 48 * time.Hour
	activityHalfLife       = 7 * 24 * time.Hour
	untimedWatchFactor     = .5
	engagementSaturation   = 100
	maxPreferredCategories = 5
)

// EngagementFacts counts signals for a candidate. In personal history, the
// counts may be fractional because recent signals carry more weight.
type EngagementFacts struct {
	Shares         float64
	Saves          float64
	QualityWatches float64
	Likes          float64
	Views          float64
	QuickSkips     float64
}

type ScoringRule struct {
	Name   string
	Weight float64
	Count  func(EngagementFacts) float64
}

type ScoringPolicy struct {
	Rules []ScoringRule
}

func V1Policy() ScoringPolicy {
	return ScoringPolicy{Rules: []ScoringRule{
		{Name: "share", Weight: 5, Count: func(facts EngagementFacts) float64 { return facts.Shares }},
		{Name: "active save", Weight: 4, Count: func(facts EngagementFacts) float64 { return facts.Saves }},
		{Name: "quality watch", Weight: 3, Count: func(facts EngagementFacts) float64 { return facts.QualityWatches }},
		{Name: "active like", Weight: 2, Count: func(facts EngagementFacts) float64 { return facts.Likes }},
		{Name: "simple view", Weight: 0, Count: func(facts EngagementFacts) float64 { return facts.Views }},
		{Name: "quick skip", Weight: -2, Count: func(facts EngagementFacts) float64 { return facts.QuickSkips }},
	}}
}

func (policy ScoringPolicy) engagement(facts EngagementFacts) float64 {
	var score float64
	for _, rule := range policy.Rules {
		score += rule.Weight * rule.Count(facts)
	}
	return score
}

type WeightedFeedRanker struct {
	policy ScoringPolicy
}

func NewWeightedFeedRanker(policy ScoringPolicy) *WeightedFeedRanker {
	// Copy the rules so later edits to the caller's policy cannot change a
	// ranker that is already serving requests.
	return &WeightedFeedRanker{policy: ScoringPolicy{Rules: slices.Clone(policy.Rules)}}
}

func (ranker *WeightedFeedRanker) Rank(candidates []domain.FeedCandidate, preferences domain.FeedPreferences, now time.Time) []int64 {
	type scoredReel struct {
		id        int64
		createdAt time.Time
		score     float64
	}
	scored := make([]scoredReel, 0, len(candidates))
	for _, candidate := range candidates {
		scored = append(scored, scoredReel{candidate.ID, candidate.CreatedAt, ranker.score(candidate, preferences, now)})
	}
	slices.SortFunc(scored, func(a, b scoredReel) int {
		if comparison := cmp.Compare(b.score, a.score); comparison != 0 {
			return comparison
		}
		if comparison := b.createdAt.Compare(a.createdAt); comparison != 0 {
			return comparison
		}
		return cmp.Compare(b.id, a.id)
	})
	ids := make([]int64, 0, len(scored))
	for _, reel := range scored {
		ids = append(ids, reel.id)
	}
	return ids
}

func (ranker *WeightedFeedRanker) score(candidate domain.FeedCandidate, preferences domain.FeedPreferences, now time.Time) float64 {
	engagement := ranker.policy.engagement(EngagementFacts{
		Shares: float64(candidate.Shares), Saves: float64(candidate.Saves),
		QualityWatches: float64(candidate.QualityWatches), Likes: float64(candidate.Likes),
		Views: float64(candidate.Views), QuickSkips: float64(candidate.QuickSkips),
	})
	normalized := math.Copysign(math.Log1p(min(math.Abs(engagement), engagementSaturation))/math.Log1p(engagementSaturation), engagement)
	freshness := decay(now, candidate.CreatedAt, reelFreshnessHalfLife)
	baseScore := .55*freshness + .45*normalized

	personalWeight := min(.35, .07*float64(preferences.UsefulReels))
	if personalWeight == 0 {
		return baseScore
	}
	categories := distinctCategories(candidate.Categories)
	var affinity float64
	for _, category := range categories {
		affinity += preferences.Affinity[category]
	}
	if len(categories) > 0 {
		affinity /= float64(len(categories))
	}
	return (1-personalWeight)*baseScore + personalWeight*affinity
}

func decay(now, at time.Time, halfLife time.Duration) float64 {
	age := max(0, now.Sub(at))
	return math.Exp2(-float64(age) / float64(halfLife))
}

func distinctCategories(categories []string) []string {
	distinct := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		if category != "" {
			distinct[category] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(distinct))
}
