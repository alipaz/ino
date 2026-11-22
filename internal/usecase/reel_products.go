package usecase

import (
	"context"
	"errors"
	"fmt"

	"ino/internal/domain"
)

var ErrReelNotFound = errors.New("published reel not found")

type ReelProductReader interface {
	// The boolean reports whether a published reel exists, even if it has no products.
	ListPublishedReelProducts(ctx context.Context, reelID int64) ([]domain.Product, bool, error)
}

type ReelProducts struct {
	reader ReelProductReader
}

func NewReelProducts(reader ReelProductReader) *ReelProducts {
	return &ReelProducts{reader: reader}
}

func (uc *ReelProducts) ListReelProducts(ctx context.Context, reelID int64) ([]domain.Product, error) {
	products, found, err := uc.reader.ListPublishedReelProducts(ctx, reelID)
	if err != nil {
		return nil, fmt.Errorf("list products for reel %d: %w", reelID, err)
	}
	if !found {
		return nil, ErrReelNotFound
	}
	return products, nil
}
