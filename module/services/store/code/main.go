package main

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

const databaseURLEnv = "CODEFLY__SERVICE_SECRET_CONFIGURATION__SAAS_STARTER__STORE__POSTGRES__CONNECTION"

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	databaseURL := os.Getenv(databaseURLEnv)
	if databaseURL == "" {
		return fmt.Errorf("%s is required", databaseURLEnv)
	}

	runner, err := migrate.New("file:///app/migrations", databaseURL)
	if err != nil {
		return fmt.Errorf("initialize database migrations: %w", err)
	}
	defer func() {
		sourceErr, databaseErr := runner.Close()
		if sourceErr != nil {
			log.Printf("close migration source: %v", sourceErr)
		}
		if databaseErr != nil {
			log.Printf("close migration database: %v", databaseErr)
		}
	}()

	if err := runner.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	return nil
}
