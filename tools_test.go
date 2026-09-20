package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupTestDB creates a temporary SQLite database with sample data.
func setupTestDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create with standard rw connection first
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}
	defer db.Close()

	schema := `
		CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT UNIQUE,
			role TEXT DEFAULT 'member'
		);
		INSERT INTO users (id, name, email, role) VALUES 
			(1, 'Alice Smith', 'alice@example.com', 'admin'),
			(2, 'Bob Jones', 'bob@example.com', 'member');

		CREATE TABLE posts (
			id INTEGER PRIMARY KEY,
			user_id INTEGER,
			title TEXT NOT NULL
		);
		INSERT INTO posts (id, user_id, title) VALUES (1, 1, 'First Post');
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}

	return dbPath
}

func TestReadOnlyEnforcement(t *testing.T) {
	dbPath := setupTestDB(t)

	db, err := openSafeDB(dbPath)
	if err != nil {
		t.Fatalf("openSafeDB failed: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. SELECT should succeed
	res, err := readQuery(ctx, db, "SELECT id, name FROM users ORDER BY id ASC")
	if err != nil {
		t.Fatalf("readQuery failed: %v", err)
	}
	if !strings.Contains(res, "Alice Smith") {
		t.Errorf("expected result to contain Alice Smith, got: %s", res)
	}

	// 2. INSERT should fail
	_, err = readQuery(ctx, db, "INSERT INTO users (id, name) VALUES (3, 'Mallory')")
	if err == nil {
		t.Fatal("expected INSERT to fail on read-only database, but it succeeded")
	}
	if !strings.Contains(err.Error(), "readonly database") {
		t.Errorf("expected readonly database error, got: %v", err)
	}

	// 3. DROP should fail
	_, err = readQuery(ctx, db, "DROP TABLE users")
	if err == nil {
		t.Fatal("expected DROP to fail on read-only database, but it succeeded")
	}
	if !strings.Contains(err.Error(), "readonly database") {
		t.Errorf("expected readonly database error, got: %v", err)
	}
}

func TestListTables(t *testing.T) {
	dbPath := setupTestDB(t)

	db, err := openSafeDB(dbPath)
	if err != nil {
		t.Fatalf("openSafeDB failed: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	tables, err := listTables(ctx, db)
	if err != nil {
		t.Fatalf("listTables failed: %v", err)
	}

	if !strings.Contains(tables, "users") || !strings.Contains(tables, "posts") {
		t.Errorf("expected users and posts in tables, got: %s", tables)
	}
}

func TestDescribeTable(t *testing.T) {
	dbPath := setupTestDB(t)

	db, err := openSafeDB(dbPath)
	if err != nil {
		t.Fatalf("openSafeDB failed: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	desc, err := describeTable(ctx, db, "users")
	if err != nil {
		t.Fatalf("describeTable failed: %v", err)
	}

	if !strings.Contains(desc, "email") || !strings.Contains(desc, "role") {
		t.Errorf("expected email and role in table description, got: %s", desc)
	}

	// Test invalid table name sanitization
	_, err = describeTable(ctx, db, "users; DROP TABLE users;")
	if err == nil {
		t.Fatal("expected injection attempt in table name to be rejected")
	}
}

func TestReadQueryRowCapping(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "large.db")

	rwDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open rw db: %v", err)
	}

	_, err = rwDB.Exec("CREATE TABLE numbers (n INTEGER);")
	if err != nil {
		t.Fatalf("create table failed: %v", err)
	}

	tx, _ := rwDB.Begin()
	stmt, _ := tx.Prepare("INSERT INTO numbers (n) VALUES (?)")
	for i := 1; i <= 100; i++ {
		stmt.Exec(i)
	}
	stmt.Close()
	tx.Commit()
	rwDB.Close()

	safeDB, err := openSafeDB(dbPath)
	if err != nil {
		t.Fatalf("openSafeDB failed: %v", err)
	}
	defer safeDB.Close()

	ctx := context.Background()
	out, err := readQuery(ctx, safeDB, "SELECT n FROM numbers ORDER BY n ASC")
	if err != nil {
		t.Fatalf("readQuery failed: %v", err)
	}

	if !strings.Contains(out, "[NOTICE: Output truncated at 50 rows") {
		t.Error("expected output to contain truncation warning notice")
	}
}

func init() {
	// Quiet logger during test runs
	os.Stderr, _ = os.Open(os.DevNull)
}
