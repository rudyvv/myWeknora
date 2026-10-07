// Root-only bounded API adapter for the approved full nsb source publication.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const (
	sourceID       = "ba6d2417-222e-4ff9-be22-eccf28659b32"
	kbID           = "65658207-a2ec-47fb-bf0f-11e7b685369e"
	approvedCommit = "c5e128035cd1d0178184a24f164673988c3f3036"
	metadataSHA    = "508ccecc713c612d67f362bb6fca7e31932c6444bae25caf56632a952825d676"
	privateDir     = `C:\Users\28211\.codex\test-runners\t22-isolated-runtime-logs`
	apiBase        = "http://127.0.0.1:57825/api/v1"
	maxResponse    = 16 << 20
	windowEnd      = "2026-10-07T10:45:00Z"
)

var uuidPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type approvedFile struct {
	Path string `json:"path"`
	Blob string `json:"blob_sha"`
	Size int64  `json:"size_bytes"`
}
type approvedScope struct {
	Source     string         `json:"data_source_id"`
	Repository string         `json:"repository"`
	Project    string         `json:"project_id"`
	Branch     string         `json:"branch"`
	Commit     string         `json:"commit_sha"`
	Count      int            `json:"selected_file_count"`
	Bytes      int64          `json:"selected_bytes"`
	Files      []approvedFile `json:"files"`
}
type previewFile struct {
	Path   string `json:"path"`
	Blob   string `json:"blob_sha"`
	Size   int64  `json:"size"`
	Status string `json:"status"`
}
type preview struct {
	Project string        `json:"project_id"`
	Branch  string        `json:"branch"`
	Commit  string        `json:"commit_sha"`
	CanSync bool          `json:"can_sync"`
	Files   []previewFile `json:"files"`
}
type snapshot struct {
	ID          string `json:"id"`
	Tenant      uint64 `json:"tenant_id"`
	KB          string `json:"knowledge_base_id"`
	Source      string `json:"data_source_id"`
	SyncLog     string `json:"sync_log_id"`
	Commit      string `json:"commit_sha"`
	State       string `json:"state"`
	Complete    bool   `json:"manifest_complete"`
	FileCount   int    `json:"file_count"`
	MemberCount int    `json:"member_count"`
	ChunkCount  int    `json:"chunk_count"`
}
type telemetry struct {
	Phase map[string]int64 `json:"phase_duration_ms"`
	Bytes *int64           `json:"selected_bytes"`
	Usage *struct {
		Calls  *int64 `json:"embedding_calls"`
		Tokens *int64 `json:"estimated_input_tokens"`
	} `json:"model_usage"`
}
type syncLog struct {
	ID     string `json:"id"`
	Tenant uint64 `json:"tenant_id"`
	Source string `json:"data_source_id"`
	Status string `json:"status"`
	Result struct {
		Source *struct {
			Snapshot  *snapshot  `json:"snapshot"`
			Telemetry *telemetry `json:"telemetry"`
		} `json:"source"`
	} `json:"result"`
}
type report struct {
	Status          string           `json:"status"`
	SelectedFiles   int              `json:"selected_files"`
	SelectedBytes   int64            `json:"selected_bytes"`
	LogID           string           `json:"sync_log_id,omitempty"`
	SnapshotID      string           `json:"snapshot_id,omitempty"`
	Commit          string           `json:"commit_sha,omitempty"`
	Chunks          int              `json:"chunks,omitempty"`
	ElapsedMS       int64            `json:"elapsed_ms"`
	PhaseMS         map[string]int64 `json:"phase_duration_ms,omitempty"`
	EmbeddingCalls  *int64           `json:"embedding_calls,omitempty"`
	EstimatedTokens *int64           `json:"estimated_input_tokens,omitempty"`
}

func main() { os.Exit(run()) }
func run() int {
	if len(os.Args) == 1 {
		fmt.Println(`{"status":"stat_only_no_access"}`)
		return 0
	}
	args := os.Args[1:]
	previewOnly := len(args) == 1 && args[0] == "--preview"
	publishMode := len(args) == 1 && args[0] == "--publish"
	watchID := ""
	if len(args) == 2 && args[0] == "--watch" && uuidPattern.MatchString(args[1]) {
		watchID = args[1]
	}
	if !previewOnly && !publishMode && watchID == "" {
		fmt.Println(`{"status":"arguments_rejected"}`)
		return 2
	}
	if os.Getenv("T22_RUNTIME_ACL_GATE") != "verified" {
		fmt.Println(`{"status":"root_acl_attestation_required"}`)
		return 2
	}
	expected, token, err := loadFixedInputs()
	if err != nil {
		fmt.Println(`{"status":"fixed_inputs_rejected"}`)
		return 2
	}
	end, _ := time.Parse(time.RFC3339, windowEnd)
	if !time.Now().Before(end) {
		fmt.Println(`{"status":"runtime_window_expired"}`)
		return 2
	}
	ctx, cancel := context.WithDeadline(context.Background(), end.Add(-time.Minute))
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect_rejected") }}
	var output *os.File
	var progress func(report) error
	if publishMode {
		// The exclusive journal makes any uncertain mutating request non-repeatable.
		output, err = os.OpenFile(filepath.Join(privateDir, "nsb-full-publication-20261007.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			fmt.Println(`{"status":"existing_journal_refuse_rerun"}`)
			return 2
		}
		defer output.Close()
		progress = func(r report) error {
			if err := json.NewEncoder(output).Encode(r); err != nil {
				return err
			}
			return output.Sync()
		}
	}
	result, err := publish(ctx, client, apiBase, token, expected, previewOnly, watchID, progress)
	if err != nil {
		result.Status = "failed_or_unknown_no_blind_retry"
	}
	if progress != nil {
		if e := progress(result); e != nil {
			fmt.Println(`{"status":"journal_outcome_unknown"}`)
			return 3
		}
	}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		return 3
	}
	if err != nil {
		return 2
	}
	return 0
}

func loadFixedInputs() (approvedScope, string, error) {
	fail := errors.New("fixed_input_invalid")
	raw, err := readFile(filepath.Join(privateDir, "full-scope-metadata-20261006.json"), 2<<20)
	if err != nil {
		return approvedScope{}, "", fail
	}
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != metadataSHA {
		return approvedScope{}, "", fail
	}
	var metadata struct {
		KB       string          `json:"knowledge_base_id"`
		Complete bool            `json:"complete"`
		Sources  []approvedScope `json:"sources"`
	}
	if json.Unmarshal(raw, &metadata) != nil || metadata.KB != kbID || !metadata.Complete || len(metadata.Sources) != 3 {
		return approvedScope{}, "", fail
	}
	var expected approvedScope
	for _, s := range metadata.Sources {
		if s.Source == sourceID {
			expected = s
		}
	}
	if expected.Repository != "nsb" || expected.Project != "402" || expected.Branch != "master" || expected.Commit != approvedCommit || expected.Count != 5448 || expected.Bytes != 88273936 || len(expected.Files) != 5448 {
		return approvedScope{}, "", fail
	}
	raw, err = readFile(filepath.Join(privateDir, "t22-rehearsal-api-key.json"), 16<<10)
	if err != nil {
		return approvedScope{}, "", fail
	}
	var fixture struct {
		Token  string `json:"token"`
		ID     uint64 `json:"id"`
		Tenant uint64 `json:"tenant_id"`
		KB     string `json:"kb_id"`
		Expiry string `json:"expires_at"`
	}
	if json.Unmarshal(raw, &fixture) != nil || fixture.ID != 1 || fixture.Tenant != 10000 || fixture.KB != kbID || fixture.Token == "" || fixture.Expiry != windowEnd {
		return approvedScope{}, "", fail
	}
	return expected, fixture.Token, nil
}
func readFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("file_rejected")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(raw)) > limit {
		return nil, errors.New("file_limit")
	}
	return raw, err
}
func request(ctx context.Context, client *http.Client, base, token, method, path string, body []byte, dst any) error {
	timeout := 30 * time.Second
	if path == "/datasource/"+sourceID+"/source-preview" {
		timeout = 2 * time.Minute
	}
	call, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(call, method, base+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("request_invalid")
	}
	req.Header.Set("X-API-Key", token)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return errors.New("request_outcome_unknown")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("api_rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse {
		return errors.New("response_rejected")
	}
	if dst != nil && json.Unmarshal(raw, dst) != nil {
		return errors.New("response_shape_rejected")
	}
	return nil
}
func publish(ctx context.Context, client *http.Client, base, token string, expected approvedScope, previewOnly bool, watchID string, progress func(report) error) (report, error) {
	started := time.Now()
	result := report{Status: "running", SelectedFiles: expected.Count, SelectedBytes: expected.Bytes}
	fail := errors.New("publication_not_verified")
	if watchID == "" {
		var envelope struct {
			Data preview `json:"data"`
		}
		if err := request(ctx, client, base, token, "POST", "/datasource/"+sourceID+"/source-preview", []byte(`{}`), &envelope); err != nil {
			return result, err
		}
		p := envelope.Data
		if p.Project != "402" || p.Branch != "master" || p.Commit != expected.Commit || !p.CanSync {
			return result, fail
		}
		approved := map[string]approvedFile{}
		for _, file := range expected.Files {
			if _, exists := approved[file.Path]; exists {
				return result, fail
			}
			approved[file.Path] = file
		}
		selected := 0
		var size int64
		for _, file := range p.Files {
			switch file.Status {
			case "included":
				want, ok := approved[file.Path]
				if !ok || want.Blob != file.Blob || want.Size != file.Size || file.Size < 0 {
					return result, fail
				}
				delete(approved, file.Path)
				selected++
				size += file.Size
			case "excluded":
			default:
				return result, fail
			}
		}
		if len(approved) != 0 || selected != expected.Count || size != expected.Bytes {
			return result, fail
		}
		result.Status = "preview_complete"
		if previewOnly {
			return result, nil
		}
		if progress == nil {
			return result, errors.New("exclusive_journal_required")
		}
		if err := progress(result); err != nil {
			return result, err
		}
		if err := request(ctx, client, base, token, "POST", "/datasource/"+sourceID+"/resume", nil, nil); err != nil {
			return result, err
		}
		result.Status = "source_resumed"
		if err := progress(result); err != nil {
			return result, err
		}
		var log syncLog
		if err := request(ctx, client, base, token, "POST", "/datasource/"+sourceID+"/sync", nil, &log); err != nil {
			return result, err
		}
		if !uuidPattern.MatchString(log.ID) || log.Source != sourceID || log.Tenant != 10000 {
			return result, fail
		}
		watchID = log.ID
		result.LogID = watchID
		result.Status = "sync_started"
		if err := progress(result); err != nil {
			return result, err
		}
	}
	result.LogID = watchID
	for polls := 0; polls < 3000; polls++ {
		var log syncLog
		if err := request(ctx, client, base, token, "GET", "/datasource/logs/"+watchID, nil, &log); err != nil {
			return result, err
		}
		if log.ID != watchID || log.Source != sourceID || log.Tenant != 10000 {
			return result, fail
		}
		switch log.Status {
		case "queued", "pending", "running":
		case "success":
			if log.Result.Source == nil || log.Result.Source.Snapshot == nil {
				return result, fail
			}
			s := log.Result.Source.Snapshot
			if s.Tenant != 10000 || s.KB != kbID || s.Source != sourceID || s.SyncLog != watchID || s.Commit != expected.Commit || s.State != "published" || !s.Complete || s.FileCount != expected.Count || s.MemberCount != expected.Count || !uuidPattern.MatchString(s.ID) || s.ChunkCount < 1 {
				return result, fail
			}
			result.Status = "published_verified"
			result.SnapshotID = s.ID
			result.Commit = s.Commit
			result.Chunks = s.ChunkCount
			result.ElapsedMS = time.Since(started).Milliseconds()
			result.PhaseMS = map[string]int64{}
			if t := log.Result.Source.Telemetry; t != nil {
				if t.Bytes == nil || *t.Bytes != expected.Bytes {
					return result, fail
				}
				for _, name := range []string{"fetching", "parsing", "indexing", "publishing"} {
					if n, ok := t.Phase[name]; ok && n >= 0 {
						result.PhaseMS[name] = n
					}
				}
				if t.Usage != nil {
					if t.Usage.Calls != nil && *t.Usage.Calls >= 0 {
						result.EmbeddingCalls = t.Usage.Calls
					}
					if t.Usage.Tokens != nil && *t.Usage.Tokens >= 0 {
						result.EstimatedTokens = t.Usage.Tokens
					}
				}
			}
			return result, nil
		default:
			return result, fail
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
	return result, fail
}
