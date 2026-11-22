package ranking

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"time"

	"ino/internal/domain"
)

type reelSignals struct {
	facts      EngagementFacts
	categories []string
	likeSeen   bool
	saveSeen   bool
	shareSeen  bool
}

// Preferences combines decayed chronological activity with separate untimed
// watch signals. It has no dependency on storage or the candidate filter.
func (ranker *WeightedFeedRanker) Preferences(signals domain.FeedSignals, now time.Time) domain.FeedPreferences {
	byReel := collectActivity(signals.Activity, now)
	addWatchSignals(byReel, signals.Watches)

	result := domain.FeedPreferences{Affinity: make(map[string]float64)}
	// Stable accumulation order avoids floating-point tie differences across
	// repeated requests for the same inputs.
	for _, reelID := range slices.Sorted(maps.Keys(byReel)) {
		signals := byReel[reelID]
		categories := distinctCategories(signals.categories)
		score := ranker.policy.engagement(signals.facts)
		if score == 0 || len(categories) == 0 {
			continue
		}
		result.UsefulReels++
		for _, category := range categories {
			result.Affinity[category] += score / float64(len(categories))
		}
	}

	var strongestMagnitude float64
	for _, score := range result.Affinity {
		strongestMagnitude = max(strongestMagnitude, math.Abs(score))
	}
	for category, score := range result.Affinity {
		if score == 0 {
			delete(result.Affinity, category)
			continue
		}
		result.Affinity[category] = score / strongestMagnitude
		if score > 0 {
			result.StrongestCategories = append(result.StrongestCategories, category)
		}
	}
	slices.SortFunc(result.StrongestCategories, func(a, b string) int {
		if comparison := cmp.Compare(result.Affinity[b], result.Affinity[a]); comparison != 0 {
			return comparison
		}
		return cmp.Compare(a, b)
	})
	result.StrongestCategories = result.StrongestCategories[:min(len(result.StrongestCategories), maxPreferredCategories)]
	return result
}

func collectActivity(events []domain.FeedActivity, now time.Time) map[int64]*reelSignals {
	ordered := slices.Clone(events)
	slices.SortStableFunc(ordered, func(a, b domain.FeedActivity) int {
		if comparison := b.Time.Compare(a.Time); comparison != 0 {
			return comparison
		}
		return cmp.Compare(b.ID, a.ID)
	})
	byReel := make(map[int64]*reelSignals)
	for _, event := range ordered[:min(len(ordered), 100)] {
		signals := signalsForReel(byReel, event.ReelID, event.Categories)
		weight := decay(now, event.Time, activityHalfLife)
		switch event.Type {
		case "like_added", "like_removed":
			if !signals.likeSeen && event.Type == "like_added" {
				signals.facts.Likes = weight
			}
			signals.likeSeen = true
		case "save_added", "save_removed":
			if !signals.saveSeen && event.Type == "save_added" {
				signals.facts.Saves = weight
			}
			signals.saveSeen = true
		case "share_created":
			if !signals.shareSeen {
				signals.facts.Shares = weight
				signals.shareSeen = true
			}
		case "view_counted":
			signals.facts.Views += weight
		}
		// watch_interest never contributes here. The stored maximum watch
		// time is the only source of watch scoring.
	}
	return byReel
}

func addWatchSignals(byReel map[int64]*reelSignals, watches []domain.FeedWatch) {
	bestWatches := make(map[int64]domain.FeedWatch, len(watches))
	for _, watch := range watches {
		if best, found := bestWatches[watch.ReelID]; !found || watch.WatchedMS > best.WatchedMS {
			bestWatches[watch.ReelID] = watch
		}
	}
	for reelID, watch := range bestWatches {
		facts := watchFacts(watch.WatchedMS, watch.DurationMS)
		signals := signalsForReel(byReel, reelID, watch.Categories)
		signals.facts.QualityWatches = facts.QualityWatches * untimedWatchFactor
		signals.facts.QuickSkips = facts.QuickSkips * untimedWatchFactor
	}
}

func signalsForReel(byReel map[int64]*reelSignals, id int64, categories []string) *reelSignals {
	signals := byReel[id]
	if signals == nil {
		signals = &reelSignals{categories: categories}
		byReel[id] = signals
	} else if len(signals.categories) == 0 {
		signals.categories = categories
	}
	return signals
}

func watchFacts(watchedMS, durationMS int64) EngagementFacts {
	if durationMS <= 0 || watchedMS < 0 {
		return EngagementFacts{}
	}
	if watchedMS < watchThreshold(durationMS, 2) {
		return EngagementFacts{QuickSkips: 1}
	}
	if watchedMS >= watchThreshold(durationMS, 7) {
		return EngagementFacts{QualityWatches: 1}
	}
	return EngagementFacts{}
}

func watchThreshold(durationMS, tenths int64) int64 {
	// The first whole millisecond at or above the exact boundary. Splitting
	// the duration before multiplying avoids overflow for bigint durations.
	return durationMS/10*tenths + (durationMS%10*tenths+9)/10
}
