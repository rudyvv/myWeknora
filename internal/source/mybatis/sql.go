package mybatis

import (
	"bytes"
	"strings"
	"unicode"
)

type sqlToken struct {
	text  string
	start int
	end   int
}

// extractTables is intentionally a conservative lexer, not a SQL executor or
// formatter. It recognizes table positions shared by MySQL/PostgreSQL-style
// mapper SQL and leaves dialect-specific constructs as unknown text.
func extractTables(raw []byte, dynamic bool) []TableAccess {
	tokens := lexSQL(raw)
	tables := []TableAccess{}
	for i := 0; i < len(tokens); i++ {
		operation := ""
		next := i + 1
		switch strings.ToLower(tokens[i].text) {
		case "from", "join":
			operation = strings.ToLower(tokens[i].text)
		case "update":
			operation = "update"
		case "into":
			operation = "into"
		case "delete":
			if next < len(tokens) && strings.EqualFold(tokens[next].text, "from") {
				operation = "delete"
				next++
			}
		}
		if operation == "" || next >= len(tokens) {
			continue
		}
		for next < len(tokens) && tokens[next].text == "(" {
			next++
		}
		if next >= len(tokens) || isSQLKeyword(tokens[next].text) {
			continue
		}
		name := strings.Trim(tokens[next].text, "`\"")
		if name == "" || strings.HasPrefix(name, "#") || strings.HasPrefix(name, "$") {
			continue
		}
		end := tokens[next].end
		if next+2 < len(tokens) && tokens[next+1].text == "." {
			second := strings.Trim(tokens[next+2].text, "`\"")
			if second != "" {
				name += "." + second
				end = tokens[next+2].end
			}
		}
		tables = append(tables, TableAccess{Name: name, Operation: operation, Range: sourceRange(raw, tokens[next].start, end), Certain: !dynamic})
	}
	return dedupeTables(tables)
}

func lexSQL(raw []byte) []sqlToken {
	tokens := []sqlToken{}
	for i := 0; i < len(raw); {
		if unicode.IsSpace(rune(raw[i])) {
			i++
			continue
		}
		if raw[i] == '-' && i+1 < len(raw) && raw[i+1] == '-' {
			i += 2
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			continue
		}
		if raw[i] == '/' && i+1 < len(raw) && raw[i+1] == '*' {
			end := bytes.Index(raw[i+2:], []byte("*/"))
			if end < 0 {
				break
			}
			i += end + 4
			continue
		}
		if raw[i] == '\'' || raw[i] == '"' || raw[i] == '`' {
			quote := raw[i]
			start := i
			i++
			for i < len(raw) {
				if raw[i] == quote {
					if i+1 < len(raw) && raw[i+1] == quote {
						i += 2
						continue
					}
					i++
					break
				}
				if raw[i] == '\\' && i+1 < len(raw) {
					i += 2
				} else {
					i++
				}
			}
			// String literals are not table names. Quoted identifiers are, but
			// backticks are the only common mapper identifier quote here.
			if quote == '`' {
				tokens = append(tokens, sqlToken{text: string(raw[start:i]), start: start, end: i})
			}
			continue
		}
		if isSQLPunctuation(raw[i]) {
			tokens = append(tokens, sqlToken{text: string(raw[i]), start: i, end: i + 1})
			i++
			continue
		}
		start := i
		for i < len(raw) && !unicode.IsSpace(rune(raw[i])) && !isSQLPunctuation(raw[i]) && raw[i] != '\'' && raw[i] != '"' && raw[i] != '`' {
			i++
		}
		tokens = append(tokens, sqlToken{text: string(raw[start:i]), start: start, end: i})
	}
	return tokens
}

func isSQLPunctuation(value byte) bool { return strings.ContainsRune("(),;.", rune(value)) }

func isSQLKeyword(value string) bool {
	switch strings.ToLower(strings.Trim(value, "`\"")) {
	case "select", "from", "where", "join", "left", "right", "inner", "outer", "on", "group", "order", "limit", "union", "values":
		return true
	default:
		return false
	}
}

func dedupeTables(tables []TableAccess) []TableAccess {
	seen := map[string]bool{}
	result := make([]TableAccess, 0, len(tables))
	for _, table := range tables {
		key := table.Operation + "|" + table.Name + "|" + string(rune(table.Range.StartByte))
		if !seen[key] {
			seen[key] = true
			result = append(result, table)
		}
	}
	return result
}

func mergeTableRanges(raw []byte, sqlStart int, sql string, tables []TableAccess) []TableAccess {
	for i := range tables {
		tables[i].Range = sourceRange(raw, sqlStart+tables[i].Range.StartByte, sqlStart+tables[i].Range.EndByte)
	}
	return tables
}

func extractEmbeddedSQL(kind, sql string, raw []byte, start int, dynamic bool) *EmbeddedSQL {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" || !looksLikeSQL(trimmed) {
		return nil
	}
	tables := extractTables([]byte(sql), dynamic)
	tables = mergeTableRanges(raw, start, sql, tables)
	return &EmbeddedSQL{Kind: kind, SQL: sql, SQLRange: sourceRange(raw, start, start+len(sql)), Dynamic: dynamic, Certain: !dynamic, Tables: tables, Quality: qualityFor(dynamic)}
}

func looksLikeSQL(sql string) bool {
	parts := strings.Fields(strings.ToLower(sql))
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "select", "insert", "update", "delete", "with":
		return true
	default:
		return false
	}
}

func qualityFor(dynamic bool) string {
	if dynamic {
		return "uncertain"
	}
	return "structural"
}
