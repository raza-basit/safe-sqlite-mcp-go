package main

import (
	"encoding/json"
	"os"
)

// JSONRPCRequest represents an incoming JSON-RPC 2.0 request or notification.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse represents an outgoing JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError represents standard JSON-RPC 2.0 error payload.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Tool represents an MCP tool definition advertised in tools/list.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema defines the JSON schema for tool arguments.
type InputSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]PropertyDef `json:"properties"`
	Required   []string               `json:"required,omitempty"`
}

// PropertyDef describes a single tool parameter.
type PropertyDef struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// ToolCallParams represents the parameters payload for tools/call.
type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult represents the response content for a tool execution.
type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is an individual item in the tool result content array.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// sendResponse writes a JSON-RPC 2.0 success response to stdout.
func sendResponse(id json.RawMessage, result any) {
	if len(id) == 0 || string(id) == "null" {
		return
	}
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	bytes, _ := json.Marshal(resp)
	os.Stdout.Write(append(bytes, '\n'))
}

// sendError writes a JSON-RPC 2.0 error response to stdout.
func sendError(id json.RawMessage, code int, msg string) {
	if len(id) == 0 || string(id) == "null" {
		return
	}
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	}
	bytes, _ := json.Marshal(resp)
	os.Stdout.Write(append(bytes, '\n'))
}
