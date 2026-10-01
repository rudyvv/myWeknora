package source

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

// SourceSearchTermsVersion versions the persisted source-only lexical fields.
const SourceSearchTermsVersion = "source-code-search-terms-v1"

const (
	MaxSourceCodeQueryIdentifiers = 8
	MaxSourceCodeQueryTerms       = 32
	maxSourceChunkIdentifiers     = 256
	maxSourceSearchTerms          = 256
	maxTermsPerIdentifier         = 64
	maxSourceIdentifierBytes      = 512
)

// SourceCodeQuery is an explicitly bounded code-reference query. ExactIdentifiers
// preserve user spelling; NormalizedTerms are used only for lexical matching.
type SourceCodeQuery struct {
	Enabled          bool
	ExactIdentifiers []string
	NormalizedTerms  []string
}

// SourceChunkSearchTerms is the typed source-only lexical projection. It is
// separate from both original chunk bytes and the token-budgeted embedding text.
type SourceChunkSearchTerms struct {
	Path            string
	FullIdentifiers []string
	NormalizedTerms []string
	Version         string
}

var (
	codeQueryToken = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$-]*(?:(?:[./#:]|\\)[A-Za-z_$][A-Za-z0-9_$-]*)*`)
	camelAcronym   = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	camelBoundary  = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	identifierPart = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]*`)
)

// ParseSourceCodeQuery extracts only explicit code-like references. Ordinary
// Chinese and prose queries remain on the existing BM25/vector path unchanged.
func ParseSourceCodeQuery(query string) SourceCodeQuery {
	query = truncateUTF8(query, maxSourceIdentifierBytes)
	if strings.TrimSpace(query) == "" {
		return SourceCodeQuery{}
	}
	quoted := quotedQueryRanges(query)
	identifiers := make([]string, 0, MaxSourceCodeQueryIdentifiers)
	seen := make(map[string]struct{}, MaxSourceCodeQueryIdentifiers)
	terms := make([]string, 0, MaxSourceCodeQueryTerms)
	seenTerms := make(map[string]struct{}, MaxSourceCodeQueryTerms)
	for _, loc := range codeQueryToken.FindAllStringIndex(query, -1) {
		value := query[loc[0]:loc[1]]
		if len(value) > maxSourceIdentifierBytes || (!containsRange(quoted, loc) && !looksLikeCodeIdentifier(value)) {
			continue
		}
		if _, ok := seen[value]; !ok {
			if len(identifiers) < MaxSourceCodeQueryIdentifiers {
				seen[value] = struct{}{}
				identifiers = append(identifiers, value)
			}
		}
		for _, term := range normalizedIdentifierTerms(value) {
			if _, ok := seenTerms[term]; ok {
				continue
			}
			if len(terms) == MaxSourceCodeQueryTerms {
				break
			}
			seenTerms[term] = struct{}{}
			terms = append(terms, term)
		}
	}
	if len(identifiers) == 0 || len(terms) == 0 {
		return SourceCodeQuery{}
	}
	return SourceCodeQuery{Enabled: true, ExactIdentifiers: identifiers, NormalizedTerms: terms}
}

// BuildSourceChunkSearchTerms derives the lexical projection from the original
// path/body and parser-authored symbols/facts. Facts outside the chunk's source
// byte range are intentionally excluded.
func BuildSourceChunkSearchTerms(path string, chunk types.ParsedSourceChunk, symbols []types.SourceSymbol, facts []types.ParsedSourceFact) SourceChunkSearchTerms {
	identifiers := make([]string, 0, maxSourceChunkIdentifiers)
	seenIdentifiers := make(map[string]struct{}, maxSourceChunkIdentifiers)
	identifiers = appendIdentifier(identifiers, seenIdentifiers, path)
	for _, fact := range facts {
		if len(identifiers) >= maxSourceChunkIdentifiers {
			break
		}
		if !rangesOverlap(chunk.Range, fact.Range) {
			continue
		}
		switch fact.Kind {
		case "mybatis_mapper":
			identifiers = appendIdentifier(identifiers, seenIdentifiers, fact.Namespace)
		case "mybatis_statement":
			statementID := fact.StatementID
			if statementID == "" {
				statementID = fact.Name
			}
			identifiers = appendIdentifier(identifiers, seenIdentifiers, statementID)
			if fact.Namespace != "" && statementID != "" {
				identifiers = appendIdentifier(identifiers, seenIdentifiers, fact.Namespace+"#"+statementID)
			}
		case "java_mapper_method":
			identifiers = appendIdentifier(identifiers, seenIdentifiers, fact.Name)
			if fact.Namespace != "" {
				identifiers = appendIdentifier(identifiers, seenIdentifiers, fact.Namespace)
			}
			if fact.Namespace != "" && fact.Name != "" {
				identifiers = appendIdentifier(identifiers, seenIdentifiers, fact.Namespace+"#"+fact.Name)
			}
		}
	}
	for _, symbol := range symbols {
		if len(identifiers) >= maxSourceChunkIdentifiers {
			break
		}
		if !rangesOverlap(chunk.Range, symbol.Range) {
			continue
		}
		identifiers = appendIdentifier(identifiers, seenIdentifiers, symbol.QualifiedName)
		identifiers = appendIdentifier(identifiers, seenIdentifiers, symbol.Name)
	}
	for _, symbol := range chunk.Symbols {
		if len(identifiers) >= maxSourceChunkIdentifiers {
			break
		}
		identifiers = appendIdentifier(identifiers, seenIdentifiers, symbol)
	}
	// Content is the parser-validated original chunk body, never the truncated
	// SourceIndexHeader. It contributes terms but is not copied into identifiers.
	terms := make([]string, 0, 64)
	seenTerms := make(map[string]struct{}, 64)
	for _, value := range identifiers {
		terms = appendNormalizedTerms(terms, seenTerms, value, maxSourceSearchTerms)
	}
	for _, token := range identifierPart.FindAllString(chunk.Content, maxSourceSearchTerms) {
		terms = appendNormalizedTerms(terms, seenTerms, token, maxSourceSearchTerms)
		if len(terms) == maxSourceSearchTerms {
			break
		}
	}
	return SourceChunkSearchTerms{Path: path, FullIdentifiers: identifiers, NormalizedTerms: terms, Version: SourceSearchTermsVersion}
}

func appendIdentifier(values []string, seen map[string]struct{}, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxSourceIdentifierBytes || len(values) >= maxSourceChunkIdentifiers {
		return values
	}
	if _, ok := seen[value]; ok {
		return values
	}
	seen[value] = struct{}{}
	return append(values, value)
}

func appendNormalizedTerms(values []string, seen map[string]struct{}, value string, limit int) []string {
	for _, term := range normalizedIdentifierTerms(value) {
		if _, ok := seen[term]; ok {
			continue
		}
		if len(values) >= limit {
			return values
		}
		seen[term] = struct{}{}
		values = append(values, term)
	}
	return values
}

func normalizedIdentifierTerms(value string) []string {
	parts := identifierPart.FindAllString(value, -1)
	terms := make([]string, 0, len(parts)*3)
	seen := make(map[string]struct{}, len(parts)*3)
	add := func(term string) {
		term = strings.ToLower(strings.Trim(term, "_$"))
		if term == "" || len(term) < 2 {
			return
		}
		if _, ok := seen[term]; ok {
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	for _, part := range parts {
		if len(terms) >= maxTermsPerIdentifier {
			break
		}
		add(part)
		camel := camelAcronym.ReplaceAllString(part, `${1} ${2}`)
		camel = camelBoundary.ReplaceAllString(camel, `${1} ${2}`)
		for _, subpart := range strings.Fields(strings.ReplaceAll(camel, "_", " ")) {
			add(subpart)
		}
	}
	return terms
}

func looksLikeCodeIdentifier(value string) bool {
	if strings.ContainsAny(value, "_./#:\\-") {
		return true
	}
	for i := 1; i < len(value); i++ {
		current := value[i]
		if current < 'A' || current > 'Z' {
			continue
		}
		previous := value[i-1]
		if (previous >= 'a' && previous <= 'z') || (previous >= '0' && previous <= '9') {
			return true
		}
	}
	return false
}

func quotedQueryRanges(query string) [][2]int {
	ranges := make([][2]int, 0, 2)
	for _, quote := range []byte{'`', '\'', '"'} {
		for start := 0; start < len(query); {
			i := strings.IndexByte(query[start:], quote)
			if i < 0 {
				break
			}
			left := start + i + 1
			j := strings.IndexByte(query[left:], quote)
			if j < 0 {
				break
			}
			ranges = append(ranges, [2]int{left, left + j})
			start = left + j + 1
		}
	}
	return ranges
}

func containsRange(ranges [][2]int, target []int) bool {
	for _, r := range ranges {
		if target[0] >= r[0] && target[1] <= r[1] {
			return true
		}
	}
	return false
}

func rangesOverlap(chunk, fact types.SourceRange) bool {
	return chunk.StartByte < fact.EndByte && fact.StartByte < chunk.EndByte
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
