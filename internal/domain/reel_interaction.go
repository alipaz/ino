package domain

// InteractionKind identifies an interaction that can be set and cleared.
type InteractionKind string

const (
	Like InteractionKind = "like"
	Save InteractionKind = "save"
)
