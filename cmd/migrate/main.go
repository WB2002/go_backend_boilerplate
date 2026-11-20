package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/platform/database"
	"go_backend_boilerplate/internal/platform/logger"
)

func main() {
	dir := flag.String("dir", "migrations", "path to migrations directory")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("Usage: go run ./cmd/migrate [-dir=migrations] <command> [args...]")
		fmt.Println("Available commands: up, up-by-one, down, status, version, reset, redo, create <name>")
		os.Exit(1)
	}

	command := strings.ToLower(args[0])

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	log := logger.New(cfg.App.Env)

	// Create command does not need DB connection
	if command == "create" {
		if len(args) < 2 {
			fmt.Println("Usage: go run ./cmd/migrate create <name>")
			os.Exit(1)
		}
		name := args[1]
		fileName := fmt.Sprintf("%s/%s_%s.sql", *dir, "000003", name)
		template := `-- +goose Up
-- SQL in this section is executed after the migration is applied.

-- +goose Down
-- SQL in this section is executed after the migration is rolled back.
`
		if err := os.WriteFile(fileName, []byte(template), 0644); err != nil {
			log.Fatal().Err(err).Msg("failed to create migration file")
		}
		log.Info().Str("file", fileName).Msg("Migration created successfully")
		return
	}

	gormDB, err := database.Connect(cfg.Database)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to PostgreSQL database")
	}
	defer func() {
		_ = database.Close(gormDB)
	}()

	sqlDB, err := gormDB.DB()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get sql.DB handle")
	}

	if err := database.RunMigrations(sqlDB, "postgres", *dir, command, args[1:]...); err != nil {
		log.Fatal().Err(err).Str("command", command).Msg("migration failed")
	}

	log.Info().Str("command", command).Msg("Migration command completed successfully")
}
