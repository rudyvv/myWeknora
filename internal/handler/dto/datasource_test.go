package dto

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
)

func TestDataSourceResponse_OmitsCredentials(t *testing.T) {
	cfg := types.DataSourceConfig{
		Type: "github",
		Credentials: map[string]interface{}{
			"token": "ghp-secret-do-not-leak",
		},
		ResourceIDs: []string{"repo-1"},
		Settings:    map[string]interface{}{"branch": "main"},
	}
	blob, _ := cfg.ToJSON()
	ds := &types.DataSource{
		ID:     "ds-1",
		Name:   "github-prod",
		Type:   "github",
		Config: blob,
	}
	body, err := json.Marshal(NewDataSourceResponse(ds))
	assert.NoError(t, err)
	s := string(body)
	assert.NotContains(t, s, "ghp-secret-do-not-leak")
	// The inner config object must not carry the credentials map (the
	// DataSourceConfigDTO type omits it structurally).
	var raw map[string]json.RawMessage
	assert.NoError(t, json.Unmarshal(body, &raw))
	if cfgRaw, ok := raw["config"]; ok {
		var inner map[string]json.RawMessage
		assert.NoError(t, json.Unmarshal(cfgRaw, &inner))
		_, hasCredsInConfig := inner["credentials"]
		assert.False(t, hasCredsInConfig,
			"credentials map must not appear inside the config DTO")
	}
	// Top-level credentials map is just the "configured?" indicator,
	// replaces the removed GET /credentials endpoint.
	assert.Contains(t, s, `"credentials":{"credentials":{"configured":true}}`)
	// Non-secret config fields pass through.
	assert.Contains(t, s, "repo-1")
	assert.Contains(t, s, "branch")
	assert.Contains(t, s, "main")
}

func TestDataSourceResponse_NilSafe(t *testing.T) {
	assert.Nil(t, NewDataSourceResponse(nil))
	assert.Equal(t, []*DataSourceResponse{}, NewDataSourceResponses(nil))
}

func TestDataSourceResponse_RSSFeedURLsFromCredentials(t *testing.T) {
	cfg := types.DataSourceConfig{
		Type: types.ConnectorTypeRSS,
		Credentials: map[string]interface{}{
			"feed_urls": "https://example.com/a.xml\nhttps://example.com/b.xml",
		},
		ResourceIDs: []string{"https://example.com/a.xml"},
	}
	blob, _ := cfg.ToJSON()
	ds := &types.DataSource{
		ID:     "ds-rss",
		Name:   "my-rss",
		Type:   types.ConnectorTypeRSS,
		Config: blob,
	}
	resp := NewDataSourceResponse(ds)
	assert.NotNil(t, resp.Config)
	assert.Equal(t, "https://example.com/a.xml\nhttps://example.com/b.xml", resp.Config.Settings["feed_urls"])
	assert.False(t, resp.Credentials["credentials"].Configured)
	body, err := json.Marshal(resp)
	assert.NoError(t, err)
	s := string(body)
	assert.Contains(t, s, "https://example.com/a.xml")
	assert.NotContains(t, s, "Bearer secret")
}

func TestDataSourceResponse_RSSAuthHeadersConfigured(t *testing.T) {
	cfg := types.DataSourceConfig{
		Type: types.ConnectorTypeRSS,
		Credentials: map[string]interface{}{
			"auth_headers": "Authorization: Bearer secret",
		},
		Settings: map[string]interface{}{
			"feed_urls": "https://example.com/feed.xml",
		},
	}
	blob, _ := cfg.ToJSON()
	ds := &types.DataSource{
		ID:     "ds-rss-auth",
		Name:   "my-rss",
		Type:   types.ConnectorTypeRSS,
		Config: blob,
	}
	resp := NewDataSourceResponse(ds)
	assert.True(t, resp.Credentials["credentials"].Configured)
	body, err := json.Marshal(resp)
	assert.NoError(t, err)
	assert.NotContains(t, string(body), "Bearer secret")
}

func TestDataSourceResponse_NoConfig(t *testing.T) {
	ds := &types.DataSource{ID: "x", Name: "x"}
	body, err := json.Marshal(NewDataSourceResponse(ds))
	assert.NoError(t, err)
	// No config jsonb stored → no config object in the response.
	assert.NotContains(t, string(body), `"config":`)
}

func TestWeDriveResponseRedactsHistoricalRawErrors(t *testing.T) {
	secretURL := "https://drive.example.com/share/private-token"
	result := types.JSON(`{"failed":1,"source_deferred":2,"errors":[{"message":"` + secretURL + `"}]}`)
	log := &types.SyncLog{ErrorMessage: "fetch failed: " + secretURL, Result: result}
	ds := &types.DataSource{
		Type: types.ConnectorTypeWeComDrive, ErrorMessage: "fetch failed: " + secretURL,
		LastSyncResult: result, LatestSyncLog: log,
	}
	response := NewDataSourceResponse(ds)
	body, err := json.Marshal(response)
	assert.NoError(t, err)
	assert.NotContains(t, string(body), secretURL)
	assert.Contains(t, string(body), `"source_deferred":2`)
	assert.Equal(t, "fetch failed: "+secretURL, ds.ErrorMessage)
	assert.Equal(t, "fetch failed: "+secretURL, log.ErrorMessage)

	safeLog := SafeWeDriveSyncLog(log)
	logBody, err := json.Marshal(safeLog)
	assert.NoError(t, err)
	assert.NotContains(t, string(logBody), secretURL)
	assert.Equal(t, "fetch failed: "+secretURL, log.ErrorMessage)
}

func TestWeDriveResponseKeepsSafeAggregateFailureReasons(t *testing.T) {
	message := "Agent could not create some share links because the WeCom Drive sharing UI was unavailable (17 items); " +
		"Agent could not create some share links; check the source user's share permission and tenant sharing policy (1 items); " +
		"Some offline files do not have a usable share link (1 items); " +
		"Some online documents could not be exported by the configured WeCom CLI identity (14 items); " +
		"The WeCom CLI bot daily file-content retrieval quota has been reached; retry after the quota resets (1 items); " +
		"Unsupported file formats are not supported (.rp: 1 item; .sql: 28 items); " +
		"https://drive.example.com/share/private-token"
	log := SafeWeDriveSyncLog(&types.SyncLog{ErrorMessage: message})
	assert.Contains(t, log.ErrorMessage, "sharing UI was unavailable (17 items)")
	assert.Contains(t, log.ErrorMessage, "tenant sharing policy (1 items)")
	assert.Contains(t, log.ErrorMessage, "usable share link (1 items)")
	assert.Contains(t, log.ErrorMessage, "exported by the configured WeCom CLI identity (14 items)")
	assert.Contains(t, log.ErrorMessage, "quota has been reached")
	assert.Contains(t, log.ErrorMessage, ".rp: 1 item; .sql: 28 items")
	assert.NotContains(t, log.ErrorMessage, "private-token")
}
