package domain

// Product is an item that can be attached to a reel.
type Product struct {
	ID       int64
	Title    string
	Brand    string
	ImageURL string
	Price    int64 // USD cents.
	Category string
}
