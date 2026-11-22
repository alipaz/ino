package domain

import "time"

const (
	MaxFeedCandidates  = 200
	FeedRankingVersion = "v1"
)

type FeedCandidate struct {
	ID             int64
	CreatedAt      time.Time
	Likes          int64
	Saves          int64
	Shares         int64
	Views          int64
	QualityWatches int64
	QuickSkips     int64
	Categories     []string
}

type FeedActivity struct {
	ActivityEvent
	Categories []string
}

type FeedWatch struct {
	ReelID     int64
	WatchedMS  int64
	DurationMS int64
	Categories []string
}

type FeedSignals struct {
	Activity []FeedActivity
	Watches  []FeedWatch
}

type FeedPreferences struct {
	Affinity            map[string]float64
	StrongestCategories []string
	UsefulReels         int
}

type FeedReel struct {
	ID           int64
	Caption      string
	VideoURL     string
	ThumbnailURL string
	CreatedAt    time.Time
	DurationMS   int64
	LikeCount    int64
	SaveCount    int64
	ViewCount    int64
	CreatorID    int64
	CreatorName  string
	Products     []Product
}

type FeedPage struct {
	Items []FeedReel
	// Consumed includes ineligible positions skipped in the original ID list.
	Consumed int
}
