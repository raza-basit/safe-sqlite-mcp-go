# safe-sqlite-mcp-go

[![Go Reference](https://pkg.go.dev/badge/github.com/raza-basit/safe-sqlite-mcp-go.svg)](https://pkg.go.dev/github.com/raza-basit/safe-sqlite-mcp-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/raza-basit/safe-sqlite-mcp-go/actions/workflows/ci.yml/badge.svg)](https://github.com/raza-basit/safe-sqlite-mcp-go/actions)

A high-performance, single-binary [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server in Go that exposes SQLite databases to AI agents with **immutable, connection-level safety guardrails**.

> 📖 **Read the full architectural breakdown**: [Building a Safe SQLite MCP Server in Go — Why Agents Shouldn't Have Raw Database Access](https://raza.build/blog/safe-sqlite-mcp-server-go) on [raza.build](https://raza.build).

---

## The Problem

Giving an autonomous AI agent (Claude, Cursor, Copilot) raw shell or database access is an operational liability:
* **Accidental Mutations**: A hallucinated `UPDATE`, `DELETE`, or `DROP TABLE` can wipe out records in milliseconds.
* **Context Window Overflows**: Running an unindexed `SELECT * FROM events` can pull hundreds of thousands of rows into the LLM context, exhausting token limits and racking up API costs.
* **Runaway Queries**: Heavy joins or full-table scans can peg host CPU without deadlines.

`safe-sqlite-mcp-go` sits as a **secure boundary layer** between the LLM and your SQLite database.

---

## Key Features & Safety Invariants

| Guardrail | Implementation Mechanism |
| :--- | :--- |
| **Connection Immutability** | Opened with `file:path?mode=ro` and `PRAGMA query_only = ON`. Any write attempt is rejected by the database engine itself. |
| **Context Window Protection** | Results are capped at **50 rows** by default. Includes explicit truncation notices guiding the LLM to refine queries. |
| **Strict Timeouts** | Every query is bound to a `context.WithTimeout(2*time.Second)`. |
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
      "args": ["--db", "/absolute/path/to/your/database.db"]
    }
  }
}
```

### Cursor / Windsurf

Add a new MCP server in settings:
* **Type**: `command`
* **Command**: `safe-sqlite-mcp-go --db /absolute/path/to/your/database.db`

---

## Available Tools

The server registers three tools during the `tools/list` capability handshake:

1. `list_tables`: Enumerates user tables in the database catalog.
2. `describe_table`: Fetches column names, data types, nullability, default values, and primary key indicators.
3. `read_query`: Executes a safe `SELECT` query with timeout protection and automated row capping.

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
```

Output:
```json
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"query execution failed: attempt to write a readonly database"}],"isError":true}}
```

---

## License

MIT © [Raza Basit](https://raza.build)

