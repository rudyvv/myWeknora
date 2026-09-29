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

// LanguageForPath selects only deployed source grammars. No project plugins run.
func LanguageForPath(logicalPath string) string {
	switch strings.ToLower(path.Ext(logicalPath)) {
	case ".java":
		return "java"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".mts", ".cts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".xml":
		return "mybatis-xml"
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
	language := LanguageForPath(path)
	if language == "" {
		return nil, fmt.Errorf("source language is not supported")
	}
	codec, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		return nil, fmt.Errorf("source tokenizer unavailable")
	}
	hash := sha256.Sum256(raw)
	digest := hex.EncodeToString(hash[:])
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	// The library budgets bytes. Verify the actual index text with the existing
	// BPE tokenizer and reduce the library budget if it exceeds 2,000 tokens.
	for budget := 4096; budget >= 64; budget /= 2 {
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
			"java_mapper_method":  true,
			"java_annotation_sql": true, "mybatis_mapper": true, "mybatis_statement": true,
			"mybatis_result_map": true, "mybatis_sql_fragment": true, "mybatis_include": true,
			"sql_table": true,
		}
		for _, fact := range parsed.Facts {
			if !allowedFacts[fact.Kind] || !validSourceRange(raw, fact.Range) || fact.Quality == "" {
				return nil, fmt.Errorf("source parser returned invalid static fact coordinates")
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
			for _, annotation := range symbol.Annotations {
				if !validSourceRange(raw, annotation.Range) || annotation.Range.StartByte < symbol.Range.StartByte || annotation.Range.EndByte > symbol.Range.EndByte || string(raw[annotation.Range.StartByte:annotation.Range.EndByte]) != annotation.Text {
					return nil, fmt.Errorf("source parser returned invalid annotation coordinates")
				}
			}
		}
		cursor, oversized := 0, false
		for _, chunk := range parsed.Chunks {
			span := chunk.Range
			if span.StartByte != cursor || span.EndByte <= cursor || span.EndByte > len(raw) || !utf8.Valid(raw[span.StartByte:span.EndByte]) || string(raw[span.StartByte:span.EndByte]) != chunk.Content {
				return nil, fmt.Errorf("source parser returned invalid original coordinates")
			}
			if !validSourceLines(raw, span) {
				return nil, fmt.Errorf("source parser returned invalid original line numbers")
			}
			for _, context := range chunk.Context {
				p := context.Range
				if p.StartByte < 0 || p.EndByte < p.StartByte || p.EndByte > len(raw) || string(raw[p.StartByte:p.EndByte]) != context.Text || !validSourceLines(raw, p) {
					return nil, fmt.Errorf("source parser returned invalid context coordinates")
				}
			}
			ids, _, e := codec.Encode(SourceIndexText(path, chunk))
			if e != nil {
				return nil, fmt.Errorf("source tokenization failed")
			}
			oversized = oversized || len(ids) > 2000
			cursor = span.EndByte
		}
		if cursor != len(raw) {
			return nil, fmt.Errorf("source parser returned an incomplete file")
		}
		if !oversized {
			return &parsed, nil
		}
	}
	return nil, fmt.Errorf("source context exceeds the index token budget")
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

// SourceIndexText is derived index text, not a contiguous original fragment.
// Only Chunk.Content and separately ranged context may be shown as code evidence.
func SourceIndexText(path string, chunk types.ParsedSourceChunk) string {
	parts := []string{path, strings.Join(chunk.Symbols, " ")}
	for _, c := range chunk.Context {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "\n") + "\n" + chunk.Content
}
