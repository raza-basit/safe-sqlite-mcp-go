package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"time"

	_ "modernc.org/sqlite"
)

// openSafeDB initializes a strictly read-only SQLite database connection.
// It enforces both OS-level read-only file access and SQLite engine query_only pragma.
func openSafeDB(path string) (*sql.DB, error) {
	// mode=ro guarantees the OS-level file descriptor cannot write
	dsn := fmt.Sprintf("file:%s?mode=ro", url.PathEscape(path))

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Optimize connection pooling for local embedded tool use
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Verify connectivity and enable query_only pragma
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to reach database: %w", err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA query_only = ON;"); err != nil {
		return nil, fmt.Errorf("failed to enable query_only mode: %w", err)
	}

	log.Printf("[INFO] Connected safely to SQLite at %s (mode=ro, query_only=ON)", path)
	return db, nil
}
