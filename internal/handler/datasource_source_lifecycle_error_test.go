package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/sirupsen/logrus"
)

func TestDataSource_SourceLifecycleErrorsDoNotExposeInternalDiagnostics(t *testing.T) {
	internalErr := errors.New("SQLSTATE 23503: UPDATE source_bindings SET credentials='tok_test_private_123' -- C:\\srv\\private\\weknora\\source.db")
	tests := []struct {
		name, path, body, operation string
		status                      int
		code, message               string
		setFailure                  func(*stubDataSourceService, error)
	}{
		{
			name: "unbind", path: "/datasource/ds1/unbind", body: `{}`, operation: "unbind",
			status: http.StatusBadRequest, code: "SOURCE_UNBIND_FAILED", message: "Failed to unbind data source",
			setFailure: func(service *stubDataSourceService, err error) {
				service.unbind = func(context.Context, string) (*types.DataSource, error) { return nil, err }
			},
		},
		{
			name: "clear", path: "/datasource/ds1/clear-source", body: `{"confirm":true,"scope":"current_and_history"}`, operation: "clear",
			status: http.StatusBadRequest, code: "SOURCE_CLEAR_FAILED", message: "Failed to clear source knowledge",
			setFailure: func(service *stubDataSourceService, err error) {
				service.clear = func(context.Context, string, bool, string) (*types.DataSource, error) { return nil, err }
			},
		},
		{
			name: "retry", path: "/datasource/ds1/clear-source/retry", body: `{"operation_id":"op1"}`, operation: "retry",
			status: http.StatusConflict, code: "SOURCE_CLEAR_RETRY_FAILED", message: "Failed to retry source clear",
			setFailure: func(service *stubDataSourceService, err error) {
				service.retryClear = func(context.Context, string, string) (*types.DataSource, error) { return nil, err }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logOutput bytes.Buffer
			captureLogger := logrus.New()
			captureLogger.SetOutput(&logOutput)
			captureLogger.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true, DisableColors: true})
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
			requestContext := context.WithValue(request.Context(), types.LoggerContextKey, logrus.NewEntry(captureLogger))
			request = request.WithContext(requestContext)
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
			logEntry := logOutput.String()
			for _, expected := range []string{"source lifecycle " + test.operation + " failed", "id=ds1", "code=" + test.code} {
				if !strings.Contains(logEntry, expected) {
					t.Fatalf("server log missing safe classification %q: %s", expected, logEntry)
				}
			}
			for _, diagnostic := range []string{"SQLSTATE", "source_bindings", "tok_test_private_123", "C:"} {
				if strings.Contains(response.Body.String(), diagnostic) || strings.Contains(logEntry, diagnostic) {
					t.Fatalf("public response or server log exposed internal diagnostic %q; response=%s log=%s", diagnostic, response.Body.String(), logEntry)
				}
			}
		})
	}
}
