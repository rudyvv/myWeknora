package service

import (
	"errors"
	"fmt"
	"testing"
)

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
