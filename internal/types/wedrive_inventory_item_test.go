package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Agent sends a share_url only to the signed inventory-ingest endpoint.
// It must survive JSON decoding so the server can encrypt it for wecom-cli,
// but it must never be serialized in normal API responses.
func TestWeDriveInventoryItemAcceptsInboundShareURLWithoutMarshalingIt(t *testing.T) {
	var item WeDriveInventoryItem
	err := json.Unmarshal([]byte(`{
		"external_id":"file-1",
		"name":"proposal.pdf",
		"path":"proposal.pdf",
		"item_type":"file",
		"share_url":"https://drive.weixin.qq.com/s?k=token"
	}`), &item)
	require.NoError(t, err)
	require.Equal(t, "https://drive.weixin.qq.com/s?k=token", item.ShareURL)

	payload, err := json.Marshal(item)
	require.NoError(t, err)
	require.NotContains(t, string(payload), `"share_url":`)
	require.NotContains(t, string(payload), "s?k=token")
}
