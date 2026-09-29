package tools

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceAnalysisFactSummariesKeepTypedEvidenceAndBoundPayload(t *testing.T) {
	facts, truncated := boundedFactSummaries(types.JSON(`[
		{"kind":"mybatis_statement","name":"find","namespace":"demo.Mapper","quality":"structural","range":{"start_byte":10,"end_byte":20,"start_line":2,"end_line":2},"text":"unbounded statement body","sql":"SELECT * FROM private_table"},
		{"kind":"java_mapper_method","method_name":"find","certainty":"certain","quality":"structural"}
	]`), 1)
	require.True(t, truncated)
	require.Len(t, facts, 1)
	require.Equal(t, "mybatis_statement", facts[0]["kind"])
	require.Equal(t, "demo.Mapper", facts[0]["namespace"])
	require.NotContains(t, facts[0], "text")
	require.NotContains(t, facts[0], "sql")
	longFacts, fieldsTruncated := boundedFactSummaries(types.JSON(`[{"kind":"fact","name":"`+strings.Repeat("x", maxAgentSummaryRunes+4)+`","include_refs":["a","b","c","d","e","f","g","h","i"]}]`), 1)
	require.True(t, fieldsTruncated)
	require.LessOrEqual(t, utf8.RuneCountInString(longFacts[0]["name"].(string)), maxAgentSummaryRunes+1)
	require.Len(t, longFacts[0]["include_refs"], maxAgentSummaryListItems)
}

func TestSourceAnalysisRangeSnippetUsesExactVersionBytesAndRejectsInvalidBounds(t *testing.T) {
	content := []byte("prefix\nSELECT * FROM customers;\nsuffix")
	rangeJSON := types.JSON(`{"start_byte":7,"end_byte":31,"start_line":2,"end_line":2}`)
	location, snippet, truncated, ok := sourceRangeSnippet(content, rangeJSON)
	require.True(t, ok)
	require.False(t, truncated)
	require.Equal(t, types.SourceRange{StartByte: 7, EndByte: 31, StartLine: 2, EndLine: 2}, location)
	require.Equal(t, "SELECT * FROM customers;", snippet)
	_, _, _, ok = sourceRangeSnippet(content, types.JSON(`{"start_byte":7,"end_byte":99}`))
	require.False(t, ok)
	_, _, _, ok = sourceRangeSnippet(content, types.JSON(`{"start_byte":8,"end_byte":7}`))
	require.False(t, ok)
	longContent := []byte(strings.Repeat("x", 500))
	_, longSnippet, longTruncated, ok := sourceRangeSnippet(longContent, types.JSON(`{"start_byte":0,"end_byte":500,"start_line":1,"end_line":1}`))
	require.True(t, ok)
	require.True(t, longTruncated)
	require.Equal(t, maxAgentSnippetRunes+1, utf8.RuneCountInString(longSnippet))
}
