package database

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// WithTransaction executes the given callback inside a PostgreSQL database transaction.
// If the callback returns an error, the transaction is automatically rolled back.
// If the callback succeeds (returns nil), the transaction is committed.
func WithTransaction(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	if db == nil {
		return fmt.Errorf("database connection is nil")
	}
	return db.WithContext(ctx).Transaction(fn)
}
