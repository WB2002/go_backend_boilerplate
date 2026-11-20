package database

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
)

// RunMigrations executes goose migration commands against the provided sql.DB connection.
// dialect can be "postgres", "sqlite3", etc.
// command can be "up", "down", "status", "version", "reset", "redo".
func RunMigrations(db *sql.DB, dialect, migrationsDir, command string, args ...string) error {
	if db == nil {
		return fmt.Errorf("database connection is nil")
	}

	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("failed to set goose dialect %q: %w", dialect, err)
	}

	cmd := strings.ToLower(strings.TrimSpace(command))
	switch cmd {
	case "up":
		return goose.Up(db, migrationsDir)
	case "up-by-one":
		return goose.UpByOne(db, migrationsDir)
	case "down":
		return goose.Down(db, migrationsDir)
	case "status":
		return goose.Status(db, migrationsDir)
	case "version":
		return goose.Version(db, migrationsDir)
	case "reset":
		return goose.Reset(db, migrationsDir)
	case "redo":
		return goose.Redo(db, migrationsDir)
	default:
		return fmt.Errorf("unsupported migration command %q (available: up, up-by-one, down, status, version, reset, redo)", command)
	}
}

// GetCurrentVersion returns the latest applied migration version in the database.
func GetCurrentVersion(db *sql.DB, dialect string) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("database connection is nil")
	}
	if err := goose.SetDialect(dialect); err != nil {
		return 0, fmt.Errorf("failed to set goose dialect %q: %w", dialect, err)
	}
	return goose.GetDBVersion(db)
}
