package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/provider"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/sirupsen/logrus"
)

const (
	shortSourceSentinel = "SHORT_SOURCE_PRIVACY_SENTINEL"
	longSourceSentinel  = "LONG_SOURCE_PRIVACY_SENTINEL"
	debugHelperEnv      = "WEKNORA_EMBEDDING_DEBUG_TEST_CHILD"
)

func TestEmbeddingProviderLogsKeepOnlyInputMetadata(t *testing.T) {
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")

	providers := []struct {
		name string
		new  func(string) (Embedder, error)
	}{
		{
			name: "openai",
			new: func(baseURL string) (Embedder, error) {
				return NewOpenAIEmbedder("test-key", baseURL, "test-embedding-model", 511, 0, "test-model-id", nil)
			},
		},
		{
			name: "zhipu",
			new: func(baseURL string) (Embedder, error) {
				return NewZhipuEmbedder("test-key", baseURL, "test-embedding-model", 511, 0, "test-model-id", nil)
			},
		},
	}
	inputs := []struct {
		name       string
		text       string
		wantMarker string
	}{
		{name: "valid", text: shortSourceSentinel, wantMarker: "input[0]: length="},
		{name: "invalid-length", text: longSourceSentinel + strings.Repeat("x", 9000), wantMarker: "input[0]: INVALID length="},
	}

	for _, providerCase := range providers {
		for _, inputCase := range inputs {
			t.Run(providerCase.name+"/"+inputCase.name, func(t *testing.T) {
				requestBody := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read embedding request: %v", err)
						return
					}
					requestBody <- body
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1,0.2],"index":0}]}`)
				}))
				defer server.Close()

				embedder, err := providerCase.new(server.URL)
				if err != nil {
					t.Fatalf("create %s embedder: %v", providerCase.name, err)
				}
				logs := &bytes.Buffer{}
				ctx := embeddingContextWithLogger(logs)
				if _, err := embedder.BatchEmbed(ctx, []string{inputCase.text}); err != nil {
					t.Fatalf("BatchEmbed: %v", err)
				}

				assertEmbeddingRequestInput(t, <-requestBody, inputCase.text)
				logOutput := logs.String()
				if strings.Contains(logOutput, shortSourceSentinel) || strings.Contains(logOutput, longSourceSentinel) {
					t.Fatalf("embedding logs contain source text: %q", logOutput)
				}
				if !strings.Contains(logOutput, inputCase.wantMarker) {
					t.Fatalf("embedding logs lost input length diagnostics %q: %q", inputCase.wantMarker, logOutput)
				}
				if !strings.Contains(logOutput, fmt.Sprintf("length=%d", len(inputCase.text))) {
					t.Fatalf("embedding logs lost input length metadata: %q", logOutput)
				}
			})
		}
	}
}

func TestEmbeddingDebugOutputOmitsInputText(t *testing.T) {
	if os.Getenv(debugHelperEnv) == "1" {
		runEmbeddingDebugPrivacyChild(t)
		return
	}

	debugDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("find current test executable: %v", err)
	}
	cmd := exec.Command(executable, "-test.run=^TestEmbeddingDebugOutputOmitsInputText$")
	cmd.Env = embeddingDebugChildEnvironment(debugDir)
	childOutput, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run isolated LLM debug test child: %v\n%s", err, childOutput)
	}
	if strings.Contains(string(childOutput), shortSourceSentinel) || strings.Contains(string(childOutput), longSourceSentinel) {
		t.Fatalf("debug test child output contains source text: %q", childOutput)
	}

	entries, err := os.ReadDir(debugDir)
	if err != nil {
		t.Fatalf("read isolated LLM debug output: %v", err)
	}
	var debugOutput strings.Builder
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(debugDir, entry.Name()))
		if err != nil {
			t.Fatalf("read LLM debug record: %v", err)
		}
		debugOutput.Write(content)
	}
	got := debugOutput.String()
	if !strings.Contains(got, "count=2") || !strings.Contains(got, "len=") {
		t.Fatalf("LLM debug output should retain input count and lengths: %q", got)
	}
	if strings.Contains(got, shortSourceSentinel) || strings.Contains(got, longSourceSentinel) {
		t.Fatalf("LLM debug output contains source text: %q", got)
	}
}

func runEmbeddingDebugPrivacyChild(t *testing.T) {
	t.Helper()
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	inputs := []string{shortSourceSentinel, longSourceSentinel + strings.Repeat("x", 9000)}
	requestBody := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read embedding request: %v", err)
			return
		}
		requestBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1],"index":0},{"embedding":[0.2],"index":1}]}`)
	}))
	defer server.Close()

	embedder, err := NewEmbedder(Config{
		Source:    types.ModelSourceRemote,
		Provider:  string(provider.ProviderOpenAI),
		BaseURL:   server.URL,
		ModelName: "test-embedding-model",
		APIKey:    "test-key",
		ModelID:   "test-model-id",
	}, nil, nil)
	if err != nil {
		t.Fatalf("create debug-wrapped embedder: %v", err)
	}
	logs := &bytes.Buffer{}
	ctx := embeddingContextWithLogger(logs)
	if _, err := embedder.BatchEmbed(ctx, inputs); err != nil {
		t.Fatalf("debug-wrapped BatchEmbed: %v", err)
	}
	assertEmbeddingRequestInputs(t, <-requestBody, inputs)
	if strings.Contains(logs.String(), shortSourceSentinel) || strings.Contains(logs.String(), longSourceSentinel) {
		t.Fatalf("provider logs contain source text while LLM debug is enabled: %q", logs.String())
	}
}

func embeddingContextWithLogger(output io.Writer) context.Context {
	entryLogger := logrus.New()
	entryLogger.SetOutput(output)
	entryLogger.SetLevel(logrus.DebugLevel)
	return context.WithValue(context.Background(), types.LoggerContextKey, logrus.NewEntry(entryLogger))
}

func assertEmbeddingRequestInput(t *testing.T, body []byte, want string) {
	t.Helper()
	assertEmbeddingRequestInputs(t, body, []string{want})
}

func assertEmbeddingRequestInputs(t *testing.T, body []byte, want []string) {
	t.Helper()
	var request struct {
		Input []string `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode captured embedding request: %v", err)
	}
	if len(request.Input) != len(want) {
		t.Fatalf("captured request has %d inputs, want %d", len(request.Input), len(want))
	}
	for i := range want {
		if request.Input[i] != want[i] {
			t.Fatalf("captured input[%d] changed in transit", i)
		}
	}
}

func embeddingDebugChildEnvironment(debugDir string) []string {
	allowed := map[string]struct{}{
		"path": {}, "systemroot": {}, "windir": {}, "temp": {}, "tmp": {},
	}
	env := make([]string, 0, 8)
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, keep := allowed[strings.ToLower(key)]; keep {
			env = append(env, item)
		}
	}
	return append(env,
		"LLM_DEBUG_LOG="+debugDir,
		"SSRF_WHITELIST=127.0.0.1",
		debugHelperEnv+"=1",
	)
}
