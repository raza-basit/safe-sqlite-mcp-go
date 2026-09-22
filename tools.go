package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
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
		Description: "List all user tables in the SQLite database.",
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

// listTables queries the sqlite_master catalog for all non-system tables.
func listTables(ctx context.Context, state *ServerState) (string, error) {
	query := `
		SELECT name 
		FROM sqlite_master 
		WHERE type='table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name ASC;
	`
	rows, err := state.DB.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
		if len(state.Policy.AllowTables) > 0 && !state.Policy.AllowTables[strings.ToLower(name)] {
			continue
		}
		tables = append(tables, name)
	}

	if len(tables) == 0 {
		return "Database contains no accessible user tables.", nil
	}

	return strings.Join(tables, "\n"), nil
}

// describeTable retrieves column-level metadata using PRAGMA table_info.
func describeTable(ctx context.Context, state *ServerState, tableName string) (string, error) {
	// Strict identifier sanitization
	for _, ch := range tableName {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_') {
			return "", fmt.Errorf("invalid table name: %q", tableName)
		}
	}

	if len(state.Policy.AllowTables) > 0 && !state.Policy.AllowTables[strings.ToLower(tableName)] {
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
		builder.WriteString(fmt.Sprintf("%d | %s | %s | %d | %s | %d\n",
			cid, name, colType, notnull, dfltValue.String, pk))
	}

	if !found {
		return "", fmt.Errorf("table %q not found", tableName)
	}

	return builder.String(), nil
}

// readQuery executes an arbitrary read-only query and formats the output as JSON, capped by policy limits.
func readQuery(ctx context.Context, state *ServerState, query string) (string, error) {
	trimmed := strings.TrimSpace(query)
	upperQuery := strings.ToUpper(trimmed)

	// 1. Wildcard query shape check
	if state.Policy.DisallowWildcard {
		if strings.Contains(upperQuery, "SELECT *") || strings.Contains(upperQuery, "SELECT\n*") || strings.Contains(upperQuery, "SELECT\t*") {
			return "", fmt.Errorf("wildcard 'SELECT *' is rejected by security policy; explicit column projections required")
		}
	}

	// 2. Cumulative session row budget check
	if state.Policy.MaxSessionRows > 0 {
		current := state.CumulativeRows.Load()
		if current >= state.Policy.MaxSessionRows {
			return "", fmt.Errorf("session row budget exceeded (%d/%d cumulative rows fetched); re-authorization required to prevent automated exfiltration", current, state.Policy.MaxSessionRows)
		}
	}

	// 3. System catalog snooping prevention if table allowlist is active
	if len(state.Policy.AllowTables) > 0 {
		if strings.Contains(upperQuery, "SQLITE_MASTER") || strings.Contains(upperQuery, "SQLITE_SCHEMA") {
			return "", fmt.Errorf("direct catalog inspection is blocked by security policy; use list_tables instead")
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

	// 4. Sensitive Column Denylist Check (Column Shape Validation)
	for _, col := range cols {
		colLower := strings.ToLower(strings.TrimSpace(col))
		if state.Policy.DenyColumns[colLower] {
			return "", fmt.Errorf("access to sensitive column %q is blocked by security policy", col)
		}
	}

	var results []map[string]any
	count := 0
	truncated := false
	maxRows := state.Policy.MaxRowsPerQuery
	if maxRows <= 0 {
		maxRows = 50
	}

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
		for i, col := range cols {
			val := values[i]
			if b, ok := val.([]byte); ok {
				rowMap[col] = string(b)
			} else {
				rowMap[col] = val
			}
		}
		results = append(results, rowMap)
		count++
	}

	// Record session accounting
	newTotal := state.CumulativeRows.Add(int64(count))

	jsonBytes, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", err
	}

	output := string(jsonBytes)
	if truncated {
		output += fmt.Sprintf("\n\n[NOTICE: Output truncated at %d rows to protect context limits. Add a specific WHERE clause or LIMIT to refine.]", maxRows)
	}
	if state.Policy.MaxSessionRows > 0 {
		output += fmt.Sprintf("\n[SESSION BUDGET: %d/%d cumulative rows fetched]", newTotal, state.Policy.MaxSessionRows)
	}

	return output, nil
}
