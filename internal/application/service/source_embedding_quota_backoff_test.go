package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSourceQuotaRetryDoesNotReembedSuccessfulTexts(t *testing.T) {
	oldBackoff := sourceEmbeddingQuotaBackoff
	sourceEmbeddingQuotaBackoff = time.Millisecond
	t.Cleanup(func() { sourceEmbeddingQuotaBackoff = oldBackoff })
	firstDone := make(chan struct{})
	var mu sync.Mutex
	calls := map[string]int{}
	model := &sourceDispatchTestModel{batchEmbed: func(ctx context.Context, texts []string) ([][]float32, error) {
		text := texts[0]
		if text == "quota-limited" {
			select {
			case <-firstDone:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		mu.Lock()
		defer mu.Unlock()
		calls[text]++
		if text == "accepted" {
			if calls[text] != 1 {
				return nil, errors.New("previously accepted text was embedded again")
			}
			close(firstDone)
			return [][]float32{{1}}, nil
		}
		if calls[text] == 1 {
			return nil, errors.New("ModelAccountTpmRateLimitExceeded")
		}
		return [][]float32{{2}}, nil
	}}
	vectors, err := sourceEmbeddingBatchWithQuotaBackoff(context.Background(), context.Background(), "volcengine", model, []string{"accepted", "quota-limited"})
	if err != nil {
		t.Fatalf("quota recovery must preserve already accepted vectors: %v", err)
	}
	if len(vectors) != 2 || len(vectors[0]) != 1 || vectors[0][0] != 1 || len(vectors[1]) != 1 || vectors[1][0] != 2 {
		t.Fatalf("recovered vectors must retain input order: %v", vectors)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["accepted"] != 1 || calls["quota-limited"] != 2 {
		t.Fatalf("only the quota-limited text may consume another provider call: %v", calls)
	}
}

func TestSourceEmbeddingRateLimitedClassifier(t *testing.T) {
	volcengineQuota := fmt.Errorf("API error: ModelAccountTpmRateLimitExceeded - TPM (Tokens Per Minute) limit of the model is exceeded")
	volcengineRpmQuota := fmt.Errorf("API error: ModelAccountRpmRateLimitExceeded - RPM limit exceeded")
	volcengineStatusOnly := fmt.Errorf("BatchEmbed API error: Http Status %s", "429 Too Many Requests")
	openAIQuota := fmt.Errorf("EmbedBatch API error: Http Status %s, Response: %s", "429 Too Many Requests", `{"error":{"message":"rate limit exceeded, please retry later"}}`)
	for _, err := range []error{volcengineQuota, volcengineRpmQuota, volcengineStatusOnly, openAIQuota} {
		if !sourceEmbeddingRateLimited(err) {
			t.Fatalf("quota-window error must classify as rate limited: %v", err)
		}
	}
	for _, err := range []error{
		errors.New("API error: InvalidApiKey - unauthorized"),
		errors.New("EmbedBatch API error: Http Status 400 Bad Requests, Response: input too long"),
		errors.New("source embedding result is incomplete"),
		errors.New("connection refused"),
	} {
		if sourceEmbeddingRateLimited(err) {
			t.Fatalf("permanent error must not classify as rate limited: %v", err)
		}
	}
	if sourceEmbeddingRateLimited(nil) {
		t.Fatal("nil error must not classify as rate limited")
	}
}
