# safe-sqlite-mcp-go

[![Go Reference](https://pkg.go.dev/badge/github.com/raza-basit/safe-sqlite-mcp-go.svg)](https://pkg.go.dev/github.com/raza-basit/safe-sqlite-mcp-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/raza-basit/safe-sqlite-mcp-go/actions/workflows/ci.yml/badge.svg)](https://github.com/raza-basit/safe-sqlite-mcp-go/actions)

A high-performance, single-binary [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server in Go that exposes SQLite databases to AI agents with **immutable database guardrails and exfiltration defenses**.

> 📖 **Read the full architectural breakdown**: [Building a Safe SQLite MCP Server in Go — Why Agents Shouldn't Have Raw Database Access](https://raza.build/blog/safe-sqlite-mcp-server-go) on [raza.build](https://raza.build).

---

## The Problem

Giving an autonomous AI agent (Claude, Cursor, Copilot) raw database or shell access is an operational liability:
* **Accidental Mutations**: Hallucinated `UPDATE`, `DELETE`, or `DROP TABLE` statements can destroy data in milliseconds.
* **Context Window Overflows**: Running unindexed `SELECT *` queries pulls tens of thousands of rows into context, exhausting token limits and racking up API costs.
* **Silent Exfiltration & Pagination Scraping**: Even in 100% read-only mode, an agent can dump sensitive columns (`password_hash`, `api_token`) or loop through an entire database 50 rows at a time (`LIMIT 50 OFFSET 0, 50, 100...`).
* **Runaway Queries**: Heavy joins or full-table scans can peg host CPU without deadlines.

`safe-sqlite-mcp-go` sits as a **secure boundary layer** enforcing defense-in-depth between the LLM and your SQLite database.

---

## Key Features & Safety Invariants

| Guardrail | Implementation Mechanism |
| :--- | :--- |
| **Connection Immutability** | Opened with `file:path?mode=ro`, driver-level connection hooks, and `PRAGMA query_only = ON; PRAGMA busy_timeout = 5000;`. |
| **Context Window Protection** | Results are capped at **50 rows** per query by default, with truncation notices guiding the LLM to refine predicates. |
| **Cumulative Session Row Budgets** | Enforces a strict sliding session-wide quota (default: 250 rows) clamped so cumulative rows never breach the limit. |
| **Sensitive Column Denylisting** | Pure-Go lexical analyzer rejects queries referencing forbidden columns anywhere (projections, aliases, expressions, WHERE, ORDER BY). |
| **Table Allowlists** | Restricts table discovery (`list_tables`, `describe_table`) and query execution (`read_query`) strictly to approved tables. |
| **Query Shape Enforcement** | Optional `--disallow-wildcard` flag to reject wildcard `SELECT *` and `u.*` while permitting arithmetic multiplication (`a * b`). |
| **Configurable Timeouts** | Bound to `context.WithTimeout` (default 2s, configurable via `--query-timeout`). |
| **Zero Cgo** | Built with [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite) for clean, pure-Go cross-compilation (`CGO_ENABLED=0`). |
| **Minimal Footprint** | Compiles to a single static ~12MB binary. Sub-2ms startup time and <8MB RSS memory. |
| **Clean Stdio Framing** | Debug and error logs are isolated strictly to `os.Stderr`, guaranteeing zero JSON-RPC framing corruption on `os.Stdout`. |

---

## Installation

### Option 1: Using `go install` (Recommended)

Ensure `$GOPATH/bin` is in your system `$PATH`:

```bash
go install github.com/raza-basit/safe-sqlite-mcp-go@latest
```

### Option 2: Clone and Build from Source

```bash
git clone https://github.com/raza-basit/safe-sqlite-mcp-go.git
cd safe-sqlite-mcp-go
go build -o safe-sqlite-mcp-go .
```

---

## Configuration & Flags

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--db` | *required* | Path to the SQLite database file |
| `--max-rows` | `50` | Maximum rows returned per individual query |
| `--max-session-rows` | `250` | Maximum cumulative rows across a session (0 to disable) |
| `--deny-columns` | `password,password_hash,secret,api_key,token,ssn,credit_card` | Comma-separated list of forbidden column names |
| `--allow-tables` | `""` | Comma-separated list of permitted tables (empty allows all non-system tables) |
| `--disallow-wildcard` | `false` | Reject queries containing wildcard `SELECT *` |
| `--query-timeout` | `2s` | Maximum execution time per database query |

---

## Client Setup

### Claude Desktop

Edit your Claude Desktop configuration file:
* **macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
* **Windows**: `%APPDATA%\Claude\claude_desktop_config.json`

Add the server under `mcpServers`:

```json
{
  "mcpServers": {
    "sqlite-db": {
      "command": "safe-sqlite-mcp-go",
      "args": [
        "--db", "/absolute/path/to/your/database.db",
        "--max-session-rows", "250",
        "--disallow-wildcard"
      ]
    }
  }
}
```

### Cursor / Windsurf

Add a new MCP server in settings:
* **Type**: `command`
* **Command**: `safe-sqlite-mcp-go --db /absolute/path/to/your/database.db --max-session-rows 250`

---

## Available Tools

The server registers three tools during the `tools/list` capability handshake:

1. `list_tables`: Enumerates approved user tables in the database catalog.
2. `describe_table`: Fetches column names, data types, nullability, default values, and primary key indicators.
3. `read_query`: Executes a safe `SELECT` query with timeout protection, automated row capping, and column safety checks.

---

## CLI Testing

You can test the server directly via standard Unix piping:

```bash
# 1. Initialize Handshake
printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n' | safe-sqlite-mcp-go --db test.db

# 2. List Tables
printf '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_tables","arguments":{}}}\n' | safe-sqlite-mcp-go --db test.db

# 3. Test Mutation Rejection
printf '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_query","arguments":{"query":"DROP TABLE users;"}}}\n' | safe-sqlite-mcp-go --db test.db

# 4. Test Sensitive Column Rejection
printf '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_query","arguments":{"query":"SELECT password_hash FROM users;"}}}\n' | safe-sqlite-mcp-go --db test.db
```

Output:
```json
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"query execution failed: attempt to write a readonly database"}],"isError":true}}
```

---

## License

MIT © [Raza Basit](https://raza.build)
