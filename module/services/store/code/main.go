package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()
	if _, err := codefly.Init(ctx); err != nil {
		return fmt.Errorf("initialize Codefly SDK: %w", err)
	}
	databaseURL, err := codefly.For(ctx).Service("store").Secret("postgres", "owner-connection")
	if err != nil {
		return fmt.Errorf("resolve store owner connection through Codefly SDK: %w", err)
	}
	return migrateStore(databaseURL)
}

func migrateStore(databaseURL string) error {
	if databaseURL == "" {
		return errors.New("store owner connection is empty")
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
