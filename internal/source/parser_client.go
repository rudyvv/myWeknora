package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/tiktoken-go/tokenizer"
)

// LanguageForPath selects a locked grammar or an explicitly supported text-only route.
// No project plugins or repository-defined parsers run.
func LanguageForPath(logicalPath string) string {
	base := strings.ToLower(path.Base(logicalPath))
	if base == "dockerfile" || base == "containerfile" || strings.HasPrefix(base, "dockerfile.") || strings.HasPrefix(base, "containerfile.") {
		return "text"
	}
	switch strings.ToLower(path.Ext(logicalPath)) {
	case ".java":
		return "java"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".mts", ".cts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".py":
		return "python"
	case ".xml":
		return "mybatis-xml"
	case ".vue":
		return "vue"
	case ".html", ".htm", ".jsp", ".jspx", ".tag", ".tagx", ".ftl", ".ftlh", ".vm",
		".css", ".scss", ".sass", ".less", ".styl", ".yaml", ".yml", ".json", ".toml",
		".properties", ".ini", ".conf", ".cfg", ".env", ".sql", ".sh", ".bash",
		".md", ".txt":
		return "text"
	default:
		return ""
	}
}

// ParseJava retains the initial Java caller contract.
func ParseJava(ctx context.Context, endpoint, logicalPath string, raw []byte) (*types.ParsedSourceFile, error) {
	if LanguageForPath(logicalPath) != "java" {
		return nil, fmt.Errorf("Java source path required")
	}
	return ParseFile(ctx, endpoint, logicalPath, raw)
}

// ParseFile uses the deployment-owned worker URL, never one supplied by GitLab.
// The worker sees a logical path, verified bytes and hash, without credentials.
func ParseFile(ctx context.Context, endpoint, path string, raw []byte) (*types.ParsedSourceFile, error) {
	return ParseFileWithProfile(ctx, endpoint, path, raw, IndexProfile{
		Tokenizer: tokenizer.Cl100kBase,
		MaxTokens: SourceIndexTokenCeiling,
	})
}

// ParseFileWithProfile verifies complete derived index text against the
// selected embedding model's explicit tokenizer and usable per-input limit.
func ParseFileWithProfile(ctx context.Context, endpoint, path string, raw []byte, profile IndexProfile) (*types.ParsedSourceFile, error) {
	language := LanguageForPath(path)
	if language == "" {
		return nil, fmt.Errorf("source language is not supported")
	}
	if profile.MaxTokens <= 0 {
		return nil, fmt.Errorf("source index token budget is invalid")
	}
	codec, err := tokenizer.Get(profile.Tokenizer)
	if err != nil {
		return nil, fmt.Errorf("source tokenizer unavailable")
	}
	hash := sha256.Sum256(raw)
	digest := hex.EncodeToString(hash[:])
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	// The parser worker budgets bytes. Count the body, header and complete
	// derived index text with the selected model tokenizer, then reduce the byte
	// budget until each chunk is within the usable model token limit.
	for budget := 4096; budget >= SourceMinimumChunkBytes; budget /= 2 {
		body, _ := json.Marshal(map[string]any{"path": path, "language": language, "sha256": digest, "content_base64": base64.StdEncoding.EncodeToString(raw), "chunk_max_bytes": budget})
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/parse", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("source parser URL is invalid")
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("source parser unavailable")
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<20+1))
		_ = response.Body.Close()
		if readErr != nil || len(data) > 64<<20 || response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("source parser rejected the file or exceeded its resource limit")
		}
		var parsed types.ParsedSourceFile
		if json.Unmarshal(data, &parsed) != nil || parsed.ParserVersion == "" || parsed.SHA256 != digest || parsed.ByteLength != len(raw) {
			return nil, fmt.Errorf("source parser content/hash mismatch")
		}
		if len(parsed.Facts) > 50000 || len(parsed.Diagnostics) > 50000 {
			return nil, fmt.Errorf("source parser exceeded its fact or diagnostic limit")
		}
		allowedFacts := map[string]bool{
			"java_mapper_method": true, "java_import": true, "java_type": true,
			"java_supertype_reference": true,
			"java_injection":           true, "java_method": true, "spring_mapping": true,
			"java_method_call": true, "java_dynamic_dispatch": true, "api_request": true,
			"api_prefix": true, "api_proxy": true,
			"java_annotation_sql": true, "mybatis_mapper": true, "mybatis_statement": true,
			"mybatis_result_map": true, "mybatis_sql_fragment": true, "mybatis_include": true,
			"mybatis_result_map_reference": true,
			"sql_table":                    true,
		}
		for _, fact := range parsed.Facts {
			if !allowedFacts[fact.Kind] || !validSourceRange(raw, fact.Range) || fact.Quality == "" {
				return nil, fmt.Errorf("source parser returned invalid static fact coordinates")
			}
			if fact.Kind == "java_supertype_reference" && (fact.Name == "" || fact.TargetName != fact.Name || fact.Namespace == "" ||
				fact.OwnerKind == "" || (fact.ReferenceKind != "extends" && fact.ReferenceKind != "implements") ||
				fact.Certainty != "uncertain" || fact.Reason == "") {
				return nil, fmt.Errorf("source parser returned an invalid unresolved Java supertype fact")
			}
			if fact.Text != "" && string(raw[fact.Range.StartByte:fact.Range.EndByte]) != fact.Text {
				return nil, fmt.Errorf("source parser fact does not match original bytes")
			}
			if fact.Kind == "sql_table" && fact.Name == "" {
				return nil, fmt.Errorf("source parser returned an empty table fact")
			}
		}
		for _, diagnostic := range parsed.Diagnostics {
			if diagnostic.Code == "" || diagnostic.Message == "" {
				return nil, fmt.Errorf("source parser returned an invalid diagnostic")
			}
			if diagnostic.Range != (types.SourceRange{}) && !validSourceRange(raw, diagnostic.Range) {
				return nil, fmt.Errorf("source parser returned invalid diagnostic coordinates")
			}
		}
		for _, symbol := range parsed.Symbols {
			if !validSourceRange(raw, symbol.Range) || !validSourceRange(raw, symbol.SignatureRange) || symbol.SignatureRange.StartByte < symbol.Range.StartByte || symbol.SignatureRange.EndByte > symbol.Range.EndByte || string(raw[symbol.SignatureRange.StartByte:symbol.SignatureRange.EndByte]) != symbol.Signature {
				return nil, fmt.Errorf("source parser returned invalid symbol coordinates")
			}
			if !validSourceRegion(symbol.Region) {
				return nil, fmt.Errorf("source parser returned invalid symbol region")
			}
			for _, annotation := range symbol.Annotations {
				if !validSourceRange(raw, annotation.Range) || annotation.Range.StartByte < symbol.Range.StartByte || annotation.Range.EndByte > symbol.Range.EndByte || string(raw[annotation.Range.StartByte:annotation.Range.EndByte]) != annotation.Text {
					return nil, fmt.Errorf("source parser returned invalid annotation coordinates")
				}
			}
		}
		cursor, oversized := 0, false
		var headerTrimmedRange types.SourceRange
		for chunkIndex := range parsed.Chunks {
			chunk := &parsed.Chunks[chunkIndex]
			span := chunk.Range
			if span.StartByte != cursor || span.EndByte <= cursor || span.EndByte > len(raw) || !utf8.Valid(raw[span.StartByte:span.EndByte]) || string(raw[span.StartByte:span.EndByte]) != chunk.Content {
				return nil, fmt.Errorf("source parser returned invalid original coordinates")
			}
			if !validSourceLines(raw, span) {
				return nil, fmt.Errorf("source parser returned invalid original line numbers")
			}
			if !validSourceRegion(chunk.Region) {
				return nil, fmt.Errorf("source parser returned invalid chunk region")
			}
			for _, diagnostic := range chunk.Diagnostics {
				if !validSourceDiagnostic(language, raw, diagnostic) {
					return nil, fmt.Errorf("source parser returned invalid diagnostic coordinates")
				}
			}
			for _, context := range chunk.Context {
				p := context.Range
				if p.StartByte < 0 || p.EndByte < p.StartByte || p.EndByte > len(raw) || string(raw[p.StartByte:p.EndByte]) != context.Text || !validSourceLines(raw, p) {
					return nil, fmt.Errorf("source parser returned invalid context coordinates")
				}
			}
			trimmed, headerTokens, e := fitSourceIndexHeader(codec, path, chunk, profile.MaxTokens)
			if e != nil {
				return nil, fmt.Errorf("source tokenization failed")
			}
			if trimmed && headerTrimmedRange == (types.SourceRange{}) {
				headerTrimmedRange = chunk.Range
			}
			if headerTokens > profile.MaxTokens {
				return nil, fmt.Errorf("source index header exceeds the embedding model token budget after removing trace context")
			}
			_, bodyTokens, indexTokens, e := sourceIndexTokenCounts(codec, path, *chunk)
			if e != nil {
				return nil, fmt.Errorf("source tokenization failed")
			}
			oversized = oversized || bodyTokens > profile.MaxTokens || indexTokens > profile.MaxTokens
			cursor = span.EndByte
		}
		if cursor != len(raw) {
			return nil, fmt.Errorf("source parser returned an incomplete file")
		}
		if headerTrimmedRange != (types.SourceRange{}) {
			parsed.Diagnostics = append(parsed.Diagnostics, types.ParsedSourceDiagnostic{
				Code:    "source_index_header_trimmed",
				Message: "Trace context or nonessential symbols were omitted from derived embedding text to fit the model input limit",
				Range:   headerTrimmedRange,
			})
		}
		if !oversized {
			return &parsed, nil
		}
	}
	return nil, fmt.Errorf("source context exceeds the embedding model token budget")
}

func sourceIndexTokenCounts(codec tokenizer.Codec, path string, chunk types.ParsedSourceChunk) (header, body, full int, err error) {
	header, err = sourceIndexHeaderTokenCount(codec, path, chunk)
	if err != nil {
		return 0, 0, 0, err
	}
	bodyIDs, _, err := codec.Encode(chunk.Content)
	if err != nil {
		return 0, 0, 0, err
	}
	fullIDs, _, err := codec.Encode(SourceIndexText(path, chunk))
	if err != nil {
		return 0, 0, 0, err
	}
	return header, len(bodyIDs), len(fullIDs), nil
}

func sourceIndexHeaderTokenCount(codec tokenizer.Codec, path string, chunk types.ParsedSourceChunk) (int, error) {
	headerIDs, _, err := codec.Encode(SourceIndexHeader(path, chunk))
	if err != nil {
		return 0, err
	}
	return len(headerIDs), nil
}

func fitSourceIndexHeader(codec tokenizer.Codec, path string, chunk *types.ParsedSourceChunk, limit int) (bool, int, error) {
	trimmed := false
	for {
		headerTokens, err := sourceIndexHeaderTokenCount(codec, path, *chunk)
		if err != nil || headerTokens <= limit {
			return trimmed, headerTokens, err
		}
		if len(chunk.Context) > 0 {
			chunk.Context = chunk.Context[1:]
			trimmed = true
			continue
		}
		if len(chunk.Symbols) > 0 {
			chunk.Symbols = chunk.Symbols[:len(chunk.Symbols)-1]
			trimmed = true
			continue
		}
		return trimmed, headerTokens, nil
	}
}

func validSourceRegion(region *types.SourceRegion) bool {
	if region == nil {
		return true
	}
	if region.Kind != "template" && region.Kind != "script" && region.Kind != "style" && region.Kind != "custom" {
		return false
	}
	if region.Quality != "structural" && region.Quality != "syntax_error" && region.Quality != "degraded" &&
		region.Quality != "unknown_preprocess" && region.Quality != "text_fallback" {
		return false
	}
	if len(region.Language) > 64 || len(region.ExternalSource) > 4096 || len(region.ResolvedPath) > 4096 {
		return false
	}
	if region.ExternalSource == "" {
		return region.ExternalStatus == "" && region.ResolvedPath == ""
	}
	if region.Kind != "script" || (region.ExternalStatus != "unchecked" && region.ExternalStatus != "rejected") {
		return false
	}
	return region.ResolvedPath == ""
}

func validSourceLines(raw []byte, span types.SourceRange) bool {
	last := span.EndByte - 1
	if last < span.StartByte {
		last = span.StartByte
	}
	return span.StartLine == bytes.Count(raw[:span.StartByte], []byte{'\n'})+1 && span.EndLine == bytes.Count(raw[:last], []byte{'\n'})+1
}

func validSourceRange(raw []byte, span types.SourceRange) bool {
	return span.StartByte >= 0 && span.EndByte >= span.StartByte && span.EndByte <= len(raw) && utf8.Valid(raw[span.StartByte:span.EndByte]) && validSourceLines(raw, span)
}

// A warning may be attributed to a body chunk even when its original location
// is in a block wrapper or at EOF; validate only the trusted source range.
func validSourceDiagnostic(language string, raw []byte, diagnostic types.SourceDiagnostic) bool {
	if language != "vue" || (diagnostic.Code != "vue_sfc_parse_warning" && diagnostic.Code != "vue_sfc_duplicate_block") {
		return false
	}
	return validSourceRange(raw, diagnostic.Range)
}

// SourceIndexText is derived index text, not a contiguous original fragment.
// Only Chunk.Content and separately ranged context may be shown as code evidence.
func SourceIndexText(path string, chunk types.ParsedSourceChunk) string {
	return SourceIndexHeader(path, chunk) + "\n" + chunk.Content
}

// SourceIndexHeader is derived context counted separately from the source body.
func SourceIndexHeader(path string, chunk types.ParsedSourceChunk) string {
	parts := []string{path, strings.Join(chunk.Symbols, " ")}
	if chunk.Region != nil {
		parts = append(parts, "Vue "+chunk.Region.Kind+" region "+chunk.Region.Language+" "+chunk.Region.Quality)
	}
	for index, diagnostic := range chunk.Diagnostics {
		if index == 4 {
			break
		}
		switch diagnostic.Code {
		case "vue_sfc_parse_warning":
			parts = append(parts, "Vue SFC block descriptor warning; original source retained")
		case "vue_sfc_duplicate_block":
			parts = append(parts, "Vue SFC duplicate top-level block warning; original source retained")
		}
	}
	for _, c := range chunk.Context {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "\n")
}
