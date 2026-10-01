package source

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/tiktoken-go/tokenizer"
)

const (
	SourceIndexTokenCeiling = 2000
	SourceIndexTokenMargin  = 16
	SourceMinimumChunkBytes = 64
	// Current embedder constructors default an unset truncate_prompt_tokens to
	// 511. Source chunks stay below that provider-side truncation as well as the
	// explicitly configured hard model limit.
	defaultProviderTruncatePromptTokens = 511
)

var sourceTokenizers = map[string]tokenizer.Encoding{
	"cl100k_base": tokenizer.Cl100kBase,
	"o200k_base":  tokenizer.O200kBase,
	"p50k_base":   tokenizer.P50kBase,
	"p50k_edit":   tokenizer.P50kEdit,
	"r50k_base":   tokenizer.R50kBase,
}

// IndexProfile is the explicit tokenization contract used only by source
// indexing. MaxTokens is the usable per-chunk limit after safety caps/margin.
type IndexProfile struct {
	Tokenizer tokenizer.Encoding
	MaxTokens int
	Identity  string
}

// NewIndexProfile requires a model-specific tokenizer and hard input limit.
// It also stays below the source pipeline's current ceiling and the embedding
// adapters' configured/default truncation point, so the provider cannot
// silently cut a source chunk after local validation.
func NewIndexProfile(parameters types.EmbeddingParameters) (IndexProfile, error) {
	name := strings.ToLower(strings.TrimSpace(parameters.Tokenizer))
	encoding, ok := sourceTokenizers[name]
	if !ok {
		if name == "" {
			return IndexProfile{}, fmt.Errorf("source embedding model must configure a supported tokenizer")
		}
		return IndexProfile{}, fmt.Errorf("source embedding tokenizer %q is unsupported", name)
	}
	if parameters.MaxInputTokens <= 0 {
		return IndexProfile{}, fmt.Errorf("source embedding model must configure max_input_tokens")
	}
	if _, err := tokenizer.Get(encoding); err != nil {
		return IndexProfile{}, fmt.Errorf("source embedding tokenizer %q is unavailable", name)
	}

	providerLimit := parameters.TruncatePromptTokens
	if providerLimit <= 0 {
		providerLimit = defaultProviderTruncatePromptTokens
	}
	effectiveLimit := parameters.MaxInputTokens
	if effectiveLimit > SourceIndexTokenCeiling {
		effectiveLimit = SourceIndexTokenCeiling
	}
	if effectiveLimit > providerLimit {
		effectiveLimit = providerLimit
	}
	usableLimit := effectiveLimit - SourceIndexTokenMargin
	if usableLimit <= 0 {
		return IndexProfile{}, fmt.Errorf("source embedding input limit must exceed the %d-token safety margin", SourceIndexTokenMargin)
	}
	identity := fmt.Sprintf("%s:max=%d:truncate=%d:usable=%d", name, parameters.MaxInputTokens,
		parameters.TruncatePromptTokens, usableLimit)
	return IndexProfile{Tokenizer: encoding, MaxTokens: usableLimit, Identity: identity}, nil
}

// CheckPreviewIndexFeasibility checks only deterministic lower-bound failures
// that can be decided without invoking the parser: the required path header,
// and bounded text-fallback bodies at the worker's minimum 64-byte chunk size.
// It is not a substitute for ParseFileWithProfile during sync.
func CheckPreviewIndexFeasibility(path string, raw []byte, profile IndexProfile) error {
	if profile.MaxTokens <= 0 {
		return fmt.Errorf("source index token budget is invalid")
	}
	codec, err := tokenizer.Get(profile.Tokenizer)
	if err != nil {
		return fmt.Errorf("source tokenizer unavailable")
	}
	headerTokens, err := sourceIndexHeaderTokenCount(codec, path, types.ParsedSourceChunk{})
	if err != nil {
		return fmt.Errorf("source tokenization failed")
	}
	if headerTokens > profile.MaxTokens {
		return fmt.Errorf("source index header exceeds the embedding model token budget after removing trace context")
	}
	if LanguageForPath(path) != "text" {
		return nil
	}
	for _, span := range boundedTextPreviewRanges(raw, SourceMinimumChunkBytes) {
		chunk := types.ParsedSourceChunk{Content: string(raw[span[0]:span[1]])}
		_, bodyTokens, indexTokens, err := sourceIndexTokenCounts(codec, path, chunk)
		if err != nil {
			return fmt.Errorf("source tokenization failed")
		}
		if bodyTokens > profile.MaxTokens || indexTokens > profile.MaxTokens {
			return fmt.Errorf("source %d-byte bounded body exceeds the embedding model token budget", SourceMinimumChunkBytes)
		}
	}
	return nil
}

// boundedTextPreviewRanges mirrors the parser worker's bounded text-fallback
// cuts so preflight evaluates the same minimum-size body segments without
// running the parser over repository contents.
func boundedTextPreviewRanges(raw []byte, maximum int) [][2]int {
	var ranges [][2]int
	for start := 0; start < len(raw); {
		cut := start + maximum
		if cut > len(raw) {
			cut = len(raw)
		}
		if cut < len(raw) {
			for cut > start && raw[cut]&0xc0 == 0x80 {
				cut--
			}
			if cut <= start {
				cut = start + maximum
				if cut > len(raw) {
					cut = len(raw)
				}
				for cut < len(raw) && raw[cut]&0xc0 == 0x80 {
					cut++
				}
			}
			if newline := bytes.LastIndexByte(raw[start:cut], '\n'); newline >= maximum/2 {
				cut = start + newline + 1
			}
		}
		if cut <= start {
			break
		}
		ranges = append(ranges, [2]int{start, cut})
		start = cut
	}
	return ranges
}
