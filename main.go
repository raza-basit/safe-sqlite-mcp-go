package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"
)

func main() {
	dbPath := flag.String("db", "", "Path to the SQLite database file (required)")
	maxRows := flag.Int("max-rows", 50, "Maximum rows returned per individual query")
	maxSessionRows := flag.Int64("max-session-rows", 250, "Maximum cumulative rows returned in a session (0 to disable)")
	denyCols := flag.String("deny-columns", "password,password_hash,secret,api_key,token,ssn,credit_card", "Comma-separated list of forbidden column names")
	allowTables := flag.String("allow-tables", "", "Comma-separated list of permitted tables (empty allows all non-system tables)")
	disallowWildcard := flag.Bool("disallow-wildcard", false, "Reject queries containing wildcard 'SELECT *'")
	flag.Parse()

	// CRITICAL: Ensure all application logging goes strictly to stderr.
	// stdout is reserved exclusively for framed JSON-RPC 2.0 protocol messages.
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ltime | log.Lshortfile)

	if *dbPath == "" {
		log.Fatal("[FATAL] --db argument is required. Usage: safe-sqlite-mcp-go --db /path/to/database.db")
	}

	db, err := openSafeDB(*dbPath)
	if err != nil {
		log.Fatalf("[FATAL] %v", err)
	}
	defer db.Close()

	policy := NewDefaultPolicy()
	policy.MaxRowsPerQuery = *maxRows
	policy.MaxSessionRows = *maxSessionRows
	policy.DisallowWildcard = *disallowWildcard

	if *denyCols != "" {
		policy.DenyColumns = make(map[string]bool)
		for _, c := range strings.Split(*denyCols, ",") {
			c = strings.ToLower(strings.TrimSpace(c))
			if c != "" {
				policy.DenyColumns[c] = true
			}
		}
	}

	if *allowTables != "" {
		policy.AllowTables = make(map[string]bool)
		for _, t := range strings.Split(*allowTables, ",") {
			t = strings.ToLower(strings.TrimSpace(t))
			if t != "" {
				policy.AllowTables[t] = true
			}
		}
	}

	state := &ServerState{
		DB:     db,
		Policy: policy,
	}

	log.Printf("[INFO] Security policy active: max-rows=%d, max-session-rows=%d, disallow-wildcard=%v",
		policy.MaxRowsPerQuery, policy.MaxSessionRows, policy.DisallowWildcard)

	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("[ERROR] read error: %v", err)
			continue
		}

		if len(line) == 0 || line[0] == '\n' {
			continue
		}

		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			log.Printf("[ERROR] json parse error: %v", err)
			continue
		}

		handleRequest(&req, state)
	}
}

// handleRequest dispatches incoming JSON-RPC methods according to the MCP specification.
func handleRequest(req *JSONRPCRequest, state *ServerState) {
	switch req.Method {
	case "initialize":
		sendResponse(req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]string{
				"name":    "safe-sqlite-mcp-go",
				"version": "1.1.0",
			},
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
		})

	case "notifications/initialized":
		// Client acknowledgment notification - no response required

	case "tools/list":
		sendResponse(req.ID, map[string]any{
			"tools": toolsList,
		})

	case "tools/call":
		var params ToolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			sendError(req.ID, -32602, "Invalid params")
			return
		}

		// Enforce a strict 2-second timeout on all database operations
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		var resultText string
		var execErr error

		switch params.Name {
		case "list_tables":
			resultText, execErr = listTables(ctx, state)

		case "describe_table":
			var args struct {
				Table string `json:"table"`
			}
			_ = json.Unmarshal(params.Arguments, &args)
			resultText, execErr = describeTable(ctx, state, args.Table)

		case "read_query":
			var args struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(params.Arguments, &args)
			resultText, execErr = readQuery(ctx, state, args.Query)

		default:
			sendError(req.ID, -32601, fmt.Sprintf("Unknown tool: %s", params.Name))
			return
		}

		if execErr != nil {
			sendResponse(req.ID, ToolResult{
				IsError: true,
				Content: []ContentBlock{
					{Type: "text", Text: execErr.Error()},
				},
			})
			return
		}

		sendResponse(req.ID, ToolResult{
			Content: []ContentBlock{
				{Type: "text", Text: resultText},
			},
		})

	default:
		if len(req.ID) > 0 {
			sendError(req.ID, -32601, fmt.Sprintf("Method not found: %s", req.Method))
		}
	}
}
