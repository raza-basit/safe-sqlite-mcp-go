package main

import (
	"context"
	"database/sql"
	"encoding/json"
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
			role TEXT DEFAULT 'member',
			avatar BLOB
		);
		INSERT INTO users (id, name, email, password_hash, role, avatar) VALUES 
			(1, 'Alice Smith', 'alice@example.com', 'hash_123', 'admin', X'00010203DEADBEEF'),
			(2, 'Bob Jones', 'bob@example.com', 'hash_456', 'member', NULL);

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

		CREATE VIEW active_users AS 
			SELECT id, name, role FROM users WHERE role = 'admin';
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

	// 2. Direct DB write attempt must fail due to mode=ro and PRAGMA query_only
	_, err = state.DB.ExecContext(ctx, "INSERT INTO users (id, name) VALUES (3, 'Mallory')")
	if err == nil {
		t.Fatal("expected direct DB INSERT to fail on read-only database, but it succeeded")
	}
	if !strings.Contains(err.Error(), "readonly database") {
		t.Errorf("expected readonly database error from engine, got: %v", err)
	}

	// 3. Tool-level INSERT must be rejected by query validator
	_, err = readQuery(ctx, state, "INSERT INTO users (id, name) VALUES (3, 'Mallory')")
	if err == nil {
		t.Fatal("expected tool-level INSERT to be rejected by query validator")
	}
	if !strings.Contains(err.Error(), "only read-only SELECT queries are permitted") {
		t.Errorf("expected validation rejection, got: %v", err)
	}

	// 4. Tool-level DROP must be rejected by query validator
	_, err = readQuery(ctx, state, "DROP TABLE users")
	if err == nil {
		t.Fatal("expected tool-level DROP to be rejected by query validator")
	}
	if !strings.Contains(err.Error(), "only read-only SELECT queries are permitted") {
		t.Errorf("expected validation rejection, got: %v", err)
	}
}

func TestListTablesAndViews(t *testing.T) {
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

	if !strings.Contains(tables, "active_users (view)") {
		t.Errorf("expected active_users view in list_tables output, got: %s", tables)
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

	// Verify sensitive column default value redaction
	if !strings.Contains(desc, "[REDACTED]") {
		t.Errorf("expected password_hash default value to be [REDACTED], got:\n%s", desc)
	}

	// Test invalid table name sanitization
	_, err = describeTable(ctx, state, "users; DROP TABLE users;")
	if err == nil {
		t.Fatal("expected injection attempt in table name to be rejected")
	}

	// System tables should be rejected from describe
	_, err = describeTable(ctx, state, "sqlite_master")
	if err == nil {
		t.Fatal("expected describeTable on sqlite_master to be rejected")
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

func TestDenySensitiveColumnsComprehensive(t *testing.T) {
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

	// 1. Direct selection should be blocked
	_, err = readQuery(ctx, state, "SELECT id, password_hash FROM users")
	if err == nil || !strings.Contains(err.Error(), "password_hash") {
		t.Fatalf("expected direct sensitive column query to be blocked, got err: %v", err)
	}

	// 2. Column Aliasing bypass attempt: SELECT password_hash AS innocent
	_, err = readQuery(ctx, state, "SELECT password_hash AS innocent FROM users")
	if err == nil || !strings.Contains(err.Error(), "password_hash") {
		t.Fatalf("expected column aliasing bypass to be blocked, got err: %v", err)
	}

	// 3. Expression / Substring bypass attempt: SELECT substr(password_hash, 1, 4)
	_, err = readQuery(ctx, state, "SELECT substr(password_hash, 1, 4) FROM users")
	if err == nil || !strings.Contains(err.Error(), "password_hash") {
		t.Fatalf("expected expression bypass to be blocked, got err: %v", err)
	}

	// 4. Blind WHERE clause exfiltration: WHERE password_hash LIKE 'a%'
	_, err = readQuery(ctx, state, "SELECT id, name FROM users WHERE password_hash = 'hash_123'")
	if err == nil || !strings.Contains(err.Error(), "password_hash") {
		t.Fatalf("expected WHERE clause sensitive column exfiltration to be blocked, got err: %v", err)
	}

	// 5. ORDER BY sensitive column
	_, err = readQuery(ctx, state, "SELECT id, name FROM users ORDER BY password_hash")
	if err == nil || !strings.Contains(err.Error(), "password_hash") {
		t.Fatalf("expected ORDER BY sensitive column to be blocked, got err: %v", err)
	}

	// 6. Subquery referencing sensitive column
	_, err = readQuery(ctx, state, "SELECT title FROM posts WHERE user_id IN (SELECT id FROM users WHERE password_hash IS NOT NULL)")
	if err == nil || !strings.Contains(err.Error(), "password_hash") {
		t.Fatalf("expected subquery sensitive column to be blocked, got err: %v", err)
	}

	// 7. Bracketed and quoted identifiers
	_, err = readQuery(ctx, state, "SELECT [password_hash] FROM users")
	if err == nil || !strings.Contains(err.Error(), "password_hash") {
		t.Fatalf("expected bracketed identifier to be blocked, got err: %v", err)
	}
	_, err = readQuery(ctx, state, `SELECT "password_hash" FROM users`)
	if err == nil || !strings.Contains(err.Error(), "password_hash") {
		t.Fatalf("expected double-quoted identifier to be blocked, got err: %v", err)
	}

	// 8. String literal that happens to match denied column name must SUCCEED (not a column)
	res, err := readQuery(ctx, state, "SELECT id, name FROM users WHERE role = 'password_hash'")
	if err != nil {
		t.Fatalf("expected string literal 'password_hash' to succeed, got: %v", err)
	}
	if !strings.Contains(res, "[]") {
		t.Errorf("expected empty array, got: %s", res)
	}
}

func TestTableAllowlistComprehensive(t *testing.T) {
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

	// 3. readQuery on unapproved table 'users' directly MUST FAIL
	_, err = readQuery(ctx, state, "SELECT id, name FROM users")
	if err == nil {
		t.Fatal("expected readQuery on unapproved table 'users' to fail")
	}
	if !strings.Contains(err.Error(), "access to table \"users\" is blocked") {
		t.Errorf("expected table policy rejection error, got: %v", err)
	}

	// 4. readQuery on unapproved table 'secret_audit' MUST FAIL
	_, err = readQuery(ctx, state, "SELECT id, api_key FROM secret_audit")
	if err == nil {
		t.Fatal("expected readQuery on unapproved table 'secret_audit' to fail")
	}

	// 5. JOIN with unapproved table MUST FAIL
	_, err = readQuery(ctx, state, "SELECT posts.id, users.name FROM posts JOIN users ON posts.user_id = users.id")
	if err == nil {
		t.Fatal("expected JOIN with unapproved table to fail")
	}

	// 6. Subquery with unapproved table MUST FAIL
	_, err = readQuery(ctx, state, "SELECT title FROM posts WHERE user_id IN (SELECT id FROM users)")
	if err == nil {
		t.Fatal("expected subquery with unapproved table to fail")
	}

	// 7. System catalog query MUST FAIL
	_, err = readQuery(ctx, state, "SELECT name FROM sqlite_master")
	if err == nil {
		t.Fatal("expected direct sqlite_master catalog inspection to be blocked")
	}

	// 8. Approved table 'posts' MUST SUCCEED
	res, err := readQuery(ctx, state, "SELECT id, title FROM posts")
	if err != nil {
		t.Fatalf("expected approved table query to succeed, got: %v", err)
	}
	if !strings.Contains(res, "First Post") {
		t.Errorf("expected result to contain First Post, got: %s", res)
	}
}

func TestWildcardDisallowedVariants(t *testing.T) {
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

	// 1. Standard SELECT *
	_, err = readQuery(ctx, state, "SELECT * FROM posts")
	if err == nil || !strings.Contains(err.Error(), "wildcard 'SELECT *'") {
		t.Fatalf("expected SELECT * to be rejected, got: %v", err)
	}

	// 2. SELECT with extra spaces
	_, err = readQuery(ctx, state, "SELECT    * FROM posts")
	if err == nil || !strings.Contains(err.Error(), "wildcard 'SELECT *'") {
		t.Fatalf("expected SELECT   * to be rejected, got: %v", err)
	}

	// 3. Qualified wildcard: SELECT p.*
	_, err = readQuery(ctx, state, "SELECT p.* FROM posts p")
	if err == nil || !strings.Contains(err.Error(), "wildcard 'SELECT *'") {
		t.Fatalf("expected SELECT p.* to be rejected, got: %v", err)
	}

	// 4. Multi-column projection: SELECT id, *
	_, err = readQuery(ctx, state, "SELECT id, * FROM posts")
	if err == nil || !strings.Contains(err.Error(), "wildcard 'SELECT *'") {
		t.Fatalf("expected SELECT id, * to be rejected, got: %v", err)
	}

	// 5. SELECT DISTINCT *
	_, err = readQuery(ctx, state, "SELECT DISTINCT * FROM posts")
	if err == nil || !strings.Contains(err.Error(), "wildcard 'SELECT *'") {
		t.Fatalf("expected SELECT DISTINCT * to be rejected, got: %v", err)
	}

	// 6. Multiplication MUST SUCCEED (not a projection wildcard)
	res, err := readQuery(ctx, state, "SELECT id * 2 AS double_id FROM posts")
	if err != nil {
		t.Fatalf("expected multiplication to succeed, got: %v", err)
	}
	if !strings.Contains(res, "double_id") {
		t.Errorf("expected result to contain double_id, got: %s", res)
	}
}

func TestStrictCumulativeSessionBudget(t *testing.T) {
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

	// Set session quota to 15 rows max, max rows per query to 10
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
	if state.CumulativeRows.Load() != 10 {
		t.Fatalf("expected cumulative rows to be 10, got: %d", state.CumulativeRows.Load())
	}

	// Second query requests 10 rows, but remaining budget is 5!
	// It MUST be clamped to 5 rows so total never breaches 15!
	out, err := readQuery(ctx, state, "SELECT id FROM items LIMIT 10 OFFSET 10")
	if err != nil {
		t.Fatalf("query 2 failed: %v", err)
	}
	if state.CumulativeRows.Load() != 15 {
		t.Fatalf("expected cumulative rows to strictly clamp at 15, got: %d", state.CumulativeRows.Load())
	}
	if !strings.Contains(out, "Output truncated at 5 rows because session row budget (15) was reached") {
		t.Errorf("expected session budget truncation notice, got: %s", out)
	}

	// Third query must be rejected immediately because cumulative >= 15
	_, err = readQuery(ctx, state, "SELECT id FROM items LIMIT 1")
	if err == nil {
		t.Fatal("expected third query to be rejected due to session budget exhaustion")
	}
	if !strings.Contains(err.Error(), "session row budget exceeded") {
		t.Errorf("expected session budget error, got: %v", err)
	}
}

func TestMultiStatementAndAdminRejection(t *testing.T) {
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

	// 1. Multi-statement query attempt
	_, err = readQuery(ctx, state, "SELECT id FROM users; DROP TABLE users;")
	if err == nil || !strings.Contains(err.Error(), "multiple SQL statements are not permitted") {
		t.Fatalf("expected multi-statement query to be rejected, got: %v", err)
	}

	// 2. PRAGMA attempt in read_query
	_, err = readQuery(ctx, state, "PRAGMA table_info(users);")
	if err == nil || !strings.Contains(err.Error(), "only read-only SELECT queries are permitted") {
		t.Fatalf("expected PRAGMA query to be rejected, got: %v", err)
	}

	// 3. ATTACH database attempt
	_, err = readQuery(ctx, state, "ATTACH DATABASE 'other.db' AS other;")
	if err == nil || !strings.Contains(err.Error(), "only read-only SELECT queries are permitted") {
		t.Fatalf("expected ATTACH to be rejected, got: %v", err)
	}
}

func TestBLOBAndDuplicateColumns(t *testing.T) {
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
			DenyColumns:     map[string]bool{},
		},
	}

	ctx := context.Background()

	// 1. BLOB column formatting
	out, err := readQuery(ctx, state, "SELECT id, avatar FROM users WHERE id = 1")
	if err != nil {
		t.Fatalf("readQuery failed: %v", err)
	}
	if !strings.Contains(out, "[BLOB: 8 bytes]") {
		t.Errorf("expected BLOB format '[BLOB: 8 bytes]', got: %s", out)
	}

	// 2. Duplicate column disambiguation: users.id and posts.id
	out, err = readQuery(ctx, state, "SELECT users.id, posts.id FROM users JOIN posts ON users.id = posts.user_id")
	if err != nil {
		t.Fatalf("readQuery with duplicate cols failed: %v", err)
	}
	if !strings.Contains(out, `"id": 1`) || !strings.Contains(out, `"id:2": 1`) {
		t.Errorf("expected disambiguated duplicate columns id and id:2, got:\n%s", out)
	}
}

func TestProtocolDispatch(t *testing.T) {
	dbPath := setupTestDB(t)

	db, err := openSafeDB(dbPath)
	if err != nil {
		t.Fatalf("openSafeDB failed: %v", err)
	}
	defer db.Close()

	state := &ServerState{
		DB:      db,
		Policy:  NewDefaultPolicy(),
		Timeout: 2 * time.Second,
	}

	// Test describe_table missing table param
	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"describe_table","arguments":{}}`),
	}
	// Capture handleRequest execution without crash
	handleRequest(req, state)

	// Test read_query missing query param
	req = &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"read_query","arguments":{}}`),
	}
	handleRequest(req, state)

	// Test ping method
	req = &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "ping",
	}
	handleRequest(req, state)
}
