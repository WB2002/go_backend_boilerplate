package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestGooseMigrationLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	migFile := filepath.Join(tempDir, "00001_create_test_table.sql")
	content := `-- +goose Up
CREATE TABLE items (id TEXT PRIMARY KEY, name TEXT NOT NULL);

-- +goose Down
DROP TABLE items;
`
	err := os.WriteFile(migFile, []byte(content), 0644)
	require.NoError(t, err)

	db, err := gorm.Open(sqlite.Open(filepath.Join(tempDir, "test.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	// Up
	err = RunMigrations(sqlDB, "sqlite3", tempDir, "up")
	assert.NoError(t, err)

	// Status / Version
	currentVer, err := GetCurrentVersion(sqlDB, "sqlite3")
	assert.NoError(t, err)
	assert.Equal(t, int64(1), currentVer)

	// Down
	err = RunMigrations(sqlDB, "sqlite3", tempDir, "down")
	assert.NoError(t, err)

	currentVerDown, err := GetCurrentVersion(sqlDB, "sqlite3")
	assert.NoError(t, err)
	assert.Equal(t, int64(0), currentVerDown)
}
