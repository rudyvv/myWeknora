package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApprovedPreviewMismatchCannotResumeOrSync(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datasource/"+sourceID+"/source-preview" {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"project_id": "402", "branch": "master", "commit_sha": approvedCommit, "can_sync": true, "files": []any{}}})
			return
		}
		writes++
		w.WriteHeader(500)
	}))
	defer server.Close()
	expected := approvedScope{Commit: approvedCommit, Files: []approvedFile{{Path: "synthetic.java", Blob: "synthetic", Size: 8}}, Count: 1, Bytes: 8}
	result, err := publish(context.Background(), server.Client(), server.URL, "synthetic-token", expected, false, "", nil)
	if result.SelectedFiles != 0 || result.SelectedBytes != 0 {
		t.Fatal("failed preview reported approved plan values as observed counts")
	}
	if err == nil || writes != 0 {
		t.Fatalf("mismatch permitted writes: error=%v writes=%d", err, writes)
	}
}

func TestPreviewPreservesExactApprovedScopeWithoutMutations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/datasource/"+sourceID+"/source-preview" {
			t.Error("unexpected mutation")
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"project_id": "402", "branch": "master", "commit_sha": approvedCommit, "can_sync": true, "files": []any{map[string]any{"path": "synthetic.java", "blob_sha": "synthetic", "size": 8, "status": "included"}}}})
	}))
	defer server.Close()
	expected := approvedScope{Commit: approvedCommit, Files: []approvedFile{{Path: "synthetic.java", Blob: "synthetic", Size: 8}}, Count: 1, Bytes: 8}
	result, err := publish(context.Background(), server.Client(), server.URL, "synthetic-token", expected, true, "", nil)
	if err != nil || result.Status != "preview_complete" || result.SelectedFiles != 1 || result.SelectedBytes != 8 {
		t.Fatalf("preview result=%+v error=%v", result, err)
	}
}

func TestWatchAcceptsOnlyCompletePublicationInApprovedScope(t *testing.T) {
	logID := "11111111-1111-4111-8111-111111111111"
	for _, wrongTenant := range []bool{false, true} {
		t.Run(map[bool]string{false: "correct publication", true: "wrong tenant"}[wrongTenant], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("watch mutated source")
					w.WriteHeader(500)
					return
				}
				tenant := uint64(10000)
				if wrongTenant {
					tenant = 20000
				}
				response := map[string]any{"id": logID, "tenant_id": 10000, "data_source_id": sourceID, "status": "success", "result": map[string]any{"source": map[string]any{
					"snapshot":  map[string]any{"id": "22222222-2222-4222-8222-222222222222", "tenant_id": tenant, "knowledge_base_id": kbID, "data_source_id": sourceID, "sync_log_id": logID, "commit_sha": approvedCommit, "state": "published", "manifest_complete": true, "file_count": 1, "member_count": 1, "chunk_count": 2},
					"telemetry": map[string]any{"selected_bytes": 8, "phase_duration_ms": map[string]int64{"fetching": 100}, "model_usage": map[string]int64{"embedding_calls": 1, "estimated_input_tokens": 4}},
				}}}
				json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			result, err := publish(context.Background(), server.Client(), server.URL, "synthetic-token", approvedScope{Commit: approvedCommit, Count: 1, Bytes: 8}, false, logID, nil)
			if wrongTenant {
				if err == nil {
					t.Fatal("foreign tenant publication accepted")
				}
				return
			}
			if err != nil || result.Status != "published_verified" || result.Chunks != 2 || result.EmbeddingCalls == nil || *result.EmbeddingCalls != 1 {
				t.Fatalf("publication=%+v error=%v", result, err)
			}
		})
	}
}
