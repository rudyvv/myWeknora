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
	wrongSnapshot  bool
	denySourceRead bool
	syncTimeout    bool
	parserNotReady bool
	chainEvidence  bool
	sourceContent  string
	sourceSHA      string
	logOffsets     []int
	searchCalls    int
	agentCalls     int
}

func (f *acceptanceFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "Bearer "+acceptanceTestToken {
		http.Error(w, `{"error":"invalid test credentials"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/datasource/source-1":
		writeFixtureJSON(w, map[string]any{"id": "source-1", "type": "gitlab", "knowledge_base_id": "kb-1", "config": map[string]any{"settings": map[string]any{"content_mode": "source", "projects": []any{map[string]any{"project_id": "42", "paths": []any{"src"}}}}}, "source_lifecycle": map[string]any{"binding_state": "bound", "query_enabled": true}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge-bases/kb-1":
		writeFixtureJSON(w, map[string]any{"success": true, "data": map[string]any{"id": "kb-1", "embedding_model_id": "model-1"}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/models/model-1":
		writeFixtureJSON(w, map[string]any{"success": true, "data": map[string]any{"id": "model-1", "type": "Embedding", "parameters": map[string]any{"embedding_parameters": map[string]any{"tokenizer": "cl100k_base", "max_input_tokens": 8192}}}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/datasource/source-1/sync":
		writeFixtureJSON(w, map[string]any{"id": "log-1", "data_source_id": "source-1", "status": "running"})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/datasource/logs/log-1" && f.syncTimeout:
		writeFixtureJSON(w, map[string]any{"id": "log-1", "data_source_id": "source-1", "status": "running"})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/datasource/source-1/source-preview":
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
		writeFixtureJSON(w, map[string]any{"data": map[string]any{
			"project_id": "42", "branch": "main", "commit_sha": "commit-1", "rules_version": "rules-v1", "can_sync": canSync,
			"files":  []any{map[string]any{"path": "src/Foo.java", "blob_sha": "blob-1", "size": len(testSourceContent), "status": "included"}},
			"checks": []any{map[string]any{"name": "parser", "ready": parserReady, "message": "ready"}, map[string]any{"name": "indexes", "ready": true, "message": "ready"}, map[string]any{"name": "source_pipeline", "ready": canSync, "message": "ready"}},
		}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/datasource/source-1/logs":
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
			writeFixtureJSON(w, []any{publishedTestLog()})
		}
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-1/hybrid-search":
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, `{"error":"invalid test body"}`, http.StatusBadRequest)
			return
		}
		ids, _ := request["source_ids"].([]any)
		if len(ids) != 1 || ids[0] != "source-1" || request["match_count"] != float64(10) {
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
			if f.chainEvidence && i == 1 {
				path, version, knowledgeID, startLine, endLine = "src/Foo.java", "version-1", "file-1", 3, 3
			}
			snapshotID := "snapshot-1"
			if f.wrongSnapshot && i == 0 {
				snapshotID = "snapshot-old"
			}
			hits[i] = map[string]any{
				"knowledge_id":   knowledgeID,
				"metadata":       map[string]string{"datasource_id": "source-1", "source_snapshot_id": snapshotID, "commit_sha": "commit-1", "source_path": path, "source_file_version_id": version},
				"chunk_metadata": map[string]any{"source": map[string]any{"range": map[string]any{"start_line": startLine, "end_line": endLine}}},
			}
		}
		writeFixtureJSON(w, map[string]any{"success": true, "data": hits})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge/file-1/source":
		if f.denySourceRead {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"sensitive-error-marker"}`))
			return
		}
		if r.URL.Query().Get("version_id") != "version-1" {
			http.Error(w, `{"error":"version was not fixed"}`, http.StatusBadRequest)
			return
		}
		content, digest := testSourceContent, testSourceSHA
		if f.sourceContent != "" {
			content = f.sourceContent
		}
		if f.sourceSHA != "" {
			digest = f.sourceSHA
		}
		writeFixtureJSON(w, map[string]any{"success": true, "data": map[string]any{
			"knowledge_id": "file-1", "data_source_id": "source-1", "snapshot_id": "snapshot-1", "file_version_id": "version-1", "commit_sha": "commit-1", "path": "src/Foo.java", "sha256": digest, "parser_version": "source-pack-test", "content": content,
		}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent-chat/session-1":
		f.agentCalls++
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil || request["agent_id"] != "agent-1" || request["agent_enabled"] != true {
			http.Error(w, `{"error":"invalid agent request"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"answer\"}\n\ndata: {\"type\":\"done\"}\n\n"))
	default:
		http.NotFound(w, r)
	}
}

const testSourceContent = "class Foo {\n void getPushSchedule() {}\n}\n"

var testSourceSHA = func() string {
	digest := sha256.Sum256([]byte(testSourceContent))
	return hex.EncodeToString(digest[:])
}()

func publishedTestLog() map[string]any {
	return map[string]any{
		"id": "log-1", "status": "success", "data_source_id": "source-1",
		"result": map[string]any{"source": map[string]any{"snapshot": map[string]any{
			"id": "snapshot-1", "data_source_id": "source-1", "knowledge_base_id": "kb-1", "commit_sha": "commit-1", "state": "published", "previous_snapshot_id": "snapshot-0", "file_count": 1,
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
	if strings.Contains(report, testSourceContent) || strings.Contains(result.Stdout+result.Stderr+report, acceptanceTestToken) {
		t.Fatal("runner exposed source text or the process credential")
	}
	if strings.Contains(report, "telemetry-secret-marker") || strings.Contains(report, "unknown_phase") || !strings.Contains(report, `"fetching": 12`) {
		t.Fatal("runner did not constrain telemetry to the typed, allowlisted metrics")
	}
	if fixture.searchCalls != 30 || fixture.agentCalls != 1 {
		t.Fatalf("expected 30 scoped searches and one UI agent request, got searches=%d agent=%d", fixture.searchCalls, fixture.agentCalls)
	}
	if len(fixture.logOffsets) != 2 || fixture.logOffsets[0] != 0 || fixture.logOffsets[1] != 100 {
		t.Fatalf("published-run lookup did not paginate logs: offsets=%v", fixture.logOffsets)
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
			map[string]any{"repository_id": "nsb", "source_id": "source-1", "snapshot_id": "snapshot-1", "commit_sha": "commit-1", "hash_mode": "sha256_utf8_lf"},
		},
	}
	result, report := runSourceAcceptanceWithQuestions(t, server.URL, acceptanceTestToken, manifest, sourceMap)
	if result.ExitCode != 0 {
		t.Fatalf("gold-shaped multi-evidence run failed: exit=%d stdout=%s stderr=%s report=%s", result.ExitCode, result.Stdout, result.Stderr, report)
	}
	if !strings.Contains(report, `"category": "business_chain"`) || !strings.Contains(report, `"question_id": "BC-01"`) || !strings.Contains(report, `"matched_count": 30`) || !strings.Contains(report, `"hash_mode": "sha256_utf8_lf"`) {
		t.Fatalf("runner did not preserve the gold bank category, evidence mode, or normalized-LF result: %s", report)
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
			"nsb":         map[string]any{"source_id": "source-1", "snapshot_id": "snapshot-1", "commit_sha": "commit-1"},
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
	manifest := goldShapedQuestionManifest(false)
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
			map[string]any{"id": "nsb", "kind": "representative_repo", "commit": "commit-1"},
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
	questions := map[string]any{"schema_version": 1, "status": "human_confirmed", "hash_mode": "sha256_utf8_lf", "questions": make([]any, 0, 30)}
	for i := 0; i < 30; i++ {
		category := "symbol_path"
		if i >= 10 && i < 20 {
			category = "business_chain"
		} else if i >= 20 {
			category = "frontend_sql"
		}
		questions["questions"] = append(questions["questions"].([]any), map[string]any{
			"question_id": fmt.Sprintf("q-%02d", i+1), "category": category, "query": fmt.Sprintf("Where is the verified behavior for case %d?", i+1),
			"expected": map[string]any{"source_id": "source-1", "snapshot_id": "snapshot-1", "commit_sha": "commit-1", "path": "src/Foo.java", "start_line": 2, "end_line": 2, "sha256": testSourceSHA, "hash_mode": "sha256_utf8_lf"},
		})
	}
	return runSourceAcceptanceWithQuestions(t, baseURL, token, questions, nil, extra...)
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
