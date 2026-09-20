package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// maxResultRows caps the query output to prevent overflowing LLM context windows.
const maxResultRows = 50

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
		Description: "Execute a read-only SQL SELECT query against the SQLite database. Any write attempt will be rejected.",
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
func listTables(ctx context.Context, db *sql.DB) (string, error) {
	query := `
		SELECT name 
		FROM sqlite_master 
		WHERE type='table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name ASC;
	`
	rows, err := db.QueryContext(ctx, query)
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
		tables = append(tables, name)
	}

	if len(tables) == 0 {
		return "Database contains no user tables.", nil
	}

	return strings.Join(tables, "\n"), nil
}

// describeTable retrieves column-level metadata using PRAGMA table_info.
func describeTable(ctx context.Context, db *sql.DB, tableName string) (string, error) {
	// Strict identifier sanitization
	for _, ch := range tableName {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_') {
			return "", fmt.Errorf("invalid table name: %q", tableName)
		}
	}

	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s);", tableName))
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

// readQuery executes an arbitrary read-only query and formats the output as JSON, capped at maxResultRows.
func readQuery(ctx context.Context, db *sql.DB, query string) (string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return "", fmt.Errorf("query execution failed: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}

	var results []map[string]any
	count := 0
	truncated := false

	for rows.Next() {
		if count >= maxResultRows {
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

	jsonBytes, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", err
	}

	output := string(jsonBytes)
	if truncated {
		output += fmt.Sprintf("\n\n[NOTICE: Output truncated at %d rows to protect context limits. Add a specific WHERE clause or LIMIT to refine.]", maxResultRows)
	}

	return output, nil
}
