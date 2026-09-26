package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	weddhub "github.com/Tencent/WeKnora/internal/wedrive"
	"github.com/gin-gonic/gin"
)

type WeDriveHandler struct {
	service *service.WeDriveService
	hub     *weddhub.Hub
}

func NewWeDriveHandler(service *service.WeDriveService, hub *weddhub.Hub) *WeDriveHandler {
	return &WeDriveHandler{service: service, hub: hub}
}

func (h *WeDriveHandler) CreateBrowserEventTicket(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	device, err := h.service.GetAuthorizedDevice(c.Request.Context(), caller.TenantID, caller.UserID, c.Param("device_id"), caller.Role.HasPermission(types.TenantRoleAdmin))
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	ticket, expires, err := h.hub.MintBrowserTicket(device.ID, caller.Role.HasPermission(types.TenantRoleAdmin))
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"ticket": ticket, "expires_at": expires})
}

// BrowserEvents consumes a single-use, one-minute ticket minted by an
// authenticated request because the browser WebSocket API cannot set the JWT
// Authorization header used by the rest of the frontend.
func (h *WeDriveHandler) BrowserEvents(c *gin.Context) {
	ticket, ok := h.hub.ConsumeBrowserTicket(c.Query("ticket"))
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid event ticket"})
		return
	}
	_ = h.hub.ServeBrowser(c.Writer, c.Request, ticket.DeviceID, ticket.CanTriggerScan)
}

func (h *WeDriveHandler) AgentEvents(c *gin.Context) {
	device, _, ok := h.signedAgentBody(c)
	if !ok {
		return
	}
	h.service.RecordAgentVersion(c.Request.Context(), device, c.GetHeader("X-WeDrive-Agent-Version"))
	if err := h.service.RecoverInterruptedScans(c.Request.Context(), device); err != nil {
		writeWeDriveError(c, err)
		return
	}
	_ = h.hub.ServeAgent(c.Writer, c.Request, device.ID)
}

func (h *WeDriveHandler) ListAgentSources(c *gin.Context) {
	device, _, ok := h.signedAgentBody(c)
	if !ok {
		return
	}
	h.service.RecordAgentVersion(c.Request.Context(), device, c.GetHeader("X-WeDrive-Agent-Version"))
	rows, err := h.service.ListDeviceSources(c.Request.Context(), device, service.SupportsWeDriveScanAttempts(c.GetHeader("X-WeDrive-Agent-Version")))
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	if rows == nil {
		rows = []*types.WeDriveSource{}
	}
	c.JSON(http.StatusOK, rows)
}

func wedriveCaller(c *gin.Context) (types.Caller, bool) {
	caller := types.CallerFromContext(c.Request.Context())
	if caller.TenantID == 0 || caller.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "workspace context missing"})
		return caller, false
	}
	return caller, true
}

func writeWeDriveError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrWeDriveNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrWeDriveForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrWeDriveInvalidState), errors.Is(err, service.ErrWeDriveInvalidSnapshot):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrWeDriveScanNotDue), errors.Is(err, service.ErrWeDriveScanBusy):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrWeDriveScanAttemptConflict), errors.Is(err, service.ErrWeDriveScanAttemptExpired):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "scan_attempt_conflict"})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}

func (h *WeDriveHandler) CreateConnection(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	var in service.WeComConnectionInput
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	row, err := h.service.CreateConnection(c.Request.Context(), caller.TenantID, in)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusCreated, row)
}

func (h *WeDriveHandler) ListConnections(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	rows, err := h.service.ListConnections(c.Request.Context(), caller.TenantID)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	if rows == nil {
		rows = []*types.WeComCLIConnection{}
	}
	c.JSON(http.StatusOK, rows)
}

func (h *WeDriveHandler) UpdateConnection(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	var in service.WeComConnectionInput
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	row, err := h.service.UpdateConnection(c.Request.Context(), caller.TenantID, c.Param("connection_id"), in)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h *WeDriveHandler) DeleteConnection(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	if err := h.service.DeleteConnection(c.Request.Context(), caller.TenantID, c.Param("connection_id")); err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *WeDriveHandler) CreateRegistration(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	result, err := h.service.CreateRegistration(c.Request.Context(), caller.TenantID, caller.UserID)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

// RegisterDevice is self-authenticated by the short-lived one-time code and is
// intentionally mounted before the global JWT middleware.
func (h *WeDriveHandler) RegisterDevice(c *gin.Context) {
	var in service.RegisterDeviceInput
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	device, err := h.service.RegisterDevice(c.Request.Context(), in)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusCreated, device)
}

func (h *WeDriveHandler) ListDevices(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	rows, err := h.service.ListDevices(c.Request.Context(), caller.TenantID, caller.UserID, caller.Role.HasPermission(types.TenantRoleAdmin))
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	if rows == nil {
		rows = []*types.WeDriveDevice{}
	}
	for _, device := range rows {
		if h.hub.IsAgentOnline(device.ID) {
			device.Status = "online"
		} else {
			device.Status = "offline"
		}
	}
	c.JSON(http.StatusOK, rows)
}

func (h *WeDriveHandler) RevokeDevice(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	err := h.service.RevokeDevice(c.Request.Context(), caller.TenantID, caller.UserID, c.Param("device_id"), caller.Role.HasPermission(types.TenantRoleAdmin))
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *WeDriveHandler) CreateSource(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	var in service.CreateWeDriveSourceInput
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	in.KnowledgeBaseID = c.Param("id")
	row, err := h.service.CreateSource(c.Request.Context(), caller.TenantID, caller.UserID, caller.Role, in)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusCreated, row)
}

func (h *WeDriveHandler) ListSources(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	rows, err := h.service.ListSources(c.Request.Context(), caller.TenantID, caller.UserID, caller.Role.HasPermission(types.TenantRoleAdmin), c.Query("kb_id"))
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	if rows == nil {
		rows = []*types.WeDriveSource{}
	}
	c.JSON(http.StatusOK, rows)
}

func (h *WeDriveHandler) DeleteSource(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	if err := h.service.DeleteSource(c.Request.Context(), caller.TenantID, c.Param("source_id")); err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *WeDriveHandler) UpdateSourceScanSettings(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	var in service.UpdateWeDriveScanSettingsInput
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	row, err := h.service.UpdateSourceScanSettings(c.Request.Context(), caller.TenantID, c.Param("source_id"), in)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h *WeDriveHandler) ApproveSource(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	var in struct {
		ConnectionID string `json:"connection_id"`
	}
	if c.ShouldBindJSON(&in) != nil || in.ConnectionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "connection_id is required"})
		return
	}
	row, err := h.service.ApproveSource(c.Request.Context(), caller.TenantID, caller.UserID, c.Param("source_id"), in.ConnectionID)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h *WeDriveHandler) RebindSourceConnection(c *gin.Context) {
	caller, ok := wedriveCaller(c)
	if !ok {
		return
	}
	var in struct {
		ConnectionID string `json:"connection_id"`
	}
	if c.ShouldBindJSON(&in) != nil || in.ConnectionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "connection_id is required"})
		return
	}
	row, err := h.service.RebindSourceConnection(c.Request.Context(), caller.TenantID, caller.UserID, c.Param("source_id"), in.ConnectionID)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h *WeDriveHandler) signedAgentBody(c *gin.Context) (*types.WeDriveDevice, []byte, bool) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 8<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot read request"})
		return nil, nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	device, err := h.service.VerifyAgentRequest(c.Request.Context(), c.GetHeader("X-WeDrive-Device-ID"), c.GetHeader("X-WeDrive-Timestamp"), c.GetHeader("X-WeDrive-Signature"), c.Request.Method, c.Request.URL.Path, body)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid device signature"})
		return nil, nil, false
	}
	return device, body, true
}

func (h *WeDriveHandler) BeginSnapshot(c *gin.Context) {
	device, body, ok := h.signedScanBody(c)
	if !ok {
		return
	}
	var in service.BeginSnapshotInput
	if json.Unmarshal(body, &in) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	row, err := h.service.BeginSnapshot(c.Request.Context(), device, in)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusCreated, row)
}

func (h *WeDriveHandler) UploadSnapshotItems(c *gin.Context) {
	device, body, ok := h.signedScanBody(c)
	if !ok {
		return
	}
	var in struct {
		Items []types.WeDriveInventoryItem `json:"items"`
	}
	if json.Unmarshal(body, &in) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if err := h.service.UploadSnapshotItems(c.Request.Context(), device, c.Param("snapshot_id"), in.Items); err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *WeDriveHandler) CommitSnapshot(c *gin.Context) {
	device, body, ok := h.signedScanBody(c)
	if !ok {
		return
	}
	var in service.CommitSnapshotInput
	if json.Unmarshal(body, &in) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	row, err := h.service.CommitSnapshot(c.Request.Context(), device, c.Param("snapshot_id"), in)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h *WeDriveHandler) ClaimScan(c *gin.Context) {
	device, body, ok := h.signedScanBody(c)
	if !ok {
		return
	}
	var in service.ClaimWeDriveScanInput
	if json.Unmarshal(body, &in) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	row, err := h.service.ClaimScan(c.Request.Context(), device, c.Param("source_id"), in)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h *WeDriveHandler) ReportScanFailure(c *gin.Context) {
	device, body, ok := h.signedScanBody(c)
	if !ok {
		return
	}
	var in service.ReportWeDriveScanFailureInput
	if json.Unmarshal(body, &in) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	row, err := h.service.ReportScanFailure(c.Request.Context(), device, c.Param("source_id"), in)
	if err != nil {
		writeWeDriveError(c, err)
		return
	}
	c.JSON(http.StatusOK, row)
}

// Check the version on this signed request, never the stored device version.
// Discovery filtering is only a convenience; every scan write enforces this.
func (h *WeDriveHandler) signedScanBody(c *gin.Context) (*types.WeDriveDevice, []byte, bool) {
	device, body, ok := h.signedAgentBody(c)
	if !ok {
		return nil, nil, false
	}
	h.service.RecordAgentVersion(c.Request.Context(), device, c.GetHeader("X-WeDrive-Agent-Version"))
	if !service.SupportsWeDriveScanAttempts(c.GetHeader("X-WeDrive-Agent-Version")) {
		c.JSON(http.StatusUpgradeRequired, gin.H{
			"error": "upgrade WeDrive sync tool to " + service.MinimumWeDriveAgentVersion + " or later",
			"code":  "agent_upgrade_required", "minimum_agent_version": service.MinimumWeDriveAgentVersion,
		})
		return nil, nil, false
	}
	return device, body, true
}
