package source

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestNewIndexProfileRequiresAndBoundsConfiguredTokenizer(t *testing.T) {
	tests := []struct {
		name       string
		parameters types.EmbeddingParameters
		wantLimit  int
		wantError  bool
	}{
		{
			name: "model limit and provider truncation default are both respected",
			parameters: types.EmbeddingParameters{
				Tokenizer: "cl100k_base", MaxInputTokens: 8192,
			},
			wantLimit: 495,
		},
		{
			name: "configured provider truncation is a stricter cap",
			parameters: types.EmbeddingParameters{
				Tokenizer: "o200k_base", MaxInputTokens: 8192, TruncatePromptTokens: 300,
			},
			wantLimit: 284,
		},
		{
			name: "model limit below both ceilings wins",
			parameters: types.EmbeddingParameters{
				Tokenizer: "p50k_base", MaxInputTokens: 100,
			},
			wantLimit: 84,
		},
		{
			name:       "tokenizer is required",
			parameters: types.EmbeddingParameters{MaxInputTokens: 8192},
			wantError:  true,
		},
		{
			name:       "unsupported tokenizer fails closed",
			parameters: types.EmbeddingParameters{Tokenizer: "provider-native", MaxInputTokens: 8192},
			wantError:  true,
		},
		{
			name:       "hard model input limit is required",
			parameters: types.EmbeddingParameters{Tokenizer: "cl100k_base"},
			wantError:  true,
		},
		{
			name:       "limit must leave room for safety margin",
			parameters: types.EmbeddingParameters{Tokenizer: "cl100k_base", MaxInputTokens: 16},
			wantError:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := NewIndexProfile(tt.parameters)
			if (err != nil) != tt.wantError {
				t.Fatalf("NewIndexProfile() error = %v, wantError %v", err, tt.wantError)
			}
			if err != nil {
				return
			}
			if profile.MaxTokens != tt.wantLimit {
				t.Fatalf("profile.MaxTokens = %d, want %d", profile.MaxTokens, tt.wantLimit)
			}
			if profile.Identity == "" {
				t.Fatal("profile identity is empty")
			}
		})
	}
}

func TestIndexProfileIdentityChangesWithTokenizerOrModelLimit(t *testing.T) {
	base := types.EmbeddingParameters{Tokenizer: "cl100k_base", MaxInputTokens: 8192}
	profile, err := NewIndexProfile(base)
	if err != nil {
		t.Fatal(err)
	}
	changedTokenizer, err := NewIndexProfile(types.EmbeddingParameters{Tokenizer: "o200k_base", MaxInputTokens: 8192})
	if err != nil {
		t.Fatal(err)
	}
	changedLimit, err := NewIndexProfile(types.EmbeddingParameters{Tokenizer: "cl100k_base", MaxInputTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Identity == changedTokenizer.Identity || profile.Identity == changedLimit.Identity {
		t.Fatal("changing the tokenizer or model limit must invalidate source parse artifacts")
	}
}

func TestIndexProfileCountTokensUsesConfiguredTokenizer(t *testing.T) {
	profile, err := NewIndexProfile(types.EmbeddingParameters{Tokenizer: "cl100k_base", MaxInputTokens: 8192})
	if err != nil {
		t.Fatal(err)
	}
	got, err := profile.CountTokens("hello source index")
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	if got == 0 {
		t.Fatal("CountTokens() returned no tokens for non-empty input")
	}
}
