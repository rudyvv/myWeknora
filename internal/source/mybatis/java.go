package mybatis

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var packagePattern = regexp.MustCompile(`(?m)\bpackage\s+([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*)\s*;`)
var interfacePattern = regexp.MustCompile(`(?m)\binterface\s+([A-Za-z_$][\w$]*)`)
var methodPattern = regexp.MustCompile(`(?m)(?:(?:public|protected|private|default|static|final|abstract|synchronized)\s+)*(?:[A-Za-z_$][\w$<>?,.\[\] ]*\s+)([A-Za-z_$][\w$]*)\s*\([^;{}]*\)\s*(?:throws\s+[^;{]+)?;`)
var annotationPattern = regexp.MustCompile(`(?s)@(Select|Insert|Update|Delete|SelectProvider|InsertProvider|UpdateProvider|DeleteProvider)\s*\(`)

func ParseJavaMapper(path string, raw []byte) (JavaMapper, error) {
	if path == "" {
		return JavaMapper{}, fmt.Errorf("Java mapper path is required")
	}
	text := string(raw)
	packageName := ""
	if match := packagePattern.FindStringSubmatch(text); match != nil {
		packageName = match[1]
	}
	interfaces := interfacePattern.FindAllStringSubmatchIndex(text, -1)
	if len(interfaces) == 0 {
		return JavaMapper{}, fmt.Errorf("Java mapper interface was not found")
	}
	selected := interfaces[0]
	name := text[selected[2]:selected[3]]
	qualified := name
	if packageName != "" {
		qualified = packageName + "." + name
	}
	interfaceStart := selected[0]
	bodyStart := strings.Index(text[selected[1]:], "{")
	if bodyStart < 0 {
		return JavaMapper{}, fmt.Errorf("Java mapper interface has no body")
	}
	bodyStart += selected[1]
	bodyEnd := matchingBrace(raw, bodyStart)
	if bodyEnd < 0 {
		return JavaMapper{}, fmt.Errorf("Java mapper interface has an unclosed body")
	}
	mapper := JavaMapper{Path: path, QualifiedName: qualified, Range: sourceRange(raw, interfaceStart, bodyEnd), Quality: "structural"}
	for _, match := range methodPattern.FindAllStringSubmatchIndex(text[bodyStart+1:bodyEnd], -1) {
		start := bodyStart + 1 + match[0]
		end := bodyStart + 1 + match[1]
		methodName := text[bodyStart+1+match[2] : bodyStart+1+match[3]]
		method := MapperMethod{Name: methodName, QualifiedName: qualified + "#" + methodName, Range: sourceRange(raw, start, end), Signature: text[start:end]}
		annotationStart := annotationRegionStart(text, bodyStart+1, start)
		if annotationStart < start {
			method.Range = sourceRange(raw, annotationStart, end)
			if sql := parseAnnotationSQL(raw, text[annotationStart:start], annotationStart); sql != nil {
				method.SQL = sql
				mapper.EmbeddedSQL = append(mapper.EmbeddedSQL, *sql)
			}
		}
		mapper.Methods = append(mapper.Methods, method)
	}
	mapper.EmbeddedSQL = append(mapper.EmbeddedSQL, parseJavaSQLFields(raw, text, bodyStart, bodyEnd)...)
	sort.Slice(mapper.Methods, func(i, j int) bool { return mapper.Methods[i].Range.StartByte < mapper.Methods[j].Range.StartByte })
	return mapper, nil
}

func annotationRegionStart(text string, floor, methodStart int) int {
	start := strings.LastIndex(text[:methodStart], "\n") + 1
	for start > floor {
		previousEnd := start - 1
		previousStart := strings.LastIndex(text[:previousEnd], "\n") + 1
		if strings.HasPrefix(strings.TrimSpace(text[previousStart:previousEnd]), "@") {
			start = previousStart
			continue
		}
		break
	}
	if strings.HasPrefix(strings.TrimSpace(text[start:methodStart]), "@") {
		return start
	}
	return methodStart
}

func parseAnnotationSQL(raw []byte, annotation string, base int) *EmbeddedSQL {
	match := annotationPattern.FindStringSubmatchIndex(annotation)
	if match == nil {
		return nil
	}
	kind := annotation[match[2]:match[3]]
	open := strings.Index(annotation[match[0]:], "(") + match[0]
	close := matchingParen([]byte(annotation), open)
	if close < 0 {
		return &EmbeddedSQL{Kind: kind, Dynamic: true, Certain: false, Quality: "uncertain", Warnings: []string{"unterminated mapper SQL annotation"}}
	}
	payload := annotation[open+1 : close]
	parts, certain := javaStringParts(payload)
	if len(parts) == 0 {
		certain = false
	}
	sql := strings.Join(parts, "\n")
	spans := javaStringSpans(payload)
	start := base + open + 1
	end := start + len(payload)
	if len(spans) > 0 {
		start = base + open + 1 + spans[0].start + 1
		end = base + open + 1 + spans[len(spans)-1].end
	}
	// A single plain literal maps SQL token offsets back to exact original
	// bytes. Fragment arrays and escaped literals remain readable, but their
	// decoded text cannot safely claim token-level original coordinates.
	exact := certain && len(spans) == 1 && !spans[0].escaped
	result := extractEmbeddedSQL(kind, sql, raw, start, !exact)
	if result == nil {
		result = &EmbeddedSQL{Kind: kind, SQL: sql, SQLRange: sourceRange(raw, start, end), Dynamic: !certain, Certain: exact, Quality: qualityFor(!exact)}
	}
	result.SQLRange = sourceRange(raw, start, end)
	result.Certain = exact
	result.Dynamic = !certain
	result.Quality = qualityFor(!exact)
	if !exact {
		result.Tables = nil
		result.Warnings = append(result.Warnings, "SQL is decoded from escaped or multiple Java literals; token ranges are not claimed")
	}
	if !certain {
		result.Warnings = append(result.Warnings, "mapper annotation contains non-literal or concatenated SQL")
	}
	return result
}

type javaLiteralSpan struct {
	start   int
	end     int
	escaped bool
}

func javaStringSpans(payload string) []javaLiteralSpan {
	spans := []javaLiteralSpan{}
	for i := 0; i < len(payload); {
		if payload[i] != '"' {
			i++
			continue
		}
		_, next, ok := javaString(payload, i)
		if !ok {
			return spans
		}
		spans = append(spans, javaLiteralSpan{start: i, end: next - 1, escaped: strings.ContainsRune(payload[i+1:next-1], '\\')})
		i = next
	}
	return spans
}

func javaStringParts(payload string) ([]string, bool) {
	parts := []string{}
	certain := true
	for i := 0; i < len(payload); {
		if unicode.IsSpace(rune(payload[i])) || strings.ContainsRune("{},", rune(payload[i])) {
			i++
			continue
		}
		if payload[i] != '"' {
			certain = false
			i++
			continue
		}
		value, next, ok := javaString(payload, i)
		if !ok {
			return parts, false
		}
		parts = append(parts, value)
		i = next
	}
	return parts, certain
}

func javaString(text string, start int) (string, int, bool) {
	var result strings.Builder
	for i := start + 1; i < len(text); i++ {
		switch text[i] {
		case '"':
			return result.String(), i + 1, true
		case '\\':
			if i+1 >= len(text) {
				return "", len(text), false
			}
			next := text[i+1]
			switch next {
			case 'n':
				result.WriteByte('\n')
			case 'r':
				result.WriteByte('\r')
			case 't':
				result.WriteByte('\t')
			default:
				result.WriteByte(next)
			}
			i++
		default:
			result.WriteByte(text[i])
		}
	}
	return "", len(text), false
}

func matchingParen(raw []byte, open int) int {
	depth := 0
	for i := open; i < len(raw); i++ {
		if raw[i] == '(' {
			depth++
		}
		if raw[i] == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func matchingBrace(raw []byte, open int) int {
	depth := 0
	for i := open; i < len(raw); i++ {
		if raw[i] == '"' || raw[i] == '\'' {
			_, next, ok := javaString(string(raw), i)
			if ok {
				i = next - 1
			}
		}
		if raw[i] == '{' {
			depth++
		}
		if raw[i] == '}' {
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func parseJavaSQLFields(raw []byte, text string, bodyStart, bodyEnd int) []EmbeddedSQL {
	result := []EmbeddedSQL{}
	for i := bodyStart; i < bodyEnd; {
		if text[i] != '"' {
			i++
			continue
		}
		value, next, ok := javaString(text, i)
		if !ok {
			break
		}
		if looksLikeSQL(strings.TrimSpace(value)) {
			dynamic := adjacentPlus(text, i, next) || strings.Contains(value, "${")
			if embedded := extractEmbeddedSQL("java_literal", value, raw, i+1, dynamic); embedded != nil {
				if strings.ContainsRune(text[i+1:next-1], '\\') {
					embedded.Certain = false
					embedded.Quality = "uncertain"
					embedded.Tables = nil
					embedded.Warnings = append(embedded.Warnings, "escaped Java SQL literal has no claimed token coordinates")
				}
				result = append(result, *embedded)
			}
		}
		i = next
	}
	return result
}

func adjacentPlus(text string, start, end int) bool {
	left := start - 1
	for left >= 0 && unicode.IsSpace(rune(text[left])) {
		left--
	}
	right := end
	for right < len(text) && unicode.IsSpace(rune(text[right])) {
		right++
	}
	return (left >= 0 && text[left] == '+') || (right < len(text) && text[right] == '+')
}
