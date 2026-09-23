package main

import (
	"strings"
	"testing"
)

func TestTokenizeSQL_Success(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []Token
	}{
		{
			name:  "whitespace and comments",
			input: "  -- comment\nSELECT /* block */ 1;",
			expected: []Token{
				{Type: TokenKeyword, Value: "SELECT", Raw: "SELECT"},
				{Type: TokenNumberLiteral, Value: "1", Raw: "1"},
				{Type: TokenPunctuation, Value: ";", Raw: ";"},
			},
		},
		{
			name:  "string literal with escape",
			input: "SELECT 'hello', 'O''Reilly';",
			expected: []Token{
				{Type: TokenKeyword, Value: "SELECT", Raw: "SELECT"},
				{Type: TokenStringLiteral, Value: "hello", Raw: "'hello'"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenStringLiteral, Value: "O'Reilly", Raw: "'O''Reilly'"},
				{Type: TokenPunctuation, Value: ";", Raw: ";"},
			},
		},
		{
			name:  "blob literals",
			input: "SELECT X'01AF', x'ff';",
			expected: []Token{
				{Type: TokenKeyword, Value: "SELECT", Raw: "SELECT"},
				{Type: TokenBlobLiteral, Value: "01AF", Raw: "X'01AF'"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenBlobLiteral, Value: "ff", Raw: "x'ff'"},
				{Type: TokenPunctuation, Value: ";", Raw: ";"},
			},
		},
		{
			name:  "quoted identifiers",
			input: `SELECT "users", "escaped""name", ` + "`posts`" + `, [audit];`,
			expected: []Token{
				{Type: TokenKeyword, Value: "SELECT", Raw: "SELECT"},
				{Type: TokenIdentifier, Value: "users", Raw: `"users"`},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenIdentifier, Value: `escaped"name`, Raw: `"escaped""name"`},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenIdentifier, Value: "posts", Raw: "`posts`"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenIdentifier, Value: "audit", Raw: "[audit]"},
				{Type: TokenPunctuation, Value: ";", Raw: ";"},
			},
		},
		{
			name:  "numbers",
			input: "SELECT 0xFF, 42, 3.14, .5, 1e-3, 2.5E+2;",
			expected: []Token{
				{Type: TokenKeyword, Value: "SELECT", Raw: "SELECT"},
				{Type: TokenNumberLiteral, Value: "0xFF", Raw: "0xFF"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenNumberLiteral, Value: "42", Raw: "42"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenNumberLiteral, Value: "3.14", Raw: "3.14"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenNumberLiteral, Value: ".5", Raw: ".5"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenNumberLiteral, Value: "1e-3", Raw: "1e-3"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenNumberLiteral, Value: "2.5E+2", Raw: "2.5E+2"},
				{Type: TokenPunctuation, Value: ";", Raw: ";"},
			},
		},
		{
			name:  "operators",
			input: "SELECT a != b, c <> d, e <= f, g >= h, i == j, k || l, m << 1, n >> 2;",
			expected: []Token{
				{Type: TokenKeyword, Value: "SELECT", Raw: "SELECT"},
				{Type: TokenIdentifier, Value: "a", Raw: "a"},
				{Type: TokenOperator, Value: "!=", Raw: "!="},
				{Type: TokenIdentifier, Value: "b", Raw: "b"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenIdentifier, Value: "c", Raw: "c"},
				{Type: TokenOperator, Value: "<>", Raw: "<>"},
				{Type: TokenIdentifier, Value: "d", Raw: "d"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenIdentifier, Value: "e", Raw: "e"},
				{Type: TokenOperator, Value: "<=", Raw: "<="},
				{Type: TokenIdentifier, Value: "f", Raw: "f"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenIdentifier, Value: "g", Raw: "g"},
				{Type: TokenOperator, Value: ">=", Raw: ">="},
				{Type: TokenIdentifier, Value: "h", Raw: "h"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenIdentifier, Value: "i", Raw: "i"},
				{Type: TokenOperator, Value: "==", Raw: "=="},
				{Type: TokenIdentifier, Value: "j", Raw: "j"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenIdentifier, Value: "k", Raw: "k"},
				{Type: TokenOperator, Value: "||", Raw: "||"},
				{Type: TokenIdentifier, Value: "l", Raw: "l"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenIdentifier, Value: "m", Raw: "m"},
				{Type: TokenOperator, Value: "<<", Raw: "<<"},
				{Type: TokenNumberLiteral, Value: "1", Raw: "1"},
				{Type: TokenPunctuation, Value: ",", Raw: ","},
				{Type: TokenIdentifier, Value: "n", Raw: "n"},
				{Type: TokenOperator, Value: ">>", Raw: ">>"},
				{Type: TokenNumberLiteral, Value: "2", Raw: "2"},
				{Type: TokenPunctuation, Value: ";", Raw: ";"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, err := TokenizeSQL(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tokens) != len(tt.expected) {
				t.Fatalf("token count mismatch: got %d, expected %d", len(tokens), len(tt.expected))
			}
			for i, exp := range tt.expected {
				got := tokens[i]
				if got.Type != exp.Type || got.Value != exp.Value || got.Raw != exp.Raw {
					t.Errorf("token %d mismatch: got %+v, expected %+v", i, got, exp)
				}
			}
		})
	}
}

func TestTokenizeSQL_Errors(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		errSubstrings []string
	}{
		{
			name:          "unclosed block comment",
			input:         "SELECT 1 /* unfinished",
			errSubstrings: []string{"unclosed block comment"},
		},
		{
			name:          "unclosed string literal",
			input:         "SELECT 'unfinished",
			errSubstrings: []string{"unclosed string literal"},
		},
		{
			name:          "unclosed blob literal",
			input:         "SELECT X'1234",
			errSubstrings: []string{"unclosed blob literal"},
		},
		{
			name:          "unclosed double quote",
			input:         `SELECT "users`,
			errSubstrings: []string{"unclosed identifier"},
		},
		{
			name:          "unclosed backtick",
			input:         "SELECT `users",
			errSubstrings: []string{"unclosed identifier"},
		},
		{
			name:          "unclosed bracket",
			input:         "SELECT [users",
			errSubstrings: []string{"unclosed identifier"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := TokenizeSQL(tt.input)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			for _, sub := range tt.errSubstrings {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("expected error to contain %q, got: %v", sub, err)
				}
			}
		})
	}
}

func TestValidateQuery_StatementTypes(t *testing.T) {
	policy := NewDefaultPolicy()

	valid := []string{
		"SELECT 1",
		"SELECT id, name FROM users",
		"WITH u AS (SELECT 1) SELECT * FROM u",
		"EXPLAIN QUERY PLAN SELECT 1",
	}

	for _, q := range valid {
		if err := ValidateQuery(q, policy, nil); err != nil {
			t.Errorf("expected query %q to be valid, got: %v", q, err)
		}
	}

	invalid := []struct {
		query       string
		errContains string
	}{
		{"", "empty query"},
		{"   ", "empty query"},
		{"INSERT INTO users VALUES (1)", "forbidden statement type: INSERT"},
		{"UPDATE users SET name = 'a'", "forbidden statement type: UPDATE"},
		{"DELETE FROM users", "forbidden statement type: DELETE"},
		{"DROP TABLE users", "forbidden statement type: DROP"},
		{"CREATE TABLE users (id INT)", "forbidden statement type: CREATE"},
		{"ALTER TABLE users ADD COLUMN c INT", "forbidden statement type: ALTER"},
		{"PRAGMA table_info(users)", "forbidden statement type: PRAGMA"},
		{"ATTACH DATABASE 'foo.db' AS foo", "forbidden statement type: ATTACH"},
		{"DETACH DATABASE foo", "forbidden statement type: DETACH"},
		{"VACUUM", "forbidden statement type: VACUUM"},
		{"REINDEX", "forbidden statement type: REINDEX"},
		{"SELECT 1; DROP TABLE users;", "multiple SQL statements"},
		{"SELECT 1 WHERE (SELECT count(*) FROM users) = 1 AND 1=0 UNION SELECT 1", ""},
	}

	for _, tt := range invalid {
		err := ValidateQuery(tt.query, policy, nil)
		if tt.errContains == "" {
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tt.query, err)
			}
		} else {
			if err == nil {
				t.Errorf("expected error for query %q, got nil", tt.query)
			} else if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("expected error for %q to contain %q, got: %v", tt.query, tt.errContains, err)
			}
		}
	}
}

func TestValidateQuery_WildcardOptions(t *testing.T) {
	policy := SecurityPolicy{
		DisallowWildcard: true,
	}

	rejected := []string{
		"SELECT * FROM users",
		"SELECT   * FROM users",
		"SELECT u.* FROM users u",
		"SELECT id, * FROM users",
		"SELECT DISTINCT * FROM users",
		"SELECT ALL * FROM users",
	}

	for _, q := range rejected {
		err := ValidateQuery(q, policy, nil)
		if err == nil || !strings.Contains(err.Error(), "wildcard 'SELECT *'") {
			t.Errorf("expected query %q to be rejected, got: %v", q, err)
		}
	}

	allowed := []string{
		"SELECT a * b FROM users",
		"SELECT (a + 1) * 2 FROM users",
		"SELECT id, count FROM users",
	}

	for _, q := range allowed {
		if err := ValidateQuery(q, policy, nil); err != nil {
			t.Errorf("expected query %q to be allowed, got: %v", q, err)
		}
	}
}

func TestValidateQuery_SensitiveColumns(t *testing.T) {
	policy := SecurityPolicy{
		DenyColumns: map[string]bool{
			"password": true,
			"secret":   true,
		},
	}

	blocked := []string{
		"SELECT password FROM users",
		"SELECT password AS pass FROM users",
		"SELECT substr(password, 1, 4) FROM users",
		"SELECT id FROM users WHERE password = 'x'",
		"SELECT id FROM users ORDER BY password",
		"SELECT count(*) FROM users GROUP BY secret",
		`SELECT "password" FROM users`,
		"SELECT [secret] FROM users",
	}

	for _, q := range blocked {
		err := ValidateQuery(q, policy, nil)
		if err == nil || !strings.Contains(err.Error(), "access to sensitive column") {
			t.Errorf("expected query %q to be blocked, got: %v", q, err)
		}
	}

	allowed := []string{
		"SELECT id, name FROM users",
		"SELECT id FROM users WHERE role = 'password'",
		"SELECT 'secret' AS label FROM users",
	}

	for _, q := range allowed {
		if err := ValidateQuery(q, policy, nil); err != nil {
			t.Errorf("expected query %q to be allowed, got: %v", q, err)
		}
	}
}

func TestValidateQuery_TableAllowlist(t *testing.T) {
	policy := SecurityPolicy{
		AllowTables: map[string]bool{
			"posts": true,
		},
	}
	knownTables := map[string]bool{
		"posts": true,
		"users": true,
	}

	blocked := []string{
		"SELECT * FROM users",
		"SELECT id FROM [users]",
		`SELECT id FROM "users"`,
		"SELECT p.id, u.name FROM posts p JOIN users u ON p.id = u.id",
		"SELECT * FROM sqlite_master",
		"SELECT * FROM sqlite_schema",
	}

	for _, q := range blocked {
		err := ValidateQuery(q, policy, knownTables)
		if err == nil {
			t.Errorf("expected query %q to be blocked, got nil", q)
		}
	}

	if err := ValidateQuery("SELECT id, title FROM posts", policy, knownTables); err != nil {
		t.Errorf("expected query on allowed table to succeed, got: %v", err)
	}
}

func TestIsStructuralKeyword(t *testing.T) {
	keywords := []string{"SELECT", "FROM", "WHERE", "JOIN", "ORDER", "GROUP", "LIMIT", "AND", "OR"}
	for _, kw := range keywords {
		if !isStructuralKeyword(kw) {
			t.Errorf("expected %q to be structural keyword", kw)
		}
	}

	nonStructural := []string{"token", "password", "users", "RANDOM"}
	for _, w := range nonStructural {
		if isStructuralKeyword(w) {
			t.Errorf("expected %q not to be structural keyword", w)
		}
	}
}
