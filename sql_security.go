package main

import (
	"fmt"
	"strings"
	"unicode"
)

// TokenType represents lexical token categories in SQLite statements.
type TokenType int

const (
	TokenKeyword TokenType = iota
	TokenIdentifier
	TokenStringLiteral
	TokenNumberLiteral
	TokenBlobLiteral
	TokenOperator
	TokenPunctuation
)

// Token represents a single lexical token.
type Token struct {
	Type  TokenType
	Value string // Normalized value (e.g. unquoted, lowercased for identifiers/keywords)
	Raw   string // Original raw text slice from query
}

// sqlKeywords contains SQLite language keywords.
var sqlKeywords = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "JOIN": true,
	"LEFT": true, "RIGHT": true, "FULL": true, "OUTER": true, "CROSS": true, "INNER": true, "NATURAL": true,
	"ON": true, "USING": true, "GROUP": true, "BY": true, "HAVING": true,
	"ORDER": true, "ASC": true, "DESC": true, "NULLS": true, "FIRST": true, "LAST": true,
	"LIMIT": true, "OFFSET": true, "WITH": true, "RECURSIVE": true, "AS": true,
	"UNION": true, "ALL": true, "INTERSECT": true, "EXCEPT": true, "DISTINCT": true,
	"BETWEEN": true, "IN": true, "IS": true, "NULL": true, "NOT": true,
	"LIKE": true, "GLOB": true, "MATCH": true, "REGEXP": true,
	"AND": true, "OR": true, "CASE": true, "WHEN": true, "THEN": true, "ELSE": true, "END": true,
	"CAST": true, "COLLATE": true, "ESCAPE": true, "EXISTS": true,
	"OVER": true, "PARTITION": true, "WINDOW": true, "FILTER": true,
	"EXPLAIN": true, "QUERY": true, "PLAN": true,
	// Prohibited operation keywords
	"INSERT": true, "UPDATE": true, "DELETE": true, "DROP": true,
	"CREATE": true, "ALTER": true, "REPLACE": true, "TRUNCATE": true,
	"PRAGMA": true, "ATTACH": true, "DETACH": true, "VACUUM": true, "REINDEX": true, "ANALYZE": true,
	"BEGIN": true, "COMMIT": true, "ROLLBACK": true, "SAVEPOINT": true, "RELEASE": true, "TRANSACTION": true,
	"TABLE": true, "INTO": true, "VALUES": true, "SET": true,
}

// systemCatalogs contains SQLite internal schema and metadata tables.
var systemCatalogs = map[string]bool{
	"sqlite_master":      true,
	"sqlite_schema":      true,
	"sqlite_temp_master": true,
	"sqlite_temp_schema": true,
	"sqlite_sequence":    true,
	"sqlite_stat1":       true,
	"sqlite_stat4":       true,
}

// TokenizeSQL performs lexical scanning of an SQLite query into tokens.
func TokenizeSQL(query string) ([]Token, error) {
	var tokens []Token
	runes := []rune(query)
	n := len(runes)
	i := 0

	for i < n {
		r := runes[i]

		// 1. Whitespace
		if unicode.IsSpace(r) {
			i++
			continue
		}

		// 2. Comments
		if r == '-' && i+1 < n && runes[i+1] == '-' {
			// Single line comment -- ...
			i += 2
			for i < n && runes[i] != '\n' {
				i++
			}
			continue
		}

		if r == '/' && i+1 < n && runes[i+1] == '*' {
			// Multi-line comment /* ... */
			i += 2
			closed := false
			for i+1 < n {
				if runes[i] == '*' && runes[i+1] == '/' {
					i += 2
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unclosed block comment '/*'")
			}
			continue
		}

		// 3. String Literal '...'
		if r == '\'' {
			start := i
			i++ // skip opening quote
			var sb strings.Builder
			closed := false
			for i < n {
				if runes[i] == '\'' {
					if i+1 < n && runes[i+1] == '\'' {
						// Escaped single quote ''
						sb.WriteRune('\'')
						i += 2
						continue
					}
					// Closing quote
					i++
					closed = true
					break
				}
				sb.WriteRune(runes[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unclosed string literal starting at position %d", start)
			}
			tokens = append(tokens, Token{
				Type:  TokenStringLiteral,
				Value: sb.String(),
				Raw:   string(runes[start:i]),
			})
			continue
		}

		// 4. Blob Literal X'...' or x'...'
		if (r == 'X' || r == 'x') && i+1 < n && runes[i+1] == '\'' {
			start := i
			i += 2 // skip X'
			var sb strings.Builder
			closed := false
			for i < n {
				if runes[i] == '\'' {
					i++
					closed = true
					break
				}
				sb.WriteRune(runes[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unclosed blob literal starting at position %d", start)
			}
			tokens = append(tokens, Token{
				Type:  TokenBlobLiteral,
				Value: sb.String(),
				Raw:   string(runes[start:i]),
			})
			continue
		}

		// 5. Quoted Identifiers: "...", `...`, [...]
		if r == '"' || r == '`' || r == '[' {
			start := i
			closing := r
			if r == '[' {
				closing = ']'
			}
			i++ // skip opening quote
			var sb strings.Builder
			closed := false
			for i < n {
				if runes[i] == closing {
					if closing != ']' && i+1 < n && runes[i+1] == closing {
						// Escaped quote "" or ``
						sb.WriteRune(closing)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				sb.WriteRune(runes[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unclosed identifier starting at position %d", start)
			}
			tokens = append(tokens, Token{
				Type:  TokenIdentifier,
				Value: strings.ToLower(sb.String()),
				Raw:   string(runes[start:i]),
			})
			continue
		}

		// 6. Numeric Literals: 0x..., 123, 12.34, .5
		if unicode.IsDigit(r) || (r == '.' && i+1 < n && unicode.IsDigit(runes[i+1])) {
			start := i
			if r == '0' && i+1 < n && (runes[i+1] == 'x' || runes[i+1] == 'X') {
				i += 2
				for i < n && (unicode.IsDigit(runes[i]) || (runes[i] >= 'a' && runes[i] <= 'f') || (runes[i] >= 'A' && runes[i] <= 'F')) {
					i++
				}
			} else {
				seenDot := (r == '.')
				i++
				for i < n {
					if runes[i] == '.' && !seenDot {
						seenDot = true
						i++
					} else if unicode.IsDigit(runes[i]) {
						i++
					} else if runes[i] == 'e' || runes[i] == 'E' {
						i++
						if i < n && (runes[i] == '+' || runes[i] == '-') {
							i++
						}
					} else {
						break
					}
				}
			}
			tokens = append(tokens, Token{
				Type:  TokenNumberLiteral,
				Value: string(runes[start:i]),
				Raw:   string(runes[start:i]),
			})
			continue
		}

		// 7. Bare Word Identifiers and Keywords
		if unicode.IsLetter(r) || r == '_' {
			start := i
			i++
			for i < n && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_' || runes[i] == '$') {
				i++
			}
			word := string(runes[start:i])
			upper := strings.ToUpper(word)
			lower := strings.ToLower(word)

			if sqlKeywords[upper] {
				tokens = append(tokens, Token{
					Type:  TokenKeyword,
					Value: upper,
					Raw:   word,
				})
			} else {
				tokens = append(tokens, Token{
					Type:  TokenIdentifier,
					Value: lower,
					Raw:   word,
				})
			}
			continue
		}

		// 8. Operators and Punctuation
		raw := string(r)
		i++

		// Multi-character operators: !=, <>, <=, >=, ==, ||, <<, >>
		if i < n {
			two := raw + string(runes[i])
			if two == "!=" || two == "<>" || two == "<=" || two == ">=" || two == "==" || two == "||" || two == "<<" || two == ">>" {
				raw = two
				i++
			}
		}

		switch raw {
		case ";", ",", "(", ")":
			tokens = append(tokens, Token{
				Type:  TokenPunctuation,
				Value: raw,
				Raw:   raw,
			})
		default:
			tokens = append(tokens, Token{
				Type:  TokenOperator,
				Value: raw,
				Raw:   raw,
			})
		}
	}

	return tokens, nil
}

// ValidateQuery enforces safety invariants on a query before execution:
// 1. Single statement only
// 2. Read-only statement type (SELECT, WITH ... SELECT, EXPLAIN)
// 3. Sensitive column denylist (any appearance in projections, aliases, WHERE, ORDER BY, etc.)
// 4. Table allowlist (table names in query must be permitted, system catalogs blocked)
// 5. Wildcard projection checks (when policy.DisallowWildcard is true)
func ValidateQuery(query string, policy SecurityPolicy, knownTables map[string]bool) error {
	tokens, err := TokenizeSQL(query)
	if err != nil {
		return fmt.Errorf("malformed query: %w", err)
	}

	if len(tokens) == 0 {
		return fmt.Errorf("empty query")
	}

	// 1. Single statement check: semicolon followed by any further non-empty token is forbidden
	for i, t := range tokens {
		if t.Type == TokenPunctuation && t.Value == ";" {
			if i < len(tokens)-1 {
				return fmt.Errorf("multiple SQL statements are not permitted; query must be a single statement")
			}
		}
	}

	// 2. Statement type validation: First token MUST be SELECT, WITH, or EXPLAIN
	firstTok := tokens[0]
	if firstTok.Type != TokenKeyword || (firstTok.Value != "SELECT" && firstTok.Value != "WITH" && firstTok.Value != "EXPLAIN") {
		return fmt.Errorf("only read-only SELECT queries are permitted; forbidden statement type: %s", firstTok.Raw)
	}

	// Prohibit mutating / administrative keywords anywhere in the token stream
	forbiddenOps := map[string]bool{
		"INSERT": true, "UPDATE": true, "DELETE": true, "DROP": true,
		"CREATE": true, "ALTER": true, "REPLACE": true, "TRUNCATE": true,
		"PRAGMA": true, "ATTACH": true, "DETACH": true, "VACUUM": true,
		"REINDEX": true, "SAVEPOINT": true, "RELEASE": true, "ROLLBACK": true,
	}

	for _, t := range tokens {
		if t.Type == TokenKeyword && forbiddenOps[t.Value] {
			return fmt.Errorf("forbidden SQL command: %s", t.Raw)
		}
	}

	// 3. Disallow Wildcard Projections if policy.DisallowWildcard is true
	if policy.DisallowWildcard {
		for i, t := range tokens {
			if t.Raw == "*" {
				// Distinguish projection wildcard from multiplication:
				// Wildcard occurs after SELECT, DISTINCT, ALL, comma (,), or dot (.)
				isWildcard := false
				if i > 0 {
					prev := tokens[i-1]
					if prev.Type == TokenKeyword && (prev.Value == "SELECT" || prev.Value == "DISTINCT" || prev.Value == "ALL") {
						isWildcard = true
					} else if prev.Type == TokenPunctuation && prev.Value == "," {
						isWildcard = true
					} else if prev.Type == TokenOperator && prev.Value == "." {
						isWildcard = true
					}
				}
				if isWildcard {
					return fmt.Errorf("wildcard 'SELECT *' projection is rejected by security policy; explicit column projections required")
				}
			}
		}
	}

	// 4. Sensitive Column Denylist Check across all identifier tokens in the query
	if len(policy.DenyColumns) > 0 {
		for _, t := range tokens {
			// Check identifier tokens and bare words
			if t.Type == TokenIdentifier || (t.Type == TokenKeyword && !isStructuralKeyword(t.Value)) {
				if policy.DenyColumns[t.Value] {
					return fmt.Errorf("access to sensitive column %q is blocked by security policy", t.Raw)
				}
			}
		}
	}

	// 5. System Catalog Snooping and Table Allowlist Verification
	// System catalogs (sqlite_master, sqlite_schema, etc.) are always forbidden in read_query.
	for _, t := range tokens {
		if t.Type == TokenIdentifier || t.Type == TokenKeyword {
			if systemCatalogs[t.Value] {
				return fmt.Errorf("direct catalog inspection of %s is blocked by security policy; use list_tables instead", t.Raw)
			}
		}
	}

	// If Table Allowlist is configured, verify all database tables referenced in the query
	if len(policy.AllowTables) > 0 && knownTables != nil {
		// Identify tables that exist in the DB but are not in the allowlist
		forbiddenTables := make(map[string]bool)
		for tbl := range knownTables {
			tblLower := strings.ToLower(tbl)
			if !policy.AllowTables[tblLower] {
				forbiddenTables[tblLower] = true
			}
		}

		for _, t := range tokens {
			if t.Type == TokenIdentifier {
				if forbiddenTables[t.Value] {
					return fmt.Errorf("access to table %q is blocked by security policy", t.Raw)
				}
			}
		}
	}

	return nil
}

// isStructuralKeyword returns true for core SQL grammar keywords that cannot be column names.
func isStructuralKeyword(kw string) bool {
	switch kw {
	case "SELECT", "FROM", "WHERE", "JOIN", "LEFT", "RIGHT", "FULL", "OUTER", "CROSS", "INNER",
		"ON", "USING", "GROUP", "BY", "HAVING", "ORDER", "ASC", "DESC", "LIMIT", "OFFSET",
		"WITH", "RECURSIVE", "AS", "UNION", "ALL", "INTERSECT", "EXCEPT", "DISTINCT", "AND", "OR":
		return true
	default:
		return false
	}
}
