package source

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ProcessingVersion controls coordinates, structure, chunk budgeting and index context.
// Increment this whenever any of those contracts changes, including tokenization.
const ProcessingVersion = "source-structure-context-cl100k-2000-v1"

func ArtifactKey(parts ...string) string {
	data, _ := json.Marshal(parts)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// EmbeddingVersion is a digest only; model credentials never reach run output.
func EmbeddingVersion(model *types.Model, dimension int) string {
	data, _ := json.Marshal(model.Parameters)
	return ArtifactKey(model.ID, model.Name, string(model.Source), string(data), model.UpdatedAt.UTC().Format(time.RFC3339Nano), fmt.Sprint(dimension))
}

func ParserVersion(ctx context.Context, endpoint string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/health", nil)
	if err != nil {
		return "", fmt.Errorf("source parser URL is invalid")
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("source parser unavailable")
	}
	defer response.Body.Close()
	var health struct {
		Ready         bool   `json:"ready"`
		ParserVersion string `json:"parser_version"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(http.MaxBytesReader(nil, response.Body, 16384)).Decode(&health) != nil || !health.Ready || health.ParserVersion == "" {
		return "", fmt.Errorf("source parser has no controlled version")
	}
	return health.ParserVersion, nil
}
