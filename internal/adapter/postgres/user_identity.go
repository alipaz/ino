package postgres

import (
	"context"
	"fmt"
)

func (writer *ReelWriter) UserExists(ctx context.Context, userID int64) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, reelWriteTimeout)
	defer cancel()
	var exists bool
	err := writer.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)", userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check user identity: %w", err)
	}
	return exists, nil
}
