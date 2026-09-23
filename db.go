package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"
)

var registerHookOnce sync.Once

// initConnectionHook registers a connection-level callback on modernc.org/sqlite.
// It ensures that any read-only connection (mode=ro) always enforces query_only and a busy timeout.
func initConnectionHook() {
	registerHookOnce.Do(func() {
		sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
			if strings.Contains(dsn, "mode=ro") {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if _, err := conn.ExecContext(ctx, "PRAGMA query_only = ON; PRAGMA busy_timeout = 5000;", nil); err != nil {
					return fmt.Errorf("failed to apply connection security pragmas: %w", err)
				}
			}
			return nil
		})
	})
}

// openSafeDB initializes a strictly read-only SQLite database connection.
// It enforces OS-level read-only file access and SQLite engine query_only pragma.
func openSafeDB(path string) (*sql.DB, error) {
	// 1. Verify file exists and is not a directory
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("database file not found or inaccessible: %w", err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("database path %q is a directory, expected SQLite database file", path)
	}

	// 2. Register driver-level connection hook for all mode=ro connections
	initConnectionHook()

	// 3. Construct safe SQLite URI
	cleanPath := filepath.ToSlash(filepath.Clean(path))
	dsn := fmt.Sprintf("file:%s?mode=ro", url.PathEscape(cleanPath))

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Use single connection to guarantee connection-level pragma invariants for local stdio MCP use
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(10 * time.Minute)

	// 4. Verify connectivity and explicitly assert query_only mode
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to reach database: %w", err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA query_only = ON; PRAGMA busy_timeout = 5000;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable query_only mode: %w", err)
	}

	log.Printf("[INFO] Connected safely to SQLite at %s (mode=ro, query_only=ON, busy_timeout=5000ms)", path)
	return db, nil
}
