package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/wedrive"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestListDevicesReportsOfflineWhenNoAgentSocketExists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wedrive-devices.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveDevice{}))
	require.NoError(t, db.Create(&types.WeDriveDevice{
		ID: "device-stale-online", TenantID: 7, UserID: "owner", Name: "stale device", PublicKey: "public-key", Status: "online",
	}).Error)

	h := NewWeDriveHandler(service.NewWeDriveService(db, nil, nil), wedrive.NewHub())
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest("GET", "/api/wedrive/devices", nil)
	c.Request = req.WithContext(context.WithValue(req.Context(), types.CallerContextKey, types.Caller{
		TenantID: 7, UserID: "owner", Role: types.TenantRoleOwner,
	}))

	h.ListDevices(c)
	require.Equal(t, 200, w.Code)
	var devices []types.WeDriveDevice
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &devices))
	require.Len(t, devices, 1)
	require.Equal(t, "offline", devices[0].Status, "a historic database status must not claim the tool is currently connected")
}
