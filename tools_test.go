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
			password_hash TEXT DEFAULT 'secret_argon2_hash',
			role TEXT DEFAULT 'member'
		);
		INSERT INTO users (id, name, email, password_hash, role) VALUES 
			(1, 'Alice Smith', 'alice@example.com', 'hash_123', 'admin'),
			(2, 'Bob Jones', 'bob@example.com', 'hash_456', 'member');

		CREATE TABLE posts (
			id INTEGER PRIMARY KEY,
			user_id INTEGER,
			title TEXT NOT NULL
		);
		INSERT INTO posts (id, user_id, title) VALUES (1, 1, 'First Post');

		CREATE TABLE secret_audit (
			id INTEGER PRIMARY KEY,
			api_key TEXT NOT NULL
		);
		INSERT INTO secret_audit (id, api_key) VALUES (1, 'sk_live_supersecret');
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

	state := &ServerState{
		DB:     db,
		Policy: NewDefaultPolicy(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. SELECT allowed columns should succeed
	res, err := readQuery(ctx, state, "SELECT id, name FROM users ORDER BY id ASC")
	if err != nil {
		t.Fatalf("readQuery failed: %v", err)
	}
	if !strings.Contains(res, "Alice Smith") {
		t.Errorf("expected result to contain Alice Smith, got: %s", res)
	}

	// 2. INSERT should fail
	_, err = readQuery(ctx, state, "INSERT INTO users (id, name) VALUES (3, 'Mallory')")
	if err == nil {
		t.Fatal("expected INSERT to fail on read-only database, but it succeeded")
	}
	if !strings.Contains(err.Error(), "readonly database") {
		t.Errorf("expected readonly database error, got: %v", err)
	}

	// 3. DROP should fail
	_, err = readQuery(ctx, state, "DROP TABLE users")
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

	state := &ServerState{
		DB:     db,
		Policy: NewDefaultPolicy(),
	}

	ctx := context.Background()
	tables, err := listTables(ctx, state)
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

	state := &ServerState{
		DB:     db,
		Policy: NewDefaultPolicy(),
	}

	ctx := context.Background()
	desc, err := describeTable(ctx, state, "users")
	if err != nil {
		t.Fatalf("describeTable failed: %v", err)
	}

	if !strings.Contains(desc, "email") || !strings.Contains(desc, "role") {
		t.Errorf("expected email and role in table description, got: %s", desc)
	}

	// Test invalid table name sanitization
	_, err = describeTable(ctx, state, "users; DROP TABLE users;")
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

	state := &ServerState{
		DB: safeDB,
		Policy: SecurityPolicy{
			MaxRowsPerQuery: 50,
			MaxSessionRows:  500,
			DenyColumns:     map[string]bool{},
		},
	}

	ctx := context.Background()
	out, err := readQuery(ctx, state, "SELECT n FROM numbers ORDER BY n ASC")
	if err != nil {
		t.Fatalf("readQuery failed: %v", err)
	}

	if !strings.Contains(out, "[NOTICE: Output truncated at 50 rows") {
		t.Error("expected output to contain truncation warning notice")
	}
}

func TestDenySensitiveColumns(t *testing.T) {
	dbPath := setupTestDB(t)

	db, err := openSafeDB(dbPath)
	if err != nil {
		t.Fatalf("openSafeDB failed: %v", err)
	}
	defer db.Close()

	state := &ServerState{
		DB:     db,
		Policy: NewDefaultPolicy(), // Denies password_hash, secret, api_key, etc.
	}

	ctx := context.Background()

	// 1. Explicitly selecting password_hash should be blocked
	_, err = readQuery(ctx, state, "SELECT id, password_hash FROM users")
	if err == nil {
		t.Fatal("expected reading sensitive column password_hash to be blocked")
	}
	if !strings.Contains(err.Error(), "password_hash") {
		t.Errorf("expected error mentioning sensitive column, got: %v", err)
	}

	// 2. Querying api_key in secret_audit should be blocked
	_, err = readQuery(ctx, state, "SELECT api_key FROM secret_audit")
	if err == nil {
		t.Fatal("expected reading sensitive column api_key to be blocked")
	}

	// 3. Querying non-sensitive columns should succeed
	res, err := readQuery(ctx, state, "SELECT id, email FROM users WHERE id = 1")
	if err != nil {
		t.Fatalf("expected non-sensitive query to succeed, got: %v", err)
	}
	if !strings.Contains(res, "alice@example.com") {
		t.Errorf("expected result to contain email, got: %s", res)
	}
}

func TestCumulativeSessionBudget(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "budget.db")

	rwDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open rw db: %v", err)
	}
	_, _ = rwDB.Exec("CREATE TABLE items (id INTEGER);")
	for i := 1; i <= 20; i++ {
		_, _ = rwDB.Exec("INSERT INTO items (id) VALUES (?)", i)
	}
	rwDB.Close()

	safeDB, err := openSafeDB(dbPath)
	if err != nil {
		t.Fatalf("openSafeDB failed: %v", err)
	}
	defer safeDB.Close()

	// Set session quota to 15 rows max
	state := &ServerState{
		DB: safeDB,
		Policy: SecurityPolicy{
			MaxRowsPerQuery: 10,
			MaxSessionRows:  15,
			DenyColumns:     map[string]bool{},
		},
	}

	ctx := context.Background()

	// First query pulls 10 rows (cumulative: 10)
	_, err = readQuery(ctx, state, "SELECT id FROM items LIMIT 10")
	if err != nil {
		t.Fatalf("query 1 failed: %v", err)
	}

	// Second query pulls 10 rows (cumulative: 20 -> trips quota)
	_, err = readQuery(ctx, state, "SELECT id FROM items LIMIT 10 OFFSET 10")
	if err != nil {
		t.Fatalf("query 2 failed: %v", err)
	}

	// Third query must be rejected because cumulative >= 15
	_, err = readQuery(ctx, state, "SELECT id FROM items LIMIT 1")
	if err == nil {
		t.Fatal("expected third query to be rejected due to session budget exhaustion")
	}
	if !strings.Contains(err.Error(), "session row budget exceeded") {
		t.Errorf("expected session budget error, got: %v", err)
	}
}

func TestWildcardDisallowed(t *testing.T) {
	dbPath := setupTestDB(t)

	db, err := openSafeDB(dbPath)
	if err != nil {
		t.Fatalf("openSafeDB failed: %v", err)
	}
	defer db.Close()

	state := &ServerState{
		DB: db,
		Policy: SecurityPolicy{
			MaxRowsPerQuery:  50,
			MaxSessionRows:   250,
			DisallowWildcard: true,
			DenyColumns:      map[string]bool{},
		},
	}

	ctx := context.Background()
	_, err = readQuery(ctx, state, "SELECT * FROM posts")
	if err == nil {
		t.Fatal("expected SELECT * to be rejected when DisallowWildcard is true")
	}
	if !strings.Contains(err.Error(), "wildcard 'SELECT *' is rejected") {
		t.Errorf("expected wildcard rejection error, got: %v", err)
	}
}

func TestTableAllowlist(t *testing.T) {
	dbPath := setupTestDB(t)

	db, err := openSafeDB(dbPath)
	if err != nil {
		t.Fatalf("openSafeDB failed: %v", err)
	}
	defer db.Close()

	state := &ServerState{
		DB: db,
		Policy: SecurityPolicy{
			MaxRowsPerQuery: 50,
			MaxSessionRows:  250,
			AllowTables: map[string]bool{
				"posts": true,
			},
			DenyColumns: map[string]bool{},
		},
	}

	ctx := context.Background()

	// 1. listTables should only return 'posts'
	tables, err := listTables(ctx, state)
	if err != nil {
		t.Fatalf("listTables failed: %v", err)
	}
	if strings.Contains(tables, "users") {
		t.Errorf("expected users to be excluded from list_tables, got: %s", tables)
	}
	if !strings.Contains(tables, "posts") {
		t.Errorf("expected posts in list_tables, got: %s", tables)
	}

	// 2. describeTable on users should be rejected
	_, err = describeTable(ctx, state, "users")
	if err == nil {
		t.Fatal("expected describeTable on unapproved table to fail")
	}

	// 3. Direct catalog inspection should be blocked
	_, err = readQuery(ctx, state, "SELECT * FROM sqlite_master")
	if err == nil {
		t.Fatal("expected direct sqlite_master catalog inspection to be blocked")
	}
}

func init() {
	// Quiet logger during test runs
	os.Stderr, _ = os.Open(os.DevNull)
}
