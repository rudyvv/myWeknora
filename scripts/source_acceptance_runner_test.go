package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const acceptanceTestToken = "test-token-never-print"

type acceptanceFixture struct {
	wrongSnapshot     bool
	denySourceRead    bool
	syncTimeout       bool
	parserNotReady    bool
	chainEvidence     bool
	badExtraHit       string
	badHitPath        string
	badHitMetric      string
	agentStream       string
	publishSuccess    bool
	publishSnapshotID string
	sourceContent     string
	sourceSHA         string
	logOffsets        []int
	searchCalls       int
	sourceReadCalls   int
	agentCalls        int
}

func (f *acceptanceFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "Bearer "+acceptanceTestToken {
		http.Error(w, `{"error":"invalid test credentials"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/datasource/source-1" || r.URL.Path == "/api/v1/datasource/source-2"):
		sourceID := strings.TrimPrefix(r.URL.Path, "/api/v1/datasource/")
		writeFixtureJSON(w, map[string]any{"id": sourceID, "type": "gitlab", "knowledge_base_id": "kb-1", "config": map[string]any{"settings": map[string]any{"content_mode": "source", "projects": []any{map[string]any{"project_id": "42", "paths": []any{"src"}}}}}, "source_lifecycle": map[string]any{"binding_state": "bound", "query_enabled": true}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge-bases/kb-1":
		writeFixtureJSON(w, map[string]any{"success": true, "data": map[string]any{"id": "kb-1", "embedding_model_id": "model-1"}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/models/model-1":
		writeFixtureJSON(w, map[string]any{"success": true, "data": map[string]any{"id": "model-1", "type": "Embedding", "parameters": map[string]any{"embedding_parameters": map[string]any{"tokenizer": "cl100k_base", "max_input_tokens": 8192}}}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/datasource/source-1/sync":
		logID := "log-1"
		if f.publishSuccess {
			logID = "sync-new-log"
		}
		writeFixtureJSON(w, map[string]any{"id": logID, "data_source_id": "source-1", "status": "running"})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/datasource/logs/sync-new-log" && f.publishSuccess:
		writeFixtureJSON(w, publishedTestLogAt("source-1", "sync-new-log", f.publishSnapshotID))
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/datasource/logs/log-1" && f.syncTimeout:
		writeFixtureJSON(w, map[string]any{"id": "log-1", "data_source_id": "source-1", "status": "running"})
	case r.Method == http.MethodPost && (r.URL.Path == "/api/v1/datasource/source-1/source-preview" || r.URL.Path == "/api/v1/datasource/source-2/source-preview"):
		sourceID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/datasource/"), "/source-preview")
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, `{"error":"invalid test body"}`, http.StatusBadRequest)
			return
		}
		settings, _ := request["settings"].(map[string]any)
		if settings["content_mode"] != "source" {
			http.Error(w, `{"error":"source settings were not forwarded"}`, http.StatusBadRequest)
			return
		}
		parserReady, canSync := !f.parserNotReady, !f.parserNotReady
		_, commit, _, _ := fixtureSourceIdentity(sourceID)
		writeFixtureJSON(w, map[string]any{"data": map[string]any{
			"project_id": "42", "branch": "main", "commit_sha": commit, "rules_version": "rules-v1", "can_sync": canSync,
			"files":  []any{map[string]any{"path": "src/Foo.java", "blob_sha": "blob-1", "size": len(testSourceContent), "status": "included"}},
			"checks": []any{map[string]any{"name": "parser", "ready": parserReady, "message": "ready"}, map[string]any{"name": "indexes", "ready": true, "message": "ready"}, map[string]any{"name": "source_pipeline", "ready": canSync, "message": "ready"}},
		}})
	case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/datasource/source-1/logs" || r.URL.Path == "/api/v1/datasource/source-2/logs"):
		sourceID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/datasource/"), "/logs")
		offset, _ := url.QueryUnescape(r.URL.Query().Get("offset"))
		var value int
		_, _ = fmt.Sscan(offset, &value)
		f.logOffsets = append(f.logOffsets, value)
		if value == 0 {
			logs := make([]any, 100)
			for i := range logs {
				logs[i] = map[string]any{"id": fmt.Sprintf("old-%d", i), "status": "failed"}
			}
			writeFixtureJSON(w, logs)
		} else {
			writeFixtureJSON(w, []any{publishedTestLog(sourceID)})
		}
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-1/hybrid-search":
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, `{"error":"invalid test body"}`, http.StatusBadRequest)
			return
		}
		ids, _ := request["source_ids"].([]any)
		isSingleSource := len(ids) == 1 && ids[0] == "source-1"
		isCrossRepository := len(ids) == 2 && ids[0] == "source-1" && ids[1] == "source-2"
		if (!isSingleSource && !isCrossRepository) || request["match_count"] != float64(10) {
			http.Error(w, `{"error":"search scope or top-k was not explicit"}`, http.StatusBadRequest)
			return
		}
		f.searchCalls++
		hits := make([]any, 10)
		for i := range hits {
			path, version, knowledgeID, startLine, endLine := "src/Other.java", "version-other", "file-other", 1, 3
			if i == 0 {
				path, version, knowledgeID, startLine, endLine = "src/Foo.java", "version-1", "file-1", 2, 2
				if f.wrongSnapshot {
					// The first hit is deliberately from a different publication.
				}
			}
			sourceID := "source-1"
			if isCrossRepository && f.chainEvidence && i == 1 {
				path, version, knowledgeID, startLine, endLine = "src/Foo.java", "version-1", "file-1", 3, 3
				sourceID = "source-2"
			}
			snapshotID, commit, _, _ := fixtureSourceIdentity(sourceID)
			if sourceID == "source-1" && f.publishSnapshotID != "" {
				snapshotID = f.publishSnapshotID
			}
			if f.wrongSnapshot && i == 0 {
				snapshotID = "snapshot-old"
			}
			if i == 1 {
				switch f.badExtraHit {
				case "snapshot":
					snapshotID = "snapshot-staging"
				case "version":
					version = "version-staging"
				case "range":
					startLine, endLine = 99, 100
				}
				if f.badHitPath != "" {
					path = f.badHitPath
				}
			}
			if sourceID == "source-2" {
				version, knowledgeID = "version-2", "file-2"
			}
			hits[i] = map[string]any{
				"score":          0.75,
				"match_type":     0,
				"knowledge_id":   knowledgeID,
				"metadata":       map[string]string{"datasource_id": sourceID, "source_snapshot_id": snapshotID, "commit_sha": commit, "source_path": path, "source_file_version_id": version},
				"chunk_metadata": map[string]any{"source": map[string]any{"range": map[string]any{"start_line": startLine, "end_line": endLine}}},
			}
			if i == 1 {
				switch f.badHitMetric {
				case "score_string":
					hits[i].(map[string]any)["score"] = "score-type-marker"
				case "match_type_string":
					hits[i].(map[string]any)["match_type"] = "match-type-marker"
				case "match_type_unknown":
					hits[i].(map[string]any)["match_type"] = 99
				}
			}
		}
		writeFixtureJSON(w, map[string]any{"success": true, "data": hits})
	case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/knowledge/file-1/source" || r.URL.Path == "/api/v1/knowledge/file-2/source" || r.URL.Path == "/api/v1/knowledge/file-other/source"):
		f.sourceReadCalls++
		if f.denySourceRead {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"sensitive-error-marker"}`))
			return
		}
		sourceID, knowledgeID, versionID, path := "source-1", "file-1", "version-1", "src/Foo.java"
		if r.URL.Path == "/api/v1/knowledge/file-2/source" {
			sourceID, knowledgeID, versionID = "source-2", "file-2", "version-2"
		} else if r.URL.Path == "/api/v1/knowledge/file-other/source" {
			knowledgeID, versionID, path = "file-other", "version-other", "src/Other.java"
		}
		if r.URL.Query().Get("version_id") != versionID {
			http.Error(w, `{"error":"version is not a published member"}`, http.StatusNotFound)
			return
		}
		snapshotID, commit, _, _ := fixtureSourceIdentity(sourceID)
		if sourceID == "source-1" && f.publishSnapshotID != "" {
			snapshotID = f.publishSnapshotID
		}
		content, digest := otherSourceContent, otherSourceSHA
		if path == "src/Foo.java" {
			content, digest = testSourceContent, testSourceSHA
		}
		if f.sourceContent != "" {
			content = f.sourceContent
		}
		if f.sourceSHA != "" {
			digest = f.sourceSHA
		}
		writeFixtureJSON(w, map[string]any{"success": true, "data": map[string]any{
			"knowledge_id": knowledgeID, "data_source_id": sourceID, "snapshot_id": snapshotID, "file_version_id": versionID, "commit_sha": commit, "path": path, "sha256": digest, "parser_version": "source-pack-test", "content": content,
		}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent-chat/session-1":
		f.agentCalls++
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil || request["agent_id"] != "agent-1" || request["agent_enabled"] != true {
			http.Error(w, `{"error":"invalid agent request"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		stream := f.agentStream
		if stream == "" {
			stream = "data: {\"response_type\":\"answer\"}\n\ndata: {\"response_type\":\"complete\",\"done\":true}\n\n"
		}
		_, _ = w.Write([]byte(stream))
	default:
		http.NotFound(w, r)
	}
}

const testSourceContent = "class Foo {\n void getPushSchedule() {}\n}\n"
const otherSourceContent = "package Other {\n func fallback() {}\n}\n"

var testSourceSHA = func() string {
	digest := sha256.Sum256([]byte(testSourceContent))
	return hex.EncodeToString(digest[:])
}()

var otherSourceSHA = func() string {
	digest := sha256.Sum256([]byte(otherSourceContent))
	return hex.EncodeToString(digest[:])
}()

func fixtureSourceIdentity(sourceID string) (snapshotID, commit, knowledgeID, versionID string) {
	if sourceID == "source-2" {
		return "snapshot-2", "commit-2", "file-2", "version-2"
	}
	return "snapshot-1", "commit-1", "file-1", "version-1"
}

func publishedTestLog(sourceID string) map[string]any {
	snapshotID, _, _, _ := fixtureSourceIdentity(sourceID)
	logID := "log-1"
	if sourceID == "source-2" {
		logID = "log-2"
	}
	return publishedTestLogAt(sourceID, logID, snapshotID)
}

func publishedTestLogAt(sourceID, logID, snapshotID string) map[string]any {
	_, commit, _, _ := fixtureSourceIdentity(sourceID)
	previousSnapshotID := "snapshot-0"
	if sourceID == "source-2" {
		previousSnapshotID = "snapshot-1"
	}
	return map[string]any{
		"id": logID, "status": "success", "data_source_id": sourceID,
		"result": map[string]any{"source": map[string]any{"snapshot": map[string]any{
			"id": snapshotID, "data_source_id": sourceID, "knowledge_base_id": "kb-1", "commit_sha": commit, "state": "published", "previous_snapshot_id": previousSnapshotID, "file_count": 1,
		}, "telemetry": map[string]any{
			"schema_version": 1, "selected_bytes": len(testSourceContent),
			"phase_duration_ms": map[string]int{"fetching": 12, "parsing": 34, "indexing": 56, "publishing": 7, "unknown_phase": 99},
			"model_usage":       map[string]any{"embedding_calls": 1, "estimated_input_tokens": 23, "provider_response": "telemetry-secret-marker"},
			"unknown_extension": "telemetry-secret-marker",
		}}},
	}
}

func writeFixtureJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestSourceAcceptanceRunnerChecksParserPublicationScopeAndAuthorizedEvidence(t *testing.T) {
	fixture := &acceptanceFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()

	result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken, "-AgentSessionId", "session-1", "-AgentId", "agent-1", "-AgentQuestion", "Where is getPushSchedule implemented?")
	if result.ExitCode != 0 {
		t.Fatalf("runner failed: exit=%d stdout=%s stderr=%s", result.ExitCode, result.Stdout, result.Stderr)
	}
	if !strings.Contains(report, `"schema_version": 1`) || !strings.Contains(report, `"parser_version"`) || !strings.Contains(report, `"ready": true`) || !strings.Contains(report, `"snapshot_id": "snapshot-1"`) || !strings.Contains(report, `"matched_count": 30`) || !strings.Contains(report, `"status": "matched"`) {
		t.Fatalf("report is missing required v1 evidence fields: %s", report)
	}
	if strings.Contains(report, testSourceContent) || strings.Contains(report, otherSourceContent) || strings.Contains(result.Stdout+result.Stderr+report, acceptanceTestToken) {
		t.Fatal("runner exposed source text or the process credential")
	}
	if strings.Contains(report, "telemetry-secret-marker") || strings.Contains(report, "unknown_phase") || !strings.Contains(report, `"fetching": 12`) {
		t.Fatal("runner did not constrain telemetry to the typed, allowlisted metrics")
	}
	if fixture.searchCalls != 30 || fixture.agentCalls != 1 {
		t.Fatalf("expected 30 scoped searches and one UI agent request, got searches=%d agent=%d", fixture.searchCalls, fixture.agentCalls)
	}
	if fixture.sourceReadCalls != 300 {
		t.Fatalf("expected a fixed-version authorized source read for all 300 top-10 hits, got %d", fixture.sourceReadCalls)
	}
	var reportEnvelope struct {
		SourceReadValidation struct {
			AuthorizedReadCount  int `json:"authorized_read_count"`
			MetadataCacheEntries int `json:"metadata_cache_entries"`
			MetadataCacheBytes   int `json:"metadata_cache_bytes"`
			MaxCacheEntries      int `json:"max_cache_entries"`
			MaxCacheBytes        int `json:"max_cache_bytes"`
		} `json:"source_read_validation"`
		AgentUISmoke struct {
			Status             string   `json:"status"`
			EventTypes         []string `json:"event_types"`
			AnswerTextRecorded bool     `json:"answer_text_recorded"`
		} `json:"agent_ui_smoke"`
	}
	if err := json.Unmarshal([]byte(report), &reportEnvelope); err != nil {
		t.Fatalf("could not decode source-read cache bounds: %v", err)
	}
	readValidation := reportEnvelope.SourceReadValidation
	if readValidation.AuthorizedReadCount != 300 || readValidation.MetadataCacheEntries > 300 || readValidation.MetadataCacheBytes > 524288 || readValidation.MaxCacheEntries != 300 || readValidation.MaxCacheBytes != 524288 {
		t.Fatalf("source validation cache exceeded or misreported its metadata-only bounds: %+v", readValidation)
	}
	if reportEnvelope.AgentUISmoke.Status != "completed" || len(reportEnvelope.AgentUISmoke.EventTypes) != 2 || reportEnvelope.AgentUISmoke.EventTypes[0] != "answer" || reportEnvelope.AgentUISmoke.EventTypes[1] != "complete" || reportEnvelope.AgentUISmoke.AnswerTextRecorded {
		t.Fatalf("agent smoke did not require and report the allowlisted terminal stream event safely: %+v", reportEnvelope.AgentUISmoke)
	}
	if !strings.Contains(report, `"t22_acceptance_status": "unknown"`) {
		t.Fatal("a completed retrieval run incorrectly decided the whole T22 gate")
	}
	if len(fixture.logOffsets) != 2 || fixture.logOffsets[0] != 0 || fixture.logOffsets[1] != 100 {
		t.Fatalf("published-run lookup did not paginate logs: offsets=%v", fixture.logOffsets)
	}
}

func TestSourceAcceptanceRunnerRejectsInvalidOrIncompleteAgentStreams(t *testing.T) {
	tests := []struct {
		name          string
		stream        string
		wantErrorCode string
		wantMarker    string
	}{
		{
			name:          "unknown response type",
			stream:        "data: {\"response_type\":\"response-type-marker\",\"content\":\"answer-marker\"}\n\n",
			wantErrorCode: "agent_ui_event_invalid",
			wantMarker:    "response-type-marker",
		},
		{
			name:          "unknown event type",
			stream:        "data: {\"response_type\":\"answer\",\"type\":\"event-type-marker\"}\n\n",
			wantErrorCode: "agent_ui_event_invalid",
			wantMarker:    "event-type-marker",
		},
		{
			name:          "stream error",
			stream:        "data: {\"response_type\":\"error\",\"content\":\"agent-error-marker\"}\n\n",
			wantErrorCode: "agent_ui_stream_error",
			wantMarker:    "agent-error-marker",
		},
		{
			name:          "empty stream",
			stream:        "\n\n",
			wantErrorCode: "agent_ui_empty_stream",
		},
		{
			name:          "partial answer without terminal event",
			stream:        "data: {\"response_type\":\"thinking\"}\n\ndata: {\"response_type\":\"answer\",\"content\":\"partial-answer-marker\"}\n\n",
			wantErrorCode: "agent_ui_incomplete_stream",
			wantMarker:    "partial-answer-marker",
		},
		{
			name:          "truncated event",
			stream:        "data: {\"response_type\":\"answer\"",
			wantErrorCode: "agent_ui_invalid_event",
			wantMarker:    "response_type",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := &acceptanceFixture{agentStream: test.stream}
			server := httptest.NewServer(fixture)
			defer server.Close()

			result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken, "-AgentSessionId", "session-1", "-AgentId", "agent-1", "-AgentQuestion", "Where is getPushSchedule implemented?")
			if result.ExitCode == 0 || !strings.Contains(report, test.wantErrorCode) {
				t.Fatalf("runner accepted an invalid or incomplete Agent stream: exit=%d want=%s report=%s", result.ExitCode, test.wantErrorCode, report)
			}
			if test.wantMarker != "" && strings.Contains(result.Stdout+result.Stderr+report, test.wantMarker) {
				t.Fatalf("runner echoed untrusted Agent stream content %q", test.wantMarker)
			}
		})
	}
}

func TestSourceAcceptanceRunnerRejectsEvidenceFromAnotherSnapshot(t *testing.T) {
	fixture := &acceptanceFixture{wrongSnapshot: true}
	server := httptest.NewServer(fixture)
	defer server.Close()

	result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken)
	if result.ExitCode == 0 {
		t.Fatalf("runner accepted cross-snapshot evidence: %s", report)
	}
	if !strings.Contains(report, `"status": "failed"`) || !strings.Contains(report, "search_scope_violation") {
		t.Fatalf("scope violation was not recorded safely: %s", report)
	}
	if strings.Contains(result.Stdout+result.Stderr+report, acceptanceTestToken) {
		t.Fatal("runner exposed the process credential on a scope failure")
	}
}

func TestSourceAcceptanceRunnerDoesNotEchoDeniedReadDetails(t *testing.T) {
	fixture := &acceptanceFixture{denySourceRead: true}
	server := httptest.NewServer(fixture)
	defer server.Close()

	result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken)
	if result.ExitCode == 0 {
		t.Fatal("runner accepted an unauthorized source-file read")
	}
	if strings.Contains(result.Stdout+result.Stderr+report, acceptanceTestToken) || strings.Contains(result.Stdout+result.Stderr+report, "sensitive-error-marker") {
		t.Fatal("runner echoed credentials or an untrusted API error body")
	}
}

func TestSourceAcceptanceRunnerFailsClosedOnAnyInvalidNonGoldTop10Hit(t *testing.T) {
	tests := []struct {
		name          string
		badHit        string
		wantErrorCode string
		wantReads     int
	}{
		{name: "staging snapshot", badHit: "snapshot", wantErrorCode: "search_scope_violation", wantReads: 1},
		{name: "staging file version", badHit: "version", wantErrorCode: "source_hit_read_failed", wantReads: 2},
		{name: "out of range", badHit: "range", wantErrorCode: "search_hit_range_invalid", wantReads: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := &acceptanceFixture{badExtraHit: test.badHit}
			server := httptest.NewServer(fixture)
			defer server.Close()

			result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken)
			if result.ExitCode == 0 || !strings.Contains(report, test.wantErrorCode) {
				t.Fatalf("runner accepted an invalid non-gold top-10 hit: exit=%d want=%s report=%s", result.ExitCode, test.wantErrorCode, report)
			}
			if fixture.sourceReadCalls != test.wantReads {
				t.Fatalf("unexpected authorized-read count before fail-closed stop: got=%d want=%d", fixture.sourceReadCalls, test.wantReads)
			}
		})
	}
}

func TestSourceAcceptanceRunnerValidatesSearchHitScoreAndMatchType(t *testing.T) {
	tests := []struct {
		name          string
		badHitMetric  string
		wantErrorCode string
		wantMarker    string
	}{
		{name: "score must be numeric", badHitMetric: "score_string", wantErrorCode: "search_hit_score_invalid", wantMarker: "score-type-marker"},
		{name: "match type must be numeric", badHitMetric: "match_type_string", wantErrorCode: "search_hit_match_type_invalid", wantMarker: "match-type-marker"},
		{name: "match type must be a known enum", badHitMetric: "match_type_unknown", wantErrorCode: "search_hit_match_type_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := &acceptanceFixture{badHitMetric: test.badHitMetric}
			server := httptest.NewServer(fixture)
			defer server.Close()

			result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken)
			if result.ExitCode == 0 || !strings.Contains(report, test.wantErrorCode) {
				t.Fatalf("runner accepted an invalid search metric: exit=%d want=%s report=%s", result.ExitCode, test.wantErrorCode, report)
			}
			if test.wantMarker != "" && strings.Contains(result.Stdout+result.Stderr+report, test.wantMarker) {
				t.Fatalf("runner echoed untrusted search metric data %q", test.wantMarker)
			}
			if fixture.sourceReadCalls != 1 {
				t.Fatalf("invalid second-hit metrics should fail before its authorized read: got %d reads", fixture.sourceReadCalls)
			}
		})
	}
}

func TestSourceAcceptanceRunnerRejectsNonCanonicalSearchHitPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "empty segment", path: "path-marker//Foo.java"},
		{name: "dot segment", path: "path-marker/./Foo.java"},
		{name: "parent segment", path: "path-marker/../Foo.java"},
		{name: "posix absolute", path: "/path-marker/Foo.java"},
		{name: "windows drive absolute", path: "C:/path-marker/Foo.java"},
		{name: "backslash", path: `path-marker\Foo.java`},
		{name: "control character", path: "path-marker/\x01Foo.java"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := &acceptanceFixture{badHitPath: test.path}
			server := httptest.NewServer(fixture)
			defer server.Close()

			result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken)
			if result.ExitCode == 0 || !strings.Contains(report, "search_hit_identity_invalid") {
				t.Fatalf("runner accepted a non-canonical Git path: exit=%d report=%s", result.ExitCode, report)
			}
			if strings.Contains(result.Stdout+result.Stderr+report, "path-marker") || fixture.sourceReadCalls != 1 {
				t.Fatalf("runner echoed an invalid path or read it before validation: reads=%d", fixture.sourceReadCalls)
			}
		})
	}
}

func TestSourceAcceptanceRunnerRejectsNonCanonicalExpectedEvidencePath(t *testing.T) {
	fixture := &acceptanceFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	manifest := legacyQuestionManifest("snapshot-1")
	firstQuestion := manifest["questions"].([]any)[0].(map[string]any)
	firstEvidence := firstQuestion["expected"].(map[string]any)
	firstEvidence["path"] = "path-marker/../Foo.java"

	result, report := runSourceAcceptanceWithQuestions(t, server.URL, acceptanceTestToken, manifest, nil)
	if result.ExitCode == 0 || !strings.Contains(report, "question_evidence_invalid") || fixture.searchCalls != 0 {
		t.Fatalf("runner accepted or searched a non-canonical expected evidence path: exit=%d searches=%d report=%s", result.ExitCode, fixture.searchCalls, report)
	}
	if strings.Contains(result.Stdout+result.Stderr+report, "path-marker") {
		t.Fatal("runner echoed the untrusted question evidence path")
	}
}

func TestSourceAcceptanceRunnerRequiresDistinctSourcesForCrossRepositoryQuestion(t *testing.T) {
	fixture := &acceptanceFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	manifest := goldShapedQuestionManifest(true)
	repositories := manifest["repositories"].([]any)
	repositories[1].(map[string]any)["commit"] = "commit-1"
	sourceMap := map[string]any{
		"schema_version": 1, "status": "approved", "knowledge_base_id": "kb-1",
		"mappings": map[string]any{
			"evip_mobile": map[string]any{"source_id": "source-1", "snapshot_id": "snapshot-1", "commit_sha": "commit-1"},
			"nsb":         map[string]any{"source_id": "source-1", "snapshot_id": "snapshot-1", "commit_sha": "commit-1"},
		},
	}
	result, report := runSourceAcceptanceWithQuestions(t, server.URL, acceptanceTestToken, manifest, sourceMap)
	if result.ExitCode == 0 || !strings.Contains(report, "question_cross_repository_mapping_invalid") || fixture.searchCalls != 0 {
		t.Fatalf("runner accepted aliases of one source as a cross-repository chain: exit=%d searches=%d report=%s", result.ExitCode, fixture.searchCalls, report)
	}
}

func TestSourceAcceptanceRunnerKeepsWholeT22StatusUnknown(t *testing.T) {
	fixture := &acceptanceFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken)
	if result.ExitCode != 0 {
		t.Fatalf("retrieval fixture failed: exit=%d report=%s", result.ExitCode, report)
	}
	var document struct {
		Status              string `json:"status"`
		T22AcceptanceStatus string `json:"t22_acceptance_status"`
		IncrementalRuns     []struct {
			Status string `json:"status"`
		} `json:"incremental_runs"`
		TextBaseline struct {
			Status string `json:"status"`
		} `json:"text_baseline"`
		FullRun struct {
			Status       string `json:"status"`
			Completeness string `json:"completeness"`
		} `json:"full_run"`
	}
	if err := json.Unmarshal([]byte(report), &document); err != nil {
		t.Fatalf("could not decode runner report: %v", err)
	}
	if document.Status != "completed" || document.T22AcceptanceStatus != "unknown" || len(document.IncrementalRuns) != 3 || document.TextBaseline.Status != "unknown" || document.FullRun.Status != "unknown" || document.FullRun.Completeness != "unknown" {
		t.Fatalf("retrieval completion was conflated with the whole T22 gate or missing measurements: %+v", document)
	}
	for _, run := range document.IncrementalRuns {
		if run.Status != "unknown" {
			t.Fatalf("missing incremental data was reported as %q", run.Status)
		}
	}
}

func TestSourceAcceptanceRunnerDowngradesEmptyMeasuredRecordsToUnknown(t *testing.T) {
	fixture := &acceptanceFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	measurement := map[string]any{
		"schema_version": 1,
		"incremental_runs": []any{map[string]any{
			"changed_file_count": 1, "status": "measured", "model_identifier": "model-1", "tokenizer": "cl100k_base", "context_limit_tokens": 8192,
			"selected_files": nil, "selected_bytes": nil, "phase_duration_ms": nil, "peak_memory_bytes": nil, "estimated_input_tokens": nil, "actual_input_tokens": nil,
		}},
		"text_baseline": map[string]any{
			"status": "measured", "model_identifier": "model-1", "tokenizer": "cl100k_base", "budget_tokens": 8192,
			"selected_files": nil, "selected_bytes": nil, "elapsed_ms": nil, "peak_memory_bytes": nil, "estimated_input_tokens": nil, "actual_input_tokens": nil,
		},
	}
	measurementPath := writeAcceptanceJSON(t, "measurements.json", measurement)
	result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken, "-MeasurementsFile", measurementPath)
	if result.ExitCode != 0 {
		t.Fatalf("empty optional measurements should remain unknown without failing retrieval: exit=%d report=%s", result.ExitCode, report)
	}
	var document struct {
		T22AcceptanceStatus string `json:"t22_acceptance_status"`
		IncrementalRuns     []struct {
			ChangedFileCount int    `json:"changed_file_count"`
			Status           string `json:"status"`
		} `json:"incremental_runs"`
		TextBaseline struct {
			Status string `json:"status"`
		} `json:"text_baseline"`
	}
	if err := json.Unmarshal([]byte(report), &document); err != nil {
		t.Fatalf("could not decode measurement report: %v", err)
	}
	if document.T22AcceptanceStatus != "unknown" || document.TextBaseline.Status != "unknown" {
		t.Fatalf("empty measurements were treated as complete evidence: %+v", document)
	}
	for _, run := range document.IncrementalRuns {
		if run.Status != "unknown" {
			t.Fatalf("%d-file run missing measured values was reported as %q", run.ChangedFileCount, run.Status)
		}
	}
}

func TestSourceAcceptanceRunnerKeepsPartialMetricsAndMarksEachMissingMetricUnknown(t *testing.T) {
	fixture := &acceptanceFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	measurement := map[string]any{
		"schema_version": 1,
		"incremental_runs": []any{map[string]any{
			"changed_file_count": 1, "status": "measured", "model_identifier": "model-1", "tokenizer": "cl100k_base", "context_limit_tokens": 8192,
			"selected_files": 1, "selected_bytes": nil, "chunk_count": nil, "elapsed_ms": nil, "peak_memory_bytes": nil,
			"phase_duration_ms":      map[string]any{"fetching": 12, "parsing": nil},
			"estimated_input_tokens": 120, "actual_input_tokens": nil, "embedding_calls": nil, "generation_calls": nil,
		}},
		"text_baseline": map[string]any{
			"status": "measured", "model_identifier": "model-1", "tokenizer": "cl100k_base", "budget_tokens": 8192,
			"selected_files": 1, "selected_bytes": 24, "chunk_count": nil, "elapsed_ms": 55, "peak_memory_bytes": nil,
			"phase_duration_ms":      map[string]any{"fetching": 8},
			"estimated_input_tokens": 300, "actual_input_tokens": nil, "embedding_calls": nil, "generation_calls": nil,
		},
	}
	measurementPath := writeAcceptanceJSON(t, "partial-measurements.json", measurement)
	result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken, "-MeasurementsFile", measurementPath)
	if result.ExitCode != 0 {
		t.Fatalf("partial optional measurements should preserve known values without failing retrieval: exit=%d report=%s", result.ExitCode, report)
	}
	var document struct {
		IncrementalRuns []struct {
			ChangedFileCount int    `json:"changed_file_count"`
			Status           string `json:"status"`
			Completeness     string `json:"completeness"`
			MetricStatus     struct {
				SelectedFiles   string            `json:"selected_files"`
				ElapsedMS       string            `json:"elapsed_ms"`
				EstimatedTokens string            `json:"estimated_input_tokens"`
				ActualTokens    string            `json:"actual_input_tokens"`
				PhaseDuration   map[string]string `json:"phase_duration_ms"`
			} `json:"metric_status"`
			Output struct {
				SelectedFiles        *int              `json:"selected_files"`
				ElapsedMS            *int              `json:"elapsed_ms"`
				EstimatedInputTokens *int              `json:"estimated_input_tokens"`
				ActualInputTokens    *int              `json:"actual_input_tokens"`
				PhaseDurationMS      map[string]*int64 `json:"phase_duration_ms"`
			} `json:"output"`
		} `json:"incremental_runs"`
		TextBaseline struct {
			Status       string `json:"status"`
			Completeness string `json:"completeness"`
			MetricStatus struct {
				ElapsedMS        string `json:"elapsed_ms"`
				PeakMemoryBytes  string `json:"peak_memory_bytes"`
				ActualInputToken string `json:"actual_input_tokens"`
			} `json:"metric_status"`
			Outputs struct {
				SelectedFiles        *int `json:"selected_files"`
				ElapsedMS            *int `json:"elapsed_ms"`
				EstimatedInputTokens *int `json:"estimated_input_tokens"`
				ActualInputTokens    *int `json:"actual_input_tokens"`
			} `json:"outputs"`
		} `json:"text_baseline"`
	}
	if err := json.Unmarshal([]byte(report), &document); err != nil {
		t.Fatalf("could not decode partial measurement report: %v", err)
	}
	if len(document.IncrementalRuns) != 3 {
		t.Fatalf("expected all 1/10/100-file scenarios in the report: %+v", document.IncrementalRuns)
	}
	partialIndex := -1
	for index, run := range document.IncrementalRuns {
		if run.ChangedFileCount == 1 {
			partialIndex = index
			break
		}
	}
	if partialIndex < 0 {
		t.Fatalf("expected the partial 1-file measurement in the report: %+v", document.IncrementalRuns)
	}
	partial := document.IncrementalRuns[partialIndex]
	if partial.Status != "partial" || partial.Completeness != "partial" || partial.Output.SelectedFiles == nil || *partial.Output.SelectedFiles != 1 || partial.Output.ElapsedMS != nil || partial.Output.EstimatedInputTokens == nil || *partial.Output.EstimatedInputTokens != 120 || partial.Output.ActualInputTokens != nil || partial.MetricStatus.SelectedFiles != "observed" || partial.MetricStatus.ElapsedMS != "unknown" || partial.MetricStatus.EstimatedTokens != "observed" || partial.MetricStatus.ActualTokens != "unknown" || partial.MetricStatus.PhaseDuration["fetching"] != "observed" || partial.MetricStatus.PhaseDuration["parsing"] != "unknown" || partial.Output.PhaseDurationMS["fetching"] == nil || partial.Output.PhaseDurationMS["parsing"] != nil {
		t.Fatalf("incremental partial metrics were lost or misclassified: %+v", partial)
	}
	if document.TextBaseline.Status != "partial" || document.TextBaseline.Completeness != "partial" || document.TextBaseline.Outputs.ElapsedMS == nil || *document.TextBaseline.Outputs.ElapsedMS != 55 || document.TextBaseline.Outputs.ActualInputTokens != nil || document.TextBaseline.MetricStatus.ElapsedMS != "observed" || document.TextBaseline.MetricStatus.PeakMemoryBytes != "unknown" || document.TextBaseline.MetricStatus.ActualInputToken != "unknown" {
		t.Fatalf("baseline partial metrics were lost or misclassified: %+v", document.TextBaseline)
	}
	for _, run := range document.IncrementalRuns {
		if run.ChangedFileCount != 1 && (run.Status != "unknown" || run.Completeness != "unknown") {
			t.Fatalf("missing %d-file measurement should remain unknown: %+v", run.ChangedFileCount, run)
		}
	}
}

func TestSourceAcceptanceRunnerRejectsNonIntegerMeasurementJSONValues(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "numeric string", value: "55"},
		{name: "boolean", value: true},
		{name: "fraction", value: 55.5},
		{name: "negative", value: -1},
		{name: "int64 overflow", value: uint64(1) << 63},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := &acceptanceFixture{}
			server := httptest.NewServer(fixture)
			defer server.Close()
			measurement := map[string]any{
				"schema_version": 1,
				"incremental_runs": []any{map[string]any{
					"changed_file_count": 1, "status": "measured", "model_identifier": "model-1", "tokenizer": "cl100k_base", "context_limit_tokens": 8192,
					"selected_files": test.value,
				}},
			}
			measurementPath := writeAcceptanceJSON(t, "invalid-measurement.json", measurement)
			result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken, "-MeasurementsFile", measurementPath)
			if result.ExitCode == 0 || !strings.Contains(report, "incremental_metrics_invalid") {
				t.Fatalf("runner accepted a non-integer JSON measurement: exit=%d report=%s", result.ExitCode, report)
			}
			if strings.Contains(report, `"selected_files": "55"`) || fixture.searchCalls != 0 {
				t.Fatal("runner copied an invalid measurement string into the report or continued into retrieval")
			}
		})
	}
}

func TestSourceAcceptanceRunnerRejectsNonFiniteMeasurementJSON(t *testing.T) {
	fixture := &acceptanceFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	raw := `{"schema_version":1,"incremental_runs":[{"changed_file_count":1,"status":"measured","model_identifier":"model-1","tokenizer":"cl100k_base","context_limit_tokens":8192,"elapsed_ms":NaN}]}`
	measurementPath := writeAcceptanceRawJSON(t, "non-finite-measurement.json", raw)
	result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken, "-MeasurementsFile", measurementPath)
	if result.ExitCode == 0 || strings.Contains(result.Stdout+result.Stderr+report, "NaN") || fixture.searchCalls != 0 {
		t.Fatalf("runner accepted or echoed non-JSON non-finite measurement input: exit=%d searches=%d report=%s", result.ExitCode, fixture.searchCalls, report)
	}
}

func TestSourceAcceptanceRunnerReportsPartialFullRunAgainstExactPublishedSnapshot(t *testing.T) {
	fixture := &acceptanceFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	measurement := map[string]any{
		"schema_version":   1,
		"incremental_runs": []any{},
		"full_run": map[string]any{
			"status": "measured", "model_identifier": "model-1", "tokenizer": "cl100k_base", "context_limit_tokens": 8192,
			"source_id": "source-1", "snapshot_id": "snapshot-1", "commit_sha": "commit-1",
			"selected_files": 0, "selected_bytes": uint64(1<<63 - 1), "elapsed_ms": 55,
			"phase_duration_ms": map[string]any{"fetching": 7},
		},
	}
	measurementPath := writeAcceptanceJSON(t, "full-run-measurement.json", measurement)
	result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken, "-MeasurementsFile", measurementPath)
	if result.ExitCode != 0 {
		t.Fatalf("valid partial full-run measurement failed: exit=%d report=%s", result.ExitCode, report)
	}
	var document struct {
		T22AcceptanceStatus string `json:"t22_acceptance_status"`
		FullRun             struct {
			Status       string `json:"status"`
			Completeness string `json:"completeness"`
			MetricStatus struct {
				SelectedFiles string            `json:"selected_files"`
				ChunkCount    string            `json:"chunk_count"`
				ActualTokens  string            `json:"actual_input_tokens"`
				Phases        map[string]string `json:"phase_duration_ms"`
			} `json:"metric_status"`
			Output struct {
				SourceID      string            `json:"source_id"`
				SnapshotID    string            `json:"snapshot_id"`
				CommitSHA     string            `json:"commit_sha"`
				SelectedFiles *int64            `json:"selected_files"`
				SelectedBytes *int64            `json:"selected_bytes"`
				ElapsedMS     *int64            `json:"elapsed_ms"`
				Phases        map[string]*int64 `json:"phase_duration_ms"`
			} `json:"output"`
		} `json:"full_run"`
	}
	if err := json.Unmarshal([]byte(report), &document); err != nil {
		t.Fatalf("could not decode full-run report: %v", err)
	}
	fullRun := document.FullRun
	if document.T22AcceptanceStatus != "unknown" || fullRun.Status != "partial" || fullRun.Completeness != "partial" || fullRun.Output.SourceID != "source-1" || fullRun.Output.SnapshotID != "snapshot-1" || fullRun.Output.CommitSHA != "commit-1" {
		t.Fatalf("full-run scope or partial status was not preserved: %+v", document)
	}
	if fullRun.Output.SelectedFiles == nil || *fullRun.Output.SelectedFiles != 0 || fullRun.Output.SelectedBytes == nil || *fullRun.Output.SelectedBytes != int64(1<<63-1) || fullRun.Output.ElapsedMS == nil || *fullRun.Output.ElapsedMS != 55 || fullRun.Output.Phases["fetching"] == nil || *fullRun.Output.Phases["fetching"] != 7 || fullRun.Output.Phases["parsing"] != nil {
		t.Fatalf("full-run numeric metrics were not preserved as nullable JSON integers: %+v", fullRun.Output)
	}
	if fullRun.MetricStatus.SelectedFiles != "observed" || fullRun.MetricStatus.ChunkCount != "unknown" || fullRun.MetricStatus.ActualTokens != "unknown" || fullRun.MetricStatus.Phases["parsing"] != "unknown" {
		t.Fatalf("full-run missing values were not individually marked unknown: %+v", fullRun.MetricStatus)
	}
}

func TestSourceAcceptanceRunnerRejectsFullRunFromDifferentCommit(t *testing.T) {
	fixture := &acceptanceFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	measurement := map[string]any{
		"schema_version":   1,
		"incremental_runs": []any{},
		"full_run": map[string]any{
			"status": "measured", "model_identifier": "model-1", "tokenizer": "cl100k_base", "context_limit_tokens": 8192,
			"source_id": "source-1", "snapshot_id": "snapshot-1", "commit_sha": "other-commit", "selected_files": 1,
		},
	}
	measurementPath := writeAcceptanceJSON(t, "wrong-full-run-measurement.json", measurement)
	result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken, "-MeasurementsFile", measurementPath)
	if result.ExitCode == 0 || !strings.Contains(report, "full_run_scope_mismatch") || fixture.searchCalls != 0 {
		t.Fatalf("runner accepted full-run metrics from another commit: exit=%d searches=%d report=%s", result.ExitCode, fixture.searchCalls, report)
	}
}

func TestSourceAcceptanceRunnerBoundsPublishPolling(t *testing.T) {
	fixture := &acceptanceFixture{syncTimeout: true}
	server := httptest.NewServer(fixture)
	defer server.Close()

	result, report := runSourceAcceptance(t, server.URL, acceptanceTestToken, "-Publish", "-SyncTimeoutSeconds", "1", "-PollIntervalSeconds", "1")
	if result.ExitCode == 0 {
		t.Fatalf("runner did not stop a stuck publication: %s", report)
	}
	if !strings.Contains(report, "sync_timeout") || strings.Contains(result.Stdout+result.Stderr+report, acceptanceTestToken) {
		t.Fatalf("publish timeout was not safely reported: stdout=%s stderr=%s report=%s", result.Stdout, result.Stderr, report)
	}
}

func TestSourceAcceptanceRunnerBindsMissingLegacySnapshotToFreshPublish(t *testing.T) {
	fixture := &acceptanceFixture{publishSuccess: true, publishSnapshotID: "snapshot-new"}
	server := httptest.NewServer(fixture)
	defer server.Close()
	manifest := legacyQuestionManifest("")
	result, report := runSourceAcceptanceWithQuestions(t, server.URL, acceptanceTestToken, manifest, nil, "-Publish", "-PollIntervalSeconds", "1")
	if result.ExitCode != 0 || !strings.Contains(report, `"sync_log_id": "sync-new-log"`) || !strings.Contains(report, `"snapshot_id": "snapshot-new"`) || !strings.Contains(report, `"matched_count": 30`) {
		t.Fatalf("legacy evidence with an omitted snapshot did not bind to the verified fresh publication: exit=%d report=%s", result.ExitCode, report)
	}
}

func TestSourceAcceptanceRunnerDoesNotRewriteExplicitLegacySnapshot(t *testing.T) {
	fixture := &acceptanceFixture{publishSuccess: true, publishSnapshotID: "snapshot-new"}
	server := httptest.NewServer(fixture)
	defer server.Close()
	manifest := legacyQuestionManifest("snapshot-1")
	result, report := runSourceAcceptanceWithQuestions(t, server.URL, acceptanceTestToken, manifest, nil, "-Publish", "-PollIntervalSeconds", "1")
	if result.ExitCode == 0 || !strings.Contains(report, "publish_snapshot_explicit_mismatch") || fixture.searchCalls != 0 {
		t.Fatalf("runner silently replaced an explicit legacy snapshot with the fresh publication: exit=%d searches=%d report=%s", result.ExitCode, fixture.searchCalls, report)
	}
}

func TestSourceAcceptanceRunnerAcceptsGoldQuestionShapeAndNormalizedLFHash(t *testing.T) {
	fixture := &acceptanceFixture{chainEvidence: true}
	fixture.sourceContent = strings.ReplaceAll(testSourceContent, "\n", "\r\n")
	rawDigest := sha256.Sum256([]byte(fixture.sourceContent))
	fixture.sourceSHA = hex.EncodeToString(rawDigest[:])
	server := httptest.NewServer(fixture)
	defer server.Close()

	manifest := goldShapedQuestionManifest(true)
	sourceMap := map[string]any{
		"schema_version":    1,
		"status":            "approved",
		"knowledge_base_id": "kb-1",
		"repositories": []any{
			map[string]any{"repository_id": "evip_mobile", "source_id": "source-1", "snapshot_id": "snapshot-1", "commit_sha": "commit-1", "hash_mode": "sha256_utf8_lf"},
			map[string]any{"repository_id": "nsb", "source_id": "source-2", "snapshot_id": "snapshot-2", "commit_sha": "commit-2", "hash_mode": "sha256_utf8_lf"},
		},
	}
	result, report := runSourceAcceptanceWithQuestions(t, server.URL, acceptanceTestToken, manifest, sourceMap)
	if result.ExitCode != 0 {
		t.Fatalf("gold-shaped multi-evidence run failed: exit=%d stdout=%s stderr=%s report=%s", result.ExitCode, result.Stdout, result.Stderr, report)
	}
	if !strings.Contains(report, `"category": "business_chain"`) || !strings.Contains(report, `"question_id": "BC-01"`) || !strings.Contains(report, `"matched_count": 30`) || !strings.Contains(report, `"hash_mode": "sha256_utf8_lf"`) || !strings.Contains(report, `"source_id": "source-2"`) || !strings.Contains(report, `"snapshot_id": "snapshot-2"`) {
		t.Fatalf("runner did not preserve the gold bank category, normalized-LF result, or distinct second-source snapshot evidence: %s", report)
	}
}

func TestSourceAcceptanceRunnerRequiresEveryCrossRepositoryEvidenceSpan(t *testing.T) {
	fixture := &acceptanceFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	manifest := goldShapedQuestionManifest(true)
	sourceMap := map[string]any{
		"schema_version": 1, "status": "approved", "knowledge_base_id": "kb-1",
		"mappings": map[string]any{
			"evip_mobile": map[string]any{"source_id": "source-1", "snapshot_id": "snapshot-1", "commit_sha": "commit-1"},
			"nsb":         map[string]any{"source_id": "source-2", "snapshot_id": "snapshot-2", "commit_sha": "commit-2"},
		},
	}
	result, report := runSourceAcceptanceWithQuestions(t, server.URL, acceptanceTestToken, manifest, sourceMap)
	if result.ExitCode != 0 {
		t.Fatalf("a 29/30 evidence pass should meet the configured threshold: exit=%d report=%s", result.ExitCode, report)
	}
	var reportDocument struct {
		QuestionResults []struct {
			QuestionID string `json:"question_id"`
			Status     string `json:"status"`
		} `json:"question_results"`
		EvidenceThreshold struct {
			MatchedCount int `json:"matched_count"`
		} `json:"evidence_threshold"`
	}
	if err := json.Unmarshal([]byte(report), &reportDocument); err != nil {
		t.Fatalf("could not decode acceptance report: %v", err)
	}
	chainStatus := ""
	for _, result := range reportDocument.QuestionResults {
		if result.QuestionID == "BC-01" {
			chainStatus = result.Status
			break
		}
	}
	if chainStatus != "unknown" || reportDocument.EvidenceThreshold.MatchedCount != 29 {
		t.Fatalf("the chain was counted as complete after only one required evidence span matched: %s", report)
	}
}

func TestSourceAcceptanceRunnerDoesNotScoreUnapprovedDraftQuestions(t *testing.T) {
	fixture := &acceptanceFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	manifest := goldShapedQuestionManifest(false)
	manifest["status"] = "draft_for_human_confirmation"
	result, report := runSourceAcceptanceWithQuestions(t, server.URL, acceptanceTestToken, manifest, nil)
	if result.ExitCode == 0 || strings.Contains(report, `"status": "completed"`) || !strings.Contains(report, "questions_not_approved") {
		t.Fatalf("unapproved question manifest was scored as completed: exit=%d report=%s", result.ExitCode, report)
	}
	if fixture.searchCalls != 0 {
		t.Fatalf("runner queried against a draft manifest before approval: %d requests", fixture.searchCalls)
	}
}

func TestSourceAcceptanceRunnerFailsClosedWhenParserHTTPHealthIsNotReady(t *testing.T) {
	fixture := &acceptanceFixture{parserNotReady: true}
	server := httptest.NewServer(fixture)
	defer server.Close()
	manifest := legacyQuestionManifest("snapshot-1")
	result, report := runSourceAcceptanceWithQuestions(t, server.URL, acceptanceTestToken, manifest, nil)
	if result.ExitCode == 0 || !strings.Contains(report, "parser_not_ready") || fixture.searchCalls != 0 {
		t.Fatalf("runner searched with an unhealthy parser readiness result: exit=%d searches=%d report=%s", result.ExitCode, fixture.searchCalls, report)
	}
}

func goldShapedQuestionManifest(twoEvidence bool) map[string]any {
	manifest := map[string]any{
		"schema_version": 1,
		"status":         "human_confirmed",
		"hash_mode":      "sha256_utf8_lf",
		"repositories": []any{
			map[string]any{"id": "evip_mobile", "kind": "representative_repo", "commit": "commit-1"},
			map[string]any{"id": "nsb", "kind": "representative_repo", "commit": "commit-2"},
		},
		"questions": make([]any, 0, 30),
	}
	for i := 0; i < 30; i++ {
		category := "symbol_path"
		idPrefix := "SYM"
		if i >= 10 && i < 20 {
			category, idPrefix = "business_chain", "BC"
		} else if i >= 20 {
			category, idPrefix = "frontend_sql", "FSQL"
		}
		id := fmt.Sprintf("%s-%02d", idPrefix, i%10+1)
		repository := "evip_mobile"
		question := map[string]any{
			"id": id, "category": category, "question": fmt.Sprintf("Locate source evidence for accepted behavior %d", i+1),
			"repository": repository, "source_kind": "representative_repo",
			"evidence": []any{map[string]any{"path": "src/Foo.java", "start_line": 2, "end_line": 2, "symbol": "getPushSchedule", "sha256": testSourceSHA, "rationale": "independent line evidence"}},
		}
		if i == 10 && twoEvidence {
			question["evidence"] = []any{
				map[string]any{"path": "src/Foo.java", "start_line": 2, "end_line": 2, "symbol": "mobileCall", "sha256": testSourceSHA, "rationale": "frontend request span"},
				map[string]any{"repository": "nsb", "path": "src/Foo.java", "start_line": 3, "end_line": 3, "symbol": "serviceHandler", "sha256": testSourceSHA, "rationale": "backend handling span"},
			}
		}
		manifest["questions"] = append(manifest["questions"].([]any), question)
	}
	return manifest
}

type commandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

func runSourceAcceptance(t *testing.T, baseURL, token string, extra ...string) (commandResult, string) {
	t.Helper()
	questions := legacyQuestionManifest("snapshot-1")
	return runSourceAcceptanceWithQuestions(t, baseURL, token, questions, nil, extra...)
}

func legacyQuestionManifest(snapshotID string) map[string]any {
	questions := map[string]any{"schema_version": 1, "status": "human_confirmed", "hash_mode": "sha256_utf8_lf", "questions": make([]any, 0, 30)}
	for i := 0; i < 30; i++ {
		category := "symbol_path"
		if i >= 10 && i < 20 {
			category = "business_chain"
		} else if i >= 20 {
			category = "frontend_sql"
		}
		expected := map[string]any{"source_id": "source-1", "commit_sha": "commit-1", "path": "src/Foo.java", "start_line": 2, "end_line": 2, "sha256": testSourceSHA, "hash_mode": "sha256_utf8_lf"}
		if snapshotID != "" {
			expected["snapshot_id"] = snapshotID
		}
		questions["questions"] = append(questions["questions"].([]any), map[string]any{
			"question_id": fmt.Sprintf("q-%02d", i+1), "category": category, "query": fmt.Sprintf("Where is the verified behavior for case %d?", i+1),
			"expected": expected,
		})
	}
	return questions
}

func writeAcceptanceJSON(t *testing.T, filename string, value any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeAcceptanceRawJSON(t *testing.T, filename, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runSourceAcceptanceWithQuestions(t *testing.T, baseURL, token string, questions map[string]any, sourceMap any, extra ...string) (commandResult, string) {
	t.Helper()
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell 7 (pwsh) is required for the independent process-level runner tests")
	}
	_, thisFile, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(thisFile), "source-acceptance-run.ps1")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("acceptance runner is missing: %v", err)
	}
	tmp := t.TempDir()
	questionsPath := filepath.Join(tmp, "questions.json")
	reportPath := filepath.Join(tmp, "report.json")
	encodedQuestions, _ := json.Marshal(questions)
	if err := os.WriteFile(questionsPath, encodedQuestions, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-NoLogo", "-NoProfile", "-File", script,
		"-BaseUrl", baseURL, "-KnowledgeBaseId", "kb-1", "-DataSourceId", "source-1",
		"-QuestionsFile", questionsPath, "-ReportPath", reportPath,
		"-ModelIdentifier", "model-1", "-Tokenizer", "cl100k_base", "-ConfiguredInputTokenLimit", "8192",
		"-ProviderDocumentedHardLimit", "8192", "-ProviderLimitReference", "test-fixture", "-HashMode", "sha256_utf8_lf",
		"-RequestTimeoutSeconds", "5",
	}
	if sourceMap != nil {
		mappingBytes, _ := json.Marshal(sourceMap)
		mappingPath := filepath.Join(tmp, "source-map.json")
		if err := os.WriteFile(mappingPath, mappingBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-RepositorySourceMap", mappingPath)
	}
	args = append(args, extra...)
	cmd := exec.Command(pwsh, args...)
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "WEKNORA_ACCESS_TOKEN=") {
			env = append(env, entry)
		}
	}
	cmd.Env = append(env, "WEKNORA_ACCESS_TOKEN="+token)
	stdout, err := cmd.Output()
	result := commandResult{Stdout: string(stdout)}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			result.Stderr = string(exitErr.Stderr)
		} else {
			result.ExitCode = -1
			result.Stderr = err.Error()
		}
	}
	reportBytes, readErr := os.ReadFile(reportPath)
	if readErr != nil {
		t.Fatalf("runner did not write a report (exit=%d stdout=%s stderr=%s): %v", result.ExitCode, result.Stdout, result.Stderr, readErr)
	}
	var pretty any
	if json.Unmarshal(reportBytes, &pretty) != nil {
		t.Fatalf("runner report is not JSON: %s", reportBytes)
	}
	prettyBytes, _ := json.MarshalIndent(pretty, "", "  ")
	return result, string(prettyBytes)
}
