// Package gitlabwebhook implements the public GitLab Push Hook HTTP boundary.
package gitlabwebhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

const maxPushPayloadBytes = 2 << 20

type Handler struct {
	service interfaces.GitLabWebhookService
}

func New(service interfaces.GitLabWebhookService) *Handler {
	return &Handler{service: service}
}

type pushPayload struct {
	ObjectKind string          `json:"object_kind"`
	ProjectID  json.RawMessage `json:"project_id"`
	Project    struct {
		ID                json.RawMessage `json:"id"`
		PathWithNamespace string          `json:"path_with_namespace"`
	} `json:"project"`
	Ref    string `json:"ref"`
	Before string `json:"before"`
	After  string `json:"after"`
}

func parseProjectID(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) == nil {
		return number.String()
	}
	return ""
}

// ReceivePush accepts only Push Hook requests using the configured shared
// token. Payload project/ref values are matched to persisted source config and
// never choose a tenant, repository, or commit target.
func (h *Handler) ReceivePush(c *gin.Context) {
	if c.GetHeader("X-Gitlab-Event") != "Push Hook" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only GitLab Push Hook events are accepted"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPushPayloadBytes)
	decoder := json.NewDecoder(c.Request.Body)
	var payload pushPayload
	if err := decoder.Decode(&payload); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "GitLab webhook payload is too large"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid GitLab Push Hook payload"})
		}
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid GitLab Push Hook payload"})
		return
	}
	if payload.ObjectKind != "push" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only GitLab Push Hook events are accepted"})
		return
	}
	projectID := parseProjectID(payload.Project.ID)
	if projectID == "" {
		projectID = parseProjectID(payload.ProjectID)
	}
	deliveryID := firstDeliveryHeader(c)
	event := types.GitLabPushEvent{
		ProjectID: projectID, ProjectPath: strings.TrimSpace(payload.Project.PathWithNamespace), Ref: payload.Ref, Before: payload.Before, After: payload.After,
		EventID: deliveryID, DeliveryID: deliveryID,
	}
	if h.service == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GitLab webhook service unavailable"})
		return
	}
	accepted, duplicate, err := h.service.ReceiveGitLabPush(c.Request.Context(), c.GetHeader("X-Gitlab-Token"), event)
	if err != nil {
		if errors.Is(err, datasource.ErrGitLabWebhookUnauthorized) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "GitLab webhook is not authorized for a registered source"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GitLab webhook trigger could not be persisted"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"accepted": accepted, "duplicate": duplicate})
}

func firstDeliveryHeader(c *gin.Context) string {
	for _, name := range []string{"webhook-id", "Idempotency-Key", "X-Gitlab-Event-UUID"} {
		if value := strings.TrimSpace(c.GetHeader(name)); value != "" {
			return value
		}
	}
	return ""
}
