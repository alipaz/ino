package domain

import "time"

type InteractionState struct {
	Liked bool
	Saved bool
}

type ActivityEvent struct {
	ID     int64
	UserID int64
	ReelID int64
	Type   string
	Time   time.Time
}

// CacheChange is committed work waiting for the Redis projector.
type CacheChange struct {
	ID        int64
	Kind      string
	UserID    int64
	ReelID    int64
	CreatedAt time.Time
}
