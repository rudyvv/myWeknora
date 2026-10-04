package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestDataSource_SourceLifecycleErrorsDoNotExposeInternalDiagnostics(t *testing.T) {
	internalErr := errors.New("SQLSTATE 23503: UPDATE source_bindings SET credentials='tok_test_private_123' -- C:\\srv\\private\\weknora\\source.db")
	tests := []struct {
		name, path, body string
		status           int
		code, message    string
		setFailure       func(*stubDataSourceService, error)
	}{
		{
			name: "unbind", path: "/datasource/ds1/unbind", body: `{}`,
			status: http.StatusBadRequest, code: "SOURCE_UNBIND_FAILED", message: "Failed to unbind data source",
			setFailure: func(service *stubDataSourceService, err error) {
				service.unbind = func(context.Context, string) (*types.DataSource, error) { return nil, err }
			},
		},
		{
			name: "clear", path: "/datasource/ds1/clear-source", body: `{"confirm":true,"scope":"current_and_history"}`,
			status: http.StatusBadRequest, code: "SOURCE_CLEAR_FAILED", message: "Failed to clear source knowledge",
			setFailure: func(service *stubDataSourceService, err error) {
				service.clear = func(context.Context, string, bool, string) (*types.DataSource, error) { return nil, err }
			},
		},
		{
			name: "retry", path: "/datasource/ds1/clear-source/retry", body: `{"operation_id":"op1"}`,
			status: http.StatusConflict, code: "SOURCE_CLEAR_RETRY_FAILED", message: "Failed to retry source clear",
			setFailure: func(service *stubDataSourceService, err error) {
				service.retryClear = func(context.Context, string, string) (*types.DataSource, error) { return nil, err }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &stubDataSourceService{
				getDataSource: func(_ context.Context, id string) (*types.DataSource, error) {
					return &types.DataSource{ID: id, KnowledgeBaseID: "kb1"}, nil
				},
			}
			test.setFailure(service, internalErr)
			kbService := &stubKBServiceForDS{getByID: func(_ context.Context, id string) (*types.KnowledgeBase, error) {
				return &types.KnowledgeBase{ID: id, TenantID: 1}, nil
			}}
			request := withDSCtx(httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body)), 1)
			response := httptest.NewRecorder()
			newDataSourceTestRouter(NewDataSourceHandler(service, kbService)).ServeHTTP(response, request)

			if response.Code != test.status {
				t.Fatalf("expected %d, got %d: %s", test.status, response.Code, response.Body.String())
			}
			var envelope struct {
				Code  string `json:"code"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Code != test.code || envelope.Error != test.message {
				t.Fatalf("expected sanitized code/message %q/%q, got %q/%q", test.code, test.message, envelope.Code, envelope.Error)
			}
			for _, diagnostic := range []string{"SQLSTATE", "source_bindings", "tok_test_private_123", "C:"} {
				if strings.Contains(response.Body.String(), diagnostic) {
					t.Fatalf("response exposed internal diagnostic %q: %s", diagnostic, response.Body.String())
				}
			}
		})
	}
}
