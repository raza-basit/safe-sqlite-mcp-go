package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// SecurityPolicy defines guardrails for mitigating data exfiltration, runaway queries, and query drift.
type SecurityPolicy struct {
	MaxRowsPerQuery  int             // Max rows per single query (default 50)
	MaxSessionRows   int64           // Max cumulative rows across the entire session (0 = disabled)
	DenyColumns      map[string]bool // Forbidden column names (case-insensitive)
	AllowTables      map[string]bool // If non-empty, only these tables can be accessed
	DisallowWildcard bool            // If true, reject 'SELECT *' queries
}

// ServerState encapsulates the database connection, security policy, and session accounting.
type ServerState struct {
	DB             *sql.DB
	Policy         SecurityPolicy
	CumulativeRows atomic.Int64
	Timeout        time.Duration
}

// NewDefaultPolicy creates a standard production security policy.
func NewDefaultPolicy() SecurityPolicy {
	deny := []string{"password", "password_hash", "secret", "api_key", "token", "ssn", "credit_card"}
	denyMap := make(map[string]bool)
	for _, c := range deny {
		denyMap[strings.ToLower(strings.TrimSpace(c))] = true
	}
	return SecurityPolicy{
		MaxRowsPerQuery:  50,
		MaxSessionRows:   250,
		DenyColumns:      denyMap,
		AllowTables:      make(map[string]bool),
		DisallowWildcard: false,
	}
}

// toolsList defines the tools registered with the MCP client.
var toolsList = []Tool{
	{
		Name:        "list_tables",
		Description: "List all user tables and views in the SQLite database.",
		InputSchema: InputSchema{
			Type:       "object",
			Properties: map[string]PropertyDef{},
		},
	},
	{
		Name:        "describe_table",
		Description: "Retrieve schema information (columns, types, nullability, primary key) for a specific table.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]PropertyDef{
				"table": {
					Type:        "string",
					Description: "The name of the table to inspect.",
				},
			},
			Required: []string{"table"},
		},
	},
	{
		Name:        "read_query",
		Description: "Execute a read-only SQL SELECT query against the SQLite database. Writes, sensitive columns, and queries exceeding session row quotas are rejected.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]PropertyDef{
				"query": {
					Type:        "string",
					Description: "The SQL SELECT statement to execute.",
				},
			},
			Required: []string{"query"},
		},
	},
}

// getDatabaseTables fetches all accessible tables and views from sqlite_master.
func getDatabaseTables(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	query := `
		SELECT name 
		FROM sqlite_master 
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%';
	`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables[strings.ToLower(name)] = true
	}
	return tables, nil
}

// listTables queries the sqlite_master catalog for all non-system tables and views.
func listTables(ctx context.Context, state *ServerState) (string, error) {
	query := `
		SELECT name, type 
		FROM sqlite_master 
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'
		ORDER BY name ASC;
	`
	rows, err := state.DB.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var items []string
	for rows.Next() {
		var name, itemType string
		if err := rows.Scan(&name, &itemType); err != nil {
			return "", err
		}
		if len(state.Policy.AllowTables) > 0 && !state.Policy.AllowTables[strings.ToLower(name)] {
			continue
		}
		if itemType == "view" {
			items = append(items, fmt.Sprintf("%s (view)", name))
		} else {
			items = append(items, name)
		}
	}

	if len(items) == 0 {
		return "Database contains no accessible user tables.", nil
	}

	return strings.Join(items, "\n"), nil
}

// describeTable retrieves column-level metadata using PRAGMA table_info.
func describeTable(ctx context.Context, state *ServerState, tableName string) (string, error) {
	tableName = strings.TrimSpace(tableName)
	if tableName == "" {
		return "", fmt.Errorf("table name cannot be empty")
	}

	// Strict identifier sanitization
	for _, ch := range tableName {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_') {
			return "", fmt.Errorf("invalid table name: %q", tableName)
		}
	}

	lowerTable := strings.ToLower(tableName)
	if strings.HasPrefix(lowerTable, "sqlite_") {
		return "", fmt.Errorf("system table %q cannot be described", tableName)
	}

	if len(state.Policy.AllowTables) > 0 && !state.Policy.AllowTables[lowerTable] {
		return "", fmt.Errorf("table %q is not in the allowed schema policy", tableName)
	}

	rows, err := state.DB.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s);", tableName))
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Schema for table %s:\n", tableName))
	builder.WriteString("cid | name | type | notnull | dflt_value | pk\n")
	builder.WriteString("----+------+------+---------+------------+---\n")

	var found bool
	for rows.Next() {
		found = true
		var cid, notnull, pk int
		var name, colType string
		var dfltValue sql.NullString

		if err := rows.Scan(&cid, &name, &colType, &notnull, &dfltValue, &pk); err != nil {
			return "", err
		}

		dfltStr := dfltValue.String
		if state.Policy.DenyColumns[strings.ToLower(name)] {
			dfltStr = "[REDACTED]"
		}

		builder.WriteString(fmt.Sprintf("%d | %s | %s | %d | %s | %d\n",
			cid, name, colType, notnull, dfltStr, pk))
	}

	if !found {
		return "", fmt.Errorf("table %q not found", tableName)
	}

	return builder.String(), nil
}

// readQuery executes an arbitrary read-only query and formats the output as JSON, capped by policy limits.
func readQuery(ctx context.Context, state *ServerState, query string) (string, error) {
	// 1. Fetch accessible tables and enforce full lexical security analysis
	knownTables, err := getDatabaseTables(ctx, state.DB)
	if err != nil {
		return "", fmt.Errorf("failed to inspect database catalog: %w", err)
	}

	if err := ValidateQuery(query, state.Policy, knownTables); err != nil {
		return "", err
	}

	// 2. Strict cumulative session row budget check and limit clamping
	maxRows := state.Policy.MaxRowsPerQuery
	if maxRows <= 0 {
		maxRows = 50
	}

	sessionBudgetCapped := false
	if state.Policy.MaxSessionRows > 0 {
		current := state.CumulativeRows.Load()
		if current >= state.Policy.MaxSessionRows {
			return "", fmt.Errorf("session row budget exceeded (%d/%d cumulative rows fetched); re-authorization required to prevent automated exfiltration", current, state.Policy.MaxSessionRows)
		}
		remaining := state.Policy.MaxSessionRows - current
		if int64(maxRows) > remaining {
			maxRows = int(remaining)
			sessionBudgetCapped = true
		}
	}

	rows, err := state.DB.QueryContext(ctx, query)
	if err != nil {
		return "", fmt.Errorf("query execution failed: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}

	// 3. Sensitive Column Denylist Check (Layer 2 runtime defense-in-depth)
	for _, col := range cols {
		colLower := strings.ToLower(strings.TrimSpace(col))
		if state.Policy.DenyColumns[colLower] {
			return "", fmt.Errorf("access to sensitive column %q is blocked by security policy", col)
		}
	}

	// Disambiguate duplicate column names if present
	seenCols := make(map[string]int, len(cols))
	colNames := make([]string, len(cols))
	for i, c := range cols {
		idx := seenCols[c]
		seenCols[c]++
		if idx == 0 {
			colNames[i] = c
		} else {
			colNames[i] = fmt.Sprintf("%s:%d", c, idx+1)
		}
	}

	results := make([]map[string]any, 0)
	count := 0
	truncated := false

	for rows.Next() {
		if count >= maxRows {
			truncated = true
			break
		}

		values := make([]any, len(cols))
		valuePtrs := make([]any, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return "", err
		}

		rowMap := make(map[string]any, len(cols))
		for i, colName := range colNames {
			val := values[i]
			if b, ok := val.([]byte); ok {
				if utf8.Valid(b) {
					rowMap[colName] = string(b)
				} else {
					rowMap[colName] = fmt.Sprintf("[BLOB: %d bytes]", len(b))
				}
			} else {
				rowMap[colName] = val
			}
		}
		results = append(results, rowMap)
		count++
	}

	// Record session accounting atomically
	newTotal := state.CumulativeRows.Add(int64(count))

	jsonBytes, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", err
	}

	output := string(jsonBytes)
	if truncated {
		if sessionBudgetCapped {
			output += fmt.Sprintf("\n\n[NOTICE: Output truncated at %d rows because session row budget (%d) was reached.]", maxRows, state.Policy.MaxSessionRows)
		} else {
			output += fmt.Sprintf("\n\n[NOTICE: Output truncated at %d rows to protect context limits. Add a specific WHERE clause or LIMIT to refine.]", maxRows)
		}
	}
	if state.Policy.MaxSessionRows > 0 {
		output += fmt.Sprintf("\n[SESSION BUDGET: %d/%d cumulative rows fetched]", newTotal, state.Policy.MaxSessionRows)
	}

	return output, nil
}
