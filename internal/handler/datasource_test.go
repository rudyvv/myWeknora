package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

type stubDataSourceService struct {
	interfaces.DataSourceService
	getSyncLogs   func(ctx context.Context, dsID string, limit int, offset int) ([]*types.SyncLog, error)
	getDataSource func(ctx context.Context, id string) (*types.DataSource, error)
	unbind        func(ctx context.Context, id string) (*types.DataSource, error)
	clear         func(ctx context.Context, id string, confirm bool, scope string) (*types.DataSource, error)
	retryClear    func(ctx context.Context, id string, operationID string) (*types.DataSource, error)
}

func (s *stubDataSourceService) GetSyncLogs(ctx context.Context, dsID string, limit int, offset int) ([]*types.SyncLog, error) {
	if s.getSyncLogs != nil {
		return s.getSyncLogs(ctx, dsID, limit, offset)
	}
	return nil, nil
}

func (s *stubDataSourceService) GetDataSource(ctx context.Context, id string) (*types.DataSource, error) {
	if s.getDataSource != nil {
		return s.getDataSource(ctx, id)
	}
	return nil, nil
}

func (s *stubDataSourceService) UnbindDataSource(ctx context.Context, id string) (*types.DataSource, error) {
	if s.unbind != nil {
		return s.unbind(ctx, id)
	}
	return nil, nil
}

func (s *stubDataSourceService) ClearSource(ctx context.Context, id string, confirm bool, scope string) (*types.DataSource, error) {
	if s.clear != nil {
		return s.clear(ctx, id, confirm, scope)
	}
	return nil, nil
}

func (s *stubDataSourceService) RetryClearSource(ctx context.Context, id string, operationID string) (*types.DataSource, error) {
	if s.retryClear != nil {
		return s.retryClear(ctx, id, operationID)
	}
	return nil, nil
}

type stubKBServiceForDS struct {
	interfaces.KnowledgeBaseService
	getByID func(ctx context.Context, id string) (*types.KnowledgeBase, error)
}

func (s *stubKBServiceForDS) GetKnowledgeBaseByID(ctx context.Context, id string) (*types.KnowledgeBase, error) {
	if s.getByID != nil {
		return s.getByID(ctx, id)
	}
	return nil, nil
}

func newDataSourceTestRouter(h *DataSourceHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(errorCapture())
	r.Use(func(c *gin.Context) {
		if tenantID, ok := c.Request.Context().Value(types.TenantIDContextKey).(uint64); ok {
			c.Set(types.TenantIDContextKey.String(), tenantID)
		}
		c.Next()
	})
	r.GET("/datasource/:id/logs", h.GetSyncLogs)
	r.POST("/datasource/:id/unbind", h.UnbindDataSource)
	r.POST("/datasource/:id/clear-source", h.ClearSource)
	r.POST("/datasource/:id/clear-source/retry", h.RetryClearSource)
	return r
}

func withDSCtx(req *http.Request, tenantID uint64) *http.Request {
	ctx := req.Context()
	ctx = context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	return req.WithContext(ctx)
}

func TestDataSource_SourceLifecycleHTTPContracts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config, err := (func() (*types.DataSourceConfig, error) {
		cfg := &types.DataSourceConfig{Type: types.ConnectorTypeGitLab, Settings: map[string]interface{}{"content_mode": "source"}}
		blob, marshalErr := cfg.ToJSON()
		if marshalErr != nil {
			return nil, marshalErr
		}
		parsed := &types.DataSourceConfig{}
		if unmarshalErr := json.Unmarshal(blob, parsed); unmarshalErr != nil {
			return nil, unmarshalErr
		}
		return parsed, nil
	})()
	if err != nil {
		t.Fatal(err)
	}
	configBlob, err := config.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	dataSource := func(binding string, queryEnabled bool, cleanup *types.SourceCleanupOperation) *types.DataSource {
		return &types.DataSource{ID: "ds1", KnowledgeBaseID: "kb1", Config: configBlob,
			SourceBindingState: binding, SourceQueryEnabled: queryEnabled, SourceCleanup: cleanup}
	}
	service := &stubDataSourceService{
		getDataSource: func(_ context.Context, _ string) (*types.DataSource, error) {
			return dataSource(types.SourceBindingBound, true, nil), nil
		},
		unbind: func(_ context.Context, id string) (*types.DataSource, error) {
			if id != "ds1" {
				t.Fatalf("unexpected source id %q", id)
			}
			return dataSource(types.SourceBindingUnbound, true, nil), nil
		},
		clear: func(_ context.Context, id string, confirm bool, scope string) (*types.DataSource, error) {
			if id != "ds1" || !confirm || scope != types.SourceCleanupScopeCurrentAndHistory {
				t.Fatalf("unexpected clear request: %q %t %q", id, confirm, scope)
			}
			return dataSource(types.SourceBindingUnbound, false, &types.SourceCleanupOperation{
				ID: "op1", Status: types.SourceCleanupPending, Retryable: false,
			}), nil
		},
		retryClear: func(_ context.Context, id string, operationID string) (*types.DataSource, error) {
			if id != "ds1" || operationID != "op1" {
				t.Fatalf("unexpected retry request: %q %q", id, operationID)
			}
			return dataSource(types.SourceBindingUnbound, false, &types.SourceCleanupOperation{
				ID: "op1", Status: types.SourceCleanupPending, Retryable: false,
			}), nil
		},
	}
	kbService := &stubKBServiceForDS{getByID: func(_ context.Context, _ string) (*types.KnowledgeBase, error) {
		return &types.KnowledgeBase{ID: "kb1", TenantID: 1}, nil
	}}
	router := newDataSourceTestRouter(NewDataSourceHandler(service, kbService))
	for _, test := range []struct {
		name, path, body string
		status           int
		wantLifecycle    string
	}{
		{name: "unbind", path: "/datasource/ds1/unbind", body: `{}`, status: http.StatusOK,
			wantLifecycle: `"source_lifecycle":{"binding_state":"unbound","query_enabled":true}`},
		{name: "clear", path: "/datasource/ds1/clear-source", body: `{"confirm":true,"scope":"current_and_history"}`, status: http.StatusAccepted,
			wantLifecycle: `"source_lifecycle":{"binding_state":"unbound","query_enabled":false,"cleanup":{"id":"op1","status":"pending","retryable":false}}`},
		{name: "retry", path: "/datasource/ds1/clear-source/retry", body: `{"operation_id":"op1"}`, status: http.StatusAccepted,
			wantLifecycle: `"source_lifecycle":{"binding_state":"unbound","query_enabled":false,"cleanup":{"id":"op1","status":"pending","retryable":false}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := withDSCtx(httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body)), 1)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("expected %d, got %d: %s", test.status, response.Code, response.Body.String())
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if _, ok := envelope["data"]; !ok || envelope["status"] != nil {
				t.Fatalf("expected the fixed data envelope, got %s", response.Body.String())
			}
			if !strings.Contains(response.Body.String(), test.wantLifecycle) {
				t.Fatalf("response missing lifecycle state %s: %s", test.wantLifecycle, response.Body.String())
			}
		})
	}
}

func TestDataSource_GetSyncLogs_ValidLimitWithinBounds(t *testing.T) {
	var capturedLimit, capturedOffset int
	dsSvc := &stubDataSourceService{
		getDataSource: func(_ context.Context, id string) (*types.DataSource, error) {
			return &types.DataSource{ID: id, KnowledgeBaseID: "kb1"}, nil
		},
		getSyncLogs: func(_ context.Context, _ string, limit int, offset int) ([]*types.SyncLog, error) {
			capturedLimit = limit
			capturedOffset = offset
			return []*types.SyncLog{
				{ID: "log1", DataSourceID: "ds1"},
			}, nil
		},
	}
	kbSvc := &stubKBServiceForDS{
		getByID: func(_ context.Context, _ string) (*types.KnowledgeBase, error) {
			return &types.KnowledgeBase{ID: "kb1", TenantID: 1}, nil
		},
	}
	h := NewDataSourceHandler(dsSvc, kbSvc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/datasource/ds1/logs?limit=50&offset=25", nil)
	req = withDSCtx(req, 1)
	newDataSourceTestRouter(h).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if capturedLimit != 50 {
		t.Fatalf("expected limit=50, got %d", capturedLimit)
	}
	if capturedOffset != 25 {
		t.Fatalf("expected offset=25, got %d", capturedOffset)
	}
}

func TestDataSource_GetSyncLogs_LimitExceedingMaximum(t *testing.T) {
	dsSvc := &stubDataSourceService{
		getDataSource: func(_ context.Context, id string) (*types.DataSource, error) {
			return &types.DataSource{ID: id, KnowledgeBaseID: "kb1"}, nil
		},
		getSyncLogs: func(_ context.Context, _ string, _ int, _ int) ([]*types.SyncLog, error) {
			t.Fatalf("service must not be called when limit exceeds maximum")
			return nil, nil
		},
	}
	kbSvc := &stubKBServiceForDS{
		getByID: func(_ context.Context, _ string) (*types.KnowledgeBase, error) {
			return &types.KnowledgeBase{ID: "kb1", TenantID: 1}, nil
		},
	}
	h := NewDataSourceHandler(dsSvc, kbSvc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/datasource/ds1/logs?limit=999", nil)
	req = withDSCtx(req, 1)
	newDataSourceTestRouter(h).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for limit > 100, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errMsg, ok := resp["error"].(string)
	if !ok || errMsg == "" {
		t.Fatalf("expected error message in response")
	}
	if errMsg != "limit must be between 1 and 100" {
		t.Fatalf("expected specific error message, got %q", errMsg)
	}
}

func TestDataSource_GetSyncLogs_MissingLimitDefaultsCorrectly(t *testing.T) {
	var capturedLimit, capturedOffset int
	dsSvc := &stubDataSourceService{
		getDataSource: func(_ context.Context, id string) (*types.DataSource, error) {
			return &types.DataSource{ID: id, KnowledgeBaseID: "kb1"}, nil
		},
		getSyncLogs: func(_ context.Context, _ string, limit int, offset int) ([]*types.SyncLog, error) {
			capturedLimit = limit
			capturedOffset = offset
			return []*types.SyncLog{}, nil
		},
	}
	kbSvc := &stubKBServiceForDS{
		getByID: func(_ context.Context, _ string) (*types.KnowledgeBase, error) {
			return &types.KnowledgeBase{ID: "kb1", TenantID: 1}, nil
		},
	}
	h := NewDataSourceHandler(dsSvc, kbSvc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/datasource/ds1/logs", nil)
	req = withDSCtx(req, 1)
	newDataSourceTestRouter(h).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if capturedLimit != 10 {
		t.Fatalf("expected default limit=10, got %d", capturedLimit)
	}
	if capturedOffset != 0 {
		t.Fatalf("expected default offset=0 (page 1), got %d", capturedOffset)
	}
}

func TestDataSource_GetSyncLogs_NonNumericLimitRejected(t *testing.T) {
	dsSvc := &stubDataSourceService{
		getDataSource: func(_ context.Context, id string) (*types.DataSource, error) {
			return &types.DataSource{ID: id, KnowledgeBaseID: "kb1"}, nil
		},
		getSyncLogs: func(_ context.Context, _ string, _ int, _ int) ([]*types.SyncLog, error) {
			t.Fatalf("service must not be called with non-numeric limit")
			return nil, nil
		},
	}
	kbSvc := &stubKBServiceForDS{
		getByID: func(_ context.Context, _ string) (*types.KnowledgeBase, error) {
			return &types.KnowledgeBase{ID: "kb1", TenantID: 1}, nil
		},
	}
	h := NewDataSourceHandler(dsSvc, kbSvc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/datasource/ds1/logs?limit=abc", nil)
	req = withDSCtx(req, 1)
	newDataSourceTestRouter(h).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-numeric limit, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestDataSource_GetSyncLogs_ZeroLimitRejected(t *testing.T) {
	dsSvc := &stubDataSourceService{
		getDataSource: func(_ context.Context, id string) (*types.DataSource, error) {
			return &types.DataSource{ID: id, KnowledgeBaseID: "kb1"}, nil
		},
		getSyncLogs: func(_ context.Context, _ string, _ int, _ int) ([]*types.SyncLog, error) {
			t.Fatalf("service must not be called with limit=0")
			return nil, nil
		},
	}
	kbSvc := &stubKBServiceForDS{
		getByID: func(_ context.Context, _ string) (*types.KnowledgeBase, error) {
			return &types.KnowledgeBase{ID: "kb1", TenantID: 1}, nil
		},
	}
	h := NewDataSourceHandler(dsSvc, kbSvc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/datasource/ds1/logs?limit=0", nil)
	req = withDSCtx(req, 1)
	newDataSourceTestRouter(h).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for limit=0, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestDataSource_GetSyncLogs_NegativeLimitRejected(t *testing.T) {
	dsSvc := &stubDataSourceService{
		getDataSource: func(_ context.Context, id string) (*types.DataSource, error) {
			return &types.DataSource{ID: id, KnowledgeBaseID: "kb1"}, nil
		},
		getSyncLogs: func(_ context.Context, _ string, _ int, _ int) ([]*types.SyncLog, error) {
			t.Fatalf("service must not be called with negative limit")
			return nil, nil
		},
	}
	kbSvc := &stubKBServiceForDS{
		getByID: func(_ context.Context, _ string) (*types.KnowledgeBase, error) {
			return &types.KnowledgeBase{ID: "kb1", TenantID: 1}, nil
		},
	}
	h := NewDataSourceHandler(dsSvc, kbSvc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/datasource/ds1/logs?limit=-5", nil)
	req = withDSCtx(req, 1)
	newDataSourceTestRouter(h).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative limit, got %d body=%s", w.Code, w.Body.String())
	}
}
