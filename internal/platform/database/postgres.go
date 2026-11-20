package database

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"go_backend_boilerplate/internal/config"
)

// BuildDSN constructs a properly URL-encoded PostgreSQL connection URL from DatabaseConfig.
func BuildDSN(cfg config.DatabaseConfig) string {
	port := cfg.Port
	if port <= 0 {
		port = 5432
	}

	sslMode := cfg.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}

	dbPath := cfg.DBName
	if !strings.HasPrefix(dbPath, "/") {
		dbPath = "/" + dbPath
	}

	var userInfo *url.Userinfo
	if cfg.Password != "" {
		userInfo = url.UserPassword(cfg.User, cfg.Password)
	} else {
		userInfo = url.User(cfg.User)
	}

	q := make(url.Values)
	q.Set("sslmode", sslMode)
	q.Set("connect_timeout", "2")

	u := &url.URL{
		Scheme:   "postgres",
		User:     userInfo,
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(port)),
		Path:     dbPath,
		RawQuery: q.Encode(),
	}

	return u.String()
}

// New creates and configures a new GORM PostgreSQL connection pool.
func New(cfg config.DatabaseConfig) (*gorm.DB, error) {
	return NewPostgres(cfg)
}

// Connect initializes and returns a GORM PostgreSQL connection (alias for New).
func Connect(cfg config.DatabaseConfig) (*gorm.DB, error) {
	return New(cfg)
}

// NewPostgres initializes GORM with the PostgreSQL driver, tunes connection pooling,
// and verifies connectivity.
func NewPostgres(cfg config.DatabaseConfig) (*gorm.DB, error) {
	if cfg.Host == "" {
		return nil, errors.New("postgres host is required")
	}
	if cfg.User == "" {
		return nil, errors.New("postgres user is required")
	}
	if cfg.DBName == "" {
		return nil, errors.New("postgres db name is required")
	}

	maxOpenConns := cfg.MaxOpenConns
	if maxOpenConns <= 0 {
		maxOpenConns = 25
	}

	maxIdleConns := cfg.MaxIdleConns
	if maxIdleConns <= 0 {
		maxIdleConns = 5
	}

	connMaxLifetime := cfg.ConnMaxLifetime
	if connMaxLifetime <= 0 {
		connMaxLifetime = 300
	}

	dsn := BuildDSN(cfg)

	gormCfg := &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	}

	db, err := gorm.Open(postgres.Open(dsn), gormCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to open gorm postgres connection: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(connMaxLifetime) * time.Second)
	sqlDB.SetConnMaxIdleTime(time.Duration(connMaxLifetime/2) * time.Second)

	// Verify connection with health check ping
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to ping postgres: %w", err)
	}

	return db, nil
}

// Ping checks database health using PingContext on the underlying *sql.DB.
func Ping(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return errors.New("nil gorm database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to retrieve sql.DB: %w", err)
	}
	return sqlDB.PingContext(ctx)
}

// Close gracefully closes the underlying sql.DB connection pool.
func Close(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
