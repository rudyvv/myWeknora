package source

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// SourceSearchTermsVersion versions the persisted source-only lexical fields.
const SourceSearchTermsVersion = "source-code-search-terms-v1"

const (
	MaxSourceCodeQueryIdentifiers = 8
	MaxSourceCodeQueryTerms       = 32
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
