package redis

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"ino/internal/domain"
	"ino/internal/usecase"
)

type CachedShareWriter struct {
	cache  *ReadCache
	writer usecase.ReelShareWriter
}

func NewCachedShareWriter(cache *ReadCache, writer usecase.ReelShareWriter) *CachedShareWriter {
	return &CachedShareWriter{cache: cache, writer: writer}
}

func (writer *CachedShareWriter) RecordReelShare(ctx context.Context, senderID, reelID, recipientID int64, idempotencyKey string) (domain.ReelShare, error) {
	cache := writer.cache
	if cache.client == nil {
		return writer.writer.RecordReelShare(ctx, senderID, reelID, recipientID, idempotencyKey)
	}
	key := fmt.Sprintf("%sreceipt:%d:%x", cachePrefix, senderID, sha256.Sum256([]byte(idempotencyKey)))
	readCtx, cancel := context.WithTimeout(ctx, cache.timeout)
	value, _ := cache.client.Get(readCtx, key).Result()
	cancel()
	if receipt, ok := decodeCached(value, func(receipt domain.ReelShare) bool {
		return receipt.ID > 0 && receipt.SenderID == senderID && receipt.IdempotencyKey == idempotencyKey &&
			receipt.ReelID > 0 && receipt.RecipientID > 0 && !receipt.CreatedAt.IsZero()
	}); ok {
		cache.logger.Debug("cache read", "kind", "share_receipt", "outcome", "hit")
		if receipt.ReelID != reelID || receipt.RecipientID != recipientID {
			return domain.ReelShare{}, usecase.ErrShareKeyConflict
		}
		return receipt, nil
	}
	cache.logger.Debug("cache fallback", "kind", "share_receipt", "outcome", "postgres")
	receipt, err := writer.writer.RecordReelShare(ctx, senderID, reelID, recipientID, idempotencyKey)
	if err != nil {
		return domain.ReelShare{}, err
	}

	// The PostgreSQL writer returns only after commit. Failures never populate Redis.
	if data, err := json.Marshal(receipt); err == nil {
		writeCtx, cancel := context.WithTimeout(ctx, cache.timeout)
		defer cancel()
		if err := cache.client.Set(writeCtx, key, data, receiptTTL).Err(); err != nil {
			cache.logger.Debug("cache fill failed", "kind", "share_receipt")
		}
	}
	return receipt, nil
}
