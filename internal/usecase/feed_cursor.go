package usecase

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"slices"
	"time"

	"ino/internal/domain"
)

var (
	ErrInvalidFeedCursor = errors.New("invalid feed cursor")
	ErrExpiredFeedCursor = errors.New("expired feed cursor")
)

const (
	feedCursorVersion     = 1
	feedCursorTTL         = 10 * time.Minute
	feedCursorHeaderSize  = 22
	maxFeedCategoryLength = 64
	maxFeedCursorBytes    = feedCursorHeaderSize + maxFeedCategoryLength + 8*domain.MaxFeedCandidates + sha256.Size
	// MaxFeedCursorSize is the unpadded base64url length of the maximum cursor.
	MaxFeedCursorSize = (maxFeedCursorBytes*8 + 5) / 6
)

type FeedCursor struct {
	secret []byte
}

func NewFeedCursor(secret []byte) (*FeedCursor, error) {
	if len(secret) < sha256.Size {
		return nil, errors.New("feed cursor secret must be at least 32 bytes")
	}
	return &FeedCursor{secret: slices.Clone(secret)}, nil
}

type feedBrowse struct {
	viewerID  int64
	category  string
	position  int
	ids       []int64
	expiresAt time.Time
}

func (cursor *FeedCursor) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, cursor.secret)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

// The binary payload carries its own snapshot, independent of Redis. Its
// header stores version, scope, expiration, position, ID count and category
// length. The category and ordered int64 IDs follow that header.
func (cursor *FeedCursor) encode(browse feedBrowse) (string, error) {
	if browse.position <= 0 || browse.position >= len(browse.ids) || browse.viewerID < 0 ||
		!validFeedCategory(browse.category) || !validFeedIDs(browse.ids) || browse.expiresAt.UnixMilli() <= 0 {
		return "", ErrInvalidFeedCursor
	}
	payload := make([]byte, 0, feedCursorHeaderSize+len(browse.category)+8*len(browse.ids)+sha256.Size)
	payload = append(payload, feedCursorVersion)
	payload = binary.BigEndian.AppendUint64(payload, uint64(browse.viewerID))
	payload = binary.BigEndian.AppendUint64(payload, uint64(browse.expiresAt.UnixMilli()))
	payload = binary.BigEndian.AppendUint16(payload, uint16(browse.position))
	payload = binary.BigEndian.AppendUint16(payload, uint16(len(browse.ids)))
	payload = append(payload, byte(len(browse.category)))
	payload = append(payload, browse.category...)
	for _, id := range browse.ids {
		payload = binary.BigEndian.AppendUint64(payload, uint64(id))
	}
	return base64.RawURLEncoding.EncodeToString(append(payload, cursor.sign(payload)...)), nil
}

func (cursor *FeedCursor) decode(token string, viewerID int64, category string, now time.Time) (feedBrowse, error) {
	if len(token) > MaxFeedCursorSize {
		return feedBrowse{}, ErrInvalidFeedCursor
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) < feedCursorHeaderSize+sha256.Size ||
		base64.RawURLEncoding.EncodeToString(raw) != token {
		return feedBrowse{}, ErrInvalidFeedCursor
	}
	payload, signature := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	if !hmac.Equal(signature, cursor.sign(payload)) || payload[0] != feedCursorVersion {
		return feedBrowse{}, ErrInvalidFeedCursor
	}

	scope := int64(binary.BigEndian.Uint64(payload[1:9]))
	expiration := int64(binary.BigEndian.Uint64(payload[9:17]))
	position := int(binary.BigEndian.Uint16(payload[17:19]))
	count := int(binary.BigEndian.Uint16(payload[19:21]))
	categoryEnd := feedCursorHeaderSize + int(payload[21])
	if scope != viewerID || scope < 0 || expiration <= 0 || count > domain.MaxFeedCandidates ||
		position <= 0 || position >= count || len(payload) != categoryEnd+8*count ||
		string(payload[feedCursorHeaderSize:categoryEnd]) != category || !validFeedCategory(category) {
		return feedBrowse{}, ErrInvalidFeedCursor
	}
	ids := make([]int64, count)
	for i := range ids {
		offset := categoryEnd + 8*i
		ids[i] = int64(binary.BigEndian.Uint64(payload[offset : offset+8]))
	}
	if !validFeedIDs(ids) {
		return feedBrowse{}, ErrInvalidFeedCursor
	}
	expiresAt := time.UnixMilli(expiration)
	if !now.Before(expiresAt) {
		return feedBrowse{}, ErrExpiredFeedCursor
	}
	return feedBrowse{viewerID: scope, category: category, position: position, ids: ids, expiresAt: expiresAt}, nil
}

func validFeedIDs(ids []int64) bool {
	if len(ids) > domain.MaxFeedCandidates {
		return false
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate || id <= 0 {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}
