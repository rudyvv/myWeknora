// Package wedrive consumes complete inventories uploaded by the Windows RPA
// Agent and materializes their content through wecom-cli. Directory discovery
// deliberately does not happen here.
package wedrive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

var (
	_                    datasource.StreamingConnector          = (*Connector)(nil)
	_                    datasource.KnowledgeFolderPathResolver = (*Connector)(nil)
	_                    datasource.DataSourceBindingValidator  = (*Connector)(nil)
	shareURLPattern                                             = regexp.MustCompile(`^https://drive\.weixin\.qq\.com/s\?[^\s#]*\bk=[^\s&#]+(?:&[^\s#]*)?$`)
	shareURLRedactor                                            = regexp.MustCompile(`https://drive\.weixin\.qq\.com/s\?[^\s\"']*\bk=[^\s\"']+`)
	cliErrorCodePattern                                         = regexp.MustCompile(`(?i)\b(?:err(?:or)?_?code|code)\b\s*[:=]\s*[\"']?([0-9]{4,8})\b`)
	cliHTTPStatusPattern                                        = regexp.MustCompile(`(?i)\b(?:http(?:\s+status)?|status(?:\s+code)?)\s*[:=]?\s*(401|403|404|429|5[0-9]{2})\b`)
	weDriveKnowledgeRoot                                        = "业务文档"
)

type Connector struct {
	db  *gorm.DB
	cli *cliRunner
}

func NewConnector(db *gorm.DB) *Connector { return &Connector{db: db, cli: newCLIRunner()} }
func (c *Connector) Type() string         { return types.ConnectorTypeWeComDrive }

type connectorConfig struct {
	SourceID     string
	ConnectionID string
}

func parseConfig(config *types.DataSourceConfig) (connectorConfig, error) {
	if config == nil {
		return connectorConfig{}, fmt.Errorf("%w: config is nil", datasource.ErrInvalidConfig)
	}
	sourceID, _ := config.Settings["source_id"].(string)
	connectionID, _ := config.Settings["connection_id"].(string)
	if sourceID == "" || connectionID == "" {
		return connectorConfig{}, fmt.Errorf("%w: source_id and connection_id are required", datasource.ErrInvalidConfig)
	}
	return connectorConfig{SourceID: sourceID, ConnectionID: connectionID}, nil
}

func (c *Connector) Validate(ctx context.Context, config *types.DataSourceConfig) error {
	cfg, err := parseConfig(config)
	if err != nil {
		return err
	}
	var source types.WeDriveSource
	if err := c.db.WithContext(ctx).Where("id = ? AND connection_id = ? AND status IN ? AND deleted_at IS NULL", cfg.SourceID, cfg.ConnectionID, []string{types.WeDriveSourceActive, types.WeDriveSourceAwaitingInventory}).First(&source).Error; err != nil {
		return fmt.Errorf("%w: active WeDrive source not found", datasource.ErrInvalidConfig)
	}
	return nil
}

func (c *Connector) ValidateDataSourceBinding(ctx context.Context, config *types.DataSourceConfig, ds *types.DataSource) error {
	if ds == nil {
		return datasource.ErrInvalidConfig
	}
	cfg, err := parseConfig(config)
	if err != nil {
		return err
	}
	query := c.db.WithContext(ctx).Model(&types.WeDriveSource{}).
		Where("id = ? AND connection_id = ? AND tenant_id = ? AND knowledge_base_id = ? AND status IN ? AND deleted_at IS NULL",
			cfg.SourceID, cfg.ConnectionID, ds.TenantID, ds.KnowledgeBaseID,
			[]string{types.WeDriveSourceAwaitingInventory, types.WeDriveSourceActive})
	if ds.ID == "" {
		query = query.Where("data_source_id = ?", "")
	} else {
		query = query.Where("data_source_id = ?", ds.ID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%w: WeDrive source does not belong to this data source", datasource.ErrInvalidConfig)
	}
	return nil
}

func (c *Connector) ListResources(ctx context.Context, config *types.DataSourceConfig, parentID string) ([]types.Resource, error) {
	if parentID != "" {
		return []types.Resource{}, nil
	}
	cfg, err := parseConfig(config)
	if err != nil {
		return nil, err
	}
	var source types.WeDriveSource
	if err := c.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", cfg.SourceID).First(&source).Error; err != nil {
		return nil, err
	}
	return []types.Resource{{ExternalID: source.ID, Name: source.Name, Type: "folder", URL: source.RootURL, HasChildren: true}}, nil
}

func (c *Connector) ResolveResourceAncestors(context.Context, *types.DataSourceConfig, []string) ([]string, error) {
	return []string{}, nil
}

// ResolveKnowledgeFolderPaths exposes the inventory's directory hierarchy to
// the generic knowledge-base importer. It contains no share links or content.
func (c *Connector) ResolveKnowledgeFolderPaths(ctx context.Context, config *types.DataSourceConfig) (map[string]string, error) {
	cfg, err := parseConfig(config)
	if err != nil {
		return nil, err
	}
	var source types.WeDriveSource
	if err := c.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", cfg.SourceID).First(&source).Error; err != nil {
		return nil, err
	}
	if source.LastSnapshotID == "" {
		return map[string]string{}, nil
	}
	var inventory []types.WeDriveInventoryItem
	if err := c.db.WithContext(ctx).Where("source_id = ? AND snapshot_id = ? AND item_type = ?", source.ID, source.LastSnapshotID, "file").Find(&inventory).Error; err != nil {
		return nil, err
	}
	paths := make(map[string]string, len(inventory))
	for _, item := range inventory {
		paths[item.ExternalID] = sourceFolderPath(item.Path)
	}
	return paths, nil
}

func (c *Connector) FetchAll(ctx context.Context, config *types.DataSourceConfig, _ []string) ([]types.FetchedItem, error) {
	var out []types.FetchedItem
	_, err := c.FetchStream(ctx, config, nil, &collectHandler{items: &out})
	return out, err
}

func (c *Connector) FetchIncremental(ctx context.Context, config *types.DataSourceConfig, cursor *types.SyncCursor) ([]types.FetchedItem, *types.SyncCursor, error) {
	var out []types.FetchedItem
	next, err := c.FetchStream(ctx, config, cursor, &collectHandler{items: &out})
	return out, next, err
}

type collectHandler struct{ items *[]types.FetchedItem }

func (h *collectHandler) Emit(_ context.Context, item types.FetchedItem) error {
	*h.items = append(*h.items, item)
	return nil
}
func (h *collectHandler) Checkpoint(context.Context, *types.SyncCursor) error { return nil }

func emitWithOutcome(ctx context.Context, h datasource.StreamHandler, item types.FetchedItem) (bool, error) {
	if reporter, ok := h.(datasource.StreamItemOutcomeHandler); ok {
		return reporter.EmitWithOutcome(ctx, item)
	}
	return true, h.Emit(ctx, item)
}

type itemState struct {
	Fingerprint  string `json:"fingerprint"`
	Title        string `json:"title"`
	MissingCount int    `json:"missing_count,omitempty"`
	MissingSince string `json:"missing_since,omitempty"`
}
type cursorState struct {
	SnapshotID              string               `json:"snapshot_id"`
	DeletionEvidenceVersion int                  `json:"deletion_evidence_version,omitempty"`
	Items                   map[string]itemState `json:"items"`
}

const deletionEvidenceVersion = 1

func decodeCursor(cursor *types.SyncCursor) cursorState {
	state := cursorState{Items: map[string]itemState{}}
	if cursor == nil || cursor.ConnectorCursor == nil {
		return state
	}
	raw, _ := json.Marshal(cursor.ConnectorCursor)
	_ = json.Unmarshal(raw, &state)
	if state.Items == nil {
		state.Items = map[string]itemState{}
	}
	return state
}

func encodeCursor(state cursorState) *types.SyncCursor {
	raw, _ := json.Marshal(state)
	var value map[string]interface{}
	_ = json.Unmarshal(raw, &value)
	return &types.SyncCursor{LastSyncTime: time.Now().UTC(), ConnectorCursor: value}
}

func (c *Connector) FetchStream(ctx context.Context, config *types.DataSourceConfig, cursor *types.SyncCursor, h datasource.StreamHandler) (*types.SyncCursor, error) {
	cfg, err := parseConfig(config)
	if err != nil {
		return nil, err
	}
	var source types.WeDriveSource
	if err := c.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", cfg.SourceID).First(&source).Error; err != nil {
		return nil, err
	}
	if source.LastSnapshotID == "" {
		return nil, fmt.Errorf("WeDrive source has no complete inventory")
	}
	var connection types.WeComCLIConnection
	if err := c.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", cfg.ConnectionID, source.TenantID).First(&connection).Error; err != nil {
		return nil, fmt.Errorf("WeCom CLI connection unavailable")
	}
	if connection.Credentials.BotID == "" || connection.Credentials.Secret == "" {
		return nil, fmt.Errorf("WeCom CLI connection credentials are not configured")
	}
	var inventory []types.WeDriveInventoryItem
	if err := c.db.WithContext(ctx).Where("source_id = ? AND snapshot_id = ?", source.ID, source.LastSnapshotID).Order("path ASC").Find(&inventory).Error; err != nil {
		return nil, err
	}

	prev := decodeCursor(cursor)
	next := cursorState{SnapshotID: prev.SnapshotID, DeletionEvidenceVersion: prev.DeletionEvidenceVersion, Items: make(map[string]itemState, len(prev.Items)+len(inventory))}
	// Checkpoints made while fetching content must retain entries that have not
	// been visited yet, including files missing from the current inventory.
	for id, state := range prev.Items {
		next.Items[id] = state
	}
	current := make(map[string]struct{}, len(inventory))
	fetchErrorCounts := map[string]int{}
	// Format support is known from the inventory alone. Count it before any
	// external fetch so a later global stop (for example the daily CLI quota)
	// still reports every unsupported extension accurately.
	unsupportedFormats := unsupportedFormatCounts(inventory)
	stats := datasource.SourceSyncStats{}
	for _, item := range inventory {
		if item.ItemType != "folder" {
			stats.Total++
		}
	}
	reportStats := func() {
		if reporter, ok := h.(datasource.SourceSyncStatsReporter); ok {
			reporter.ReportSourceSyncStats(stats)
		}
	}
	for index, item := range inventory {
		if err := ctx.Err(); err != nil {
			return encodeCursor(next), err
		}
		current[item.ExternalID] = struct{}{}
		if item.ItemType == "folder" {
			delete(next.Items, item.ExternalID)
			continue
		}
		old, existed := prev.Items[item.ExternalID]
		old.MissingCount, old.MissingSince = 0, ""
		if existed {
			next.Items[item.ExternalID] = old
		}
		// Directory discovery inventories every entry, but only supported
		// content may reach the CLI. Otherwise a format limitation is
		// misreported as a missing share-link error.
		if item.Consumability != "supported" {
			continue
		}
		fingerprint := inventoryFingerprint(item)
		if existed && old.Fingerprint == fingerprint {
			next.Items[item.ExternalID] = itemState{Fingerprint: fingerprint, Title: item.Name}
			stats.Unchanged++
			continue
		}
		fetched, fetchErr := c.cli.fetch(ctx, connection.ID, connection.Credentials, item)
		if fetchErr != nil {
			message := publicItemFetchError(item, fetchErr)
			fetchErrorCounts[message]++
			// 640459 is a tenant-side daily content-read quota. Continuing to
			// invoke the CLI for every remaining item cannot succeed and can make
			// the log look like a file-permission outage. Keep the checkpoint so a
			// later manual/scheduled run resumes the untouched inventory.
			if isDailyContentQuotaError(fetchErr) {
				stats.Failed = sumCounts(fetchErrorCounts) + sumCounts(unsupportedFormats)
				stats.Deferred = countDeferredConsumableFiles(inventory[index+1:], prev)
				reportStats()
				return encodeCursor(next), partialFetchError(fetchErrorCounts, unsupportedFormats)
			}
			if existed {
				next.Items[item.ExternalID] = old
			}
			continue
		}
		attachKnowledgeFolder(&fetched, item)
		applied, err := emitWithOutcome(ctx, h, fetched)
		if err != nil {
			return encodeCursor(next), err
		}
		if applied {
			next.Items[item.ExternalID] = itemState{Fingerprint: fingerprint, Title: item.Name}
		}
		if err := h.Checkpoint(ctx, encodeCursor(next)); err != nil {
			return encodeCursor(next), err
		}
	}

	// Missing entries require two distinct, consecutive complete snapshots plus
	// 24 hours. Content syncs can run repeatedly without a new directory scan.
	now := time.Now().UTC()
	missingIDs := make([]string, 0)
	for id := range prev.Items {
		if _, present := current[id]; !present {
			missingIDs = append(missingIDs, id)
		}
	}
	observations, presence, resetEvidence, err := c.missingSnapshotEvidence(ctx, source, prev, missingIDs)
	if err != nil {
		return encodeCursor(next), err
	}
	for _, id := range missingIDs {
		old := prev.Items[id]
		if resetEvidence {
			old.MissingCount, old.MissingSince = 0, ""
		}
		for _, snapshot := range observations {
			if _, found := presence[snapshot.ID][id]; found {
				old.MissingCount, old.MissingSince = 0, ""
				continue
			}
			if old.MissingCount == 0 && snapshot.CommittedAt != nil {
				old.MissingSince = snapshot.CommittedAt.UTC().Format(time.RFC3339Nano)
			}
			if old.MissingCount < 2 {
				old.MissingCount++
			}
		}
		missingSince, _ := time.Parse(time.RFC3339Nano, old.MissingSince)
		if old.MissingCount >= 2 && !missingSince.IsZero() && now.Sub(missingSince) >= 24*time.Hour {
			deleted := types.FetchedItem{ExternalID: id, Title: old.Title, IsDeleted: true, SourceResourceID: source.ID}
			applied, err := emitWithOutcome(ctx, h, deleted)
			if err != nil {
				return encodeCursor(next), err
			}
			if applied {
				delete(next.Items, id)
			} else {
				next.Items[id] = old
			}
			continue
		}
		next.Items[id] = old
	}
	next.SnapshotID = source.LastSnapshotID
	next.DeletionEvidenceVersion = deletionEvidenceVersion
	final := encodeCursor(next)
	stats.Failed = sumCounts(fetchErrorCounts) + sumCounts(unsupportedFormats)
	reportStats()
	if err := h.Checkpoint(ctx, final); err != nil {
		return final, err
	}
	if partial := partialFetchError(fetchErrorCounts, unsupportedFormats); partial != nil {
		return final, partial
	}
	return final, nil
}

// missingSnapshotEvidence returns every newly committed complete inventory
// since the last completed deletion check. Legacy or discontinuous cursors
// start a fresh streak at the current snapshot; their old missing counts are
// not proof of distinct scans.
func (c *Connector) missingSnapshotEvidence(ctx context.Context, source types.WeDriveSource, prev cursorState, missingIDs []string) ([]types.WeDriveSnapshot, map[string]map[string]struct{}, bool, error) {
	presence := make(map[string]map[string]struct{})
	if len(missingIDs) == 0 {
		return nil, presence, false, nil
	}
	var current types.WeDriveSnapshot
	if err := c.db.WithContext(ctx).Where("id = ? AND source_id = ? AND tenant_id = ? AND status = ?", source.LastSnapshotID, source.ID, source.TenantID, types.WeDriveSnapshotComplete).First(&current).Error; err != nil {
		return nil, nil, false, fmt.Errorf("load complete WeDrive snapshot: %w", err)
	}
	if current.CommittedAt == nil {
		return nil, nil, false, fmt.Errorf("complete WeDrive snapshot has no commit time")
	}
	observations := []types.WeDriveSnapshot{current}
	reset := prev.DeletionEvidenceVersion != deletionEvidenceVersion || prev.SnapshotID == ""
	if !reset && prev.SnapshotID == current.ID {
		return nil, presence, false, nil
	}
	if !reset {
		var previous types.WeDriveSnapshot
		if err := c.db.WithContext(ctx).Where("id = ? AND source_id = ? AND tenant_id = ? AND status = ?", prev.SnapshotID, source.ID, source.TenantID, types.WeDriveSnapshotComplete).First(&previous).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil, false, err
			}
			reset = true
		} else if previous.Sequence >= current.Sequence {
			reset = true
		} else {
			var scans []types.WeDriveSnapshot
			if err := c.db.WithContext(ctx).Where("source_id = ? AND tenant_id = ? AND status = ? AND sequence > ? AND sequence <= ?", source.ID, source.TenantID, types.WeDriveSnapshotComplete, previous.Sequence, current.Sequence).Order("sequence ASC").Find(&scans).Error; err != nil {
				return nil, nil, false, err
			}
			if len(scans) == 0 || scans[len(scans)-1].ID != current.ID {
				reset = true
			} else {
				observations = scans
			}
		}
	}
	if reset {
		observations = []types.WeDriveSnapshot{current}
	}
	for _, snapshot := range observations {
		if snapshot.CommittedAt == nil {
			return nil, nil, false, fmt.Errorf("complete WeDrive snapshot has no commit time")
		}
	}
	// A file that reappeared in any intervening complete snapshot breaks the
	// absence streak, even when no content sync ran for that snapshot.
	const batchSize = 300
	for scanStart := 0; scanStart < len(observations)-1; scanStart += batchSize {
		scanEnd := min(scanStart+batchSize, len(observations)-1)
		snapshotIDs := make([]string, 0, scanEnd-scanStart)
		for _, snapshot := range observations[scanStart:scanEnd] {
			snapshotIDs = append(snapshotIDs, snapshot.ID)
		}
		for itemStart := 0; itemStart < len(missingIDs); itemStart += batchSize {
			itemEnd := min(itemStart+batchSize, len(missingIDs))
			var rows []struct{ SnapshotID, ExternalID string }
			if err := c.db.WithContext(ctx).Model(&types.WeDriveInventoryItem{}).Select("snapshot_id, external_id").Where("source_id = ? AND snapshot_id IN ? AND external_id IN ?", source.ID, snapshotIDs, missingIDs[itemStart:itemEnd]).Scan(&rows).Error; err != nil {
				return nil, nil, false, err
			}
			for _, row := range rows {
				if presence[row.SnapshotID] == nil {
					presence[row.SnapshotID] = make(map[string]struct{})
				}
				presence[row.SnapshotID][row.ExternalID] = struct{}{}
			}
		}
	}
	return observations, presence, reset, nil
}

func sumCounts(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

// countDeferredFiles counts only file entries after a global stop condition.
// Their state is intentionally unknown until the next run, so they must not be
// represented as failed or unchanged.
func countDeferredConsumableFiles(items []types.WeDriveInventoryItem, prev cursorState) int {
	count := 0
	for _, item := range items {
		if item.ItemType != "folder" && item.Consumability == "supported" {
			old, existed := prev.Items[item.ExternalID]
			if !existed || old.Fingerprint != inventoryFingerprint(item) {
				count++
			}
		}
	}
	return count
}

func unsupportedFormatCounts(items []types.WeDriveInventoryItem) map[string]int {
	counts := map[string]int{}
	for _, item := range items {
		if item.ItemType != "folder" && item.Consumability != "supported" {
			counts[unsupportedExtension(item.Name)]++
		}
	}
	return counts
}

// attachKnowledgeFolder turns the Agent's relative source path into the
// standard knowledge folder path. The ingestion service already splits a
// path-qualified FileName into folder_path + display filename.
func attachKnowledgeFolder(fetched *types.FetchedItem, item types.WeDriveInventoryItem) {
	if fetched == nil {
		return
	}
	folder := sourceFolderPath(item.Path)
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(fetched.FileName), `\`, "/"))
	if name == "." || name == "" {
		name = path.Base(strings.ReplaceAll(strings.TrimSpace(item.Name), `\`, "/"))
	}
	if folder != "" {
		fetched.FileName = path.Join(folder, name)
	} else {
		fetched.FileName = name
	}
	if fetched.Metadata == nil {
		fetched.Metadata = map[string]string{}
	}
	fetched.Metadata["source_path"] = item.Path
	fetched.Metadata["folder_path"] = folder
}

func sourceFolderPath(sourcePath string) string {
	normalized := strings.Trim(strings.ReplaceAll(strings.TrimSpace(sourcePath), `\`, "/"), "/")
	if normalized == "" {
		return weDriveKnowledgeRoot
	}
	directory := path.Dir(normalized)
	if directory == "." {
		return weDriveKnowledgeRoot
	}
	return types.NormalizeKnowledgeFolderPath(path.Join(weDriveKnowledgeRoot, directory))
}

// fetchErrorDetails deliberately contains only a public category and the
// number of affected items. It must never include a file name, inventory ID,
// path, share URL, or output returned by wecom-cli.
func fetchErrorDetails(counts map[string]int) []string {
	details := make([]string, 0, len(counts))
	for message, count := range counts {
		if count > 0 {
			details = append(details, fmt.Sprintf("%s (%d items)", message, count))
		}
	}
	sort.Strings(details)
	return details
}

// unsupportedFormatDetails reports only extensions and aggregate counts. It
// deliberately omits inventory names and paths because sync logs are visible
// to more users than the source directory itself.
func unsupportedFormatDetails(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	exts := make([]string, 0, len(counts))
	for ext, count := range counts {
		if count > 0 {
			exts = append(exts, fmt.Sprintf("%s: %d item%s", ext, count, pluralSuffix(count)))
		}
	}
	if len(exts) == 0 {
		return ""
	}
	sort.Strings(exts)
	return "Unsupported file formats are not supported (" + strings.Join(exts, "; ") + ")"
}

func unsupportedExtension(name string) string {
	if ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name))); ext != "" {
		return ext
	}
	return "[no extension]"
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func inventoryFingerprint(item types.WeDriveInventoryItem) string {
	modified := ""
	if item.ModifiedAt != nil {
		modified = item.ModifiedAt.UTC().Format(time.RFC3339Nano)
	}
	share := "0"
	if item.ShareURL != "" {
		share = "1"
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{item.ExternalID, item.Path, item.DocID, modified, fmt.Sprint(item.Size), share, item.ContentFingerprint}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// publicFetchError is persisted to data-source logs and rendered in the UI.
// Keep transport diagnostics in server logs, but never expose inventory IDs,
// drive URLs, local filesystem paths, or CLI output to ordinary users.
func publicFetchError(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "offline file has no valid share link"):
		return "Some offline files do not have a usable share link"
	case strings.Contains(message, "error code=640008"):
		return "Some online documents are not accessible to the configured WeCom CLI identity"
	case strings.Contains(message, "error code=http_401"):
		return "The configured WeCom CLI credentials were rejected"
	case strings.Contains(message, "error code=http_403"):
		return "Some WeCom Drive content is not accessible to the configured WeCom CLI identity"
	case strings.Contains(message, "error code=http_404"):
		return "Some WeCom Drive content is no longer available to the configured WeCom CLI identity"
	case strings.Contains(message, "error code=http_429"):
		return "WeCom CLI rate limit was reached; retry later"
	case strings.Contains(message, "error code=640027"), strings.Contains(message, "error code=640029"):
		return "Some offline file share links cannot be downloaded by the configured WeCom CLI identity"
	case strings.Contains(message, "error code=640459"):
		return "The WeCom CLI bot daily file-content retrieval quota has been reached; retry after the quota resets"
	case strings.Contains(message, "create wecom cli profile"),
		strings.Contains(message, "credential initialization"):
		return "WeCom CLI credential storage is unavailable"
	case strings.Contains(message, "rate limit persisted"), strings.Contains(message, "error code=850005"):
		return "WeCom CLI rate limit was reached; retry later"
	case strings.Contains(message, "wecom-cli timed out"):
		return "WeCom CLI request timed out"
	case strings.Contains(message, "unsupported online document type"):
		return "Some online document types are not supported"
	default:
		return "Some files could not be fetched from WeCom Drive"
	}
}

func isDailyContentQuotaError(err error) bool {
	var apiErr *cliAPIError
	return errors.As(err, &apiErr) && apiErr.Code == "640459"
}

func partialFetchError(fetchErrors, unsupportedFormats map[string]int) error {
	details := fetchErrorDetails(fetchErrors)
	if unsupported := unsupportedFormatDetails(unsupportedFormats); unsupported != "" {
		details = append(details, unsupported)
	}
	if len(details) == 0 {
		return nil
	}
	sort.Strings(details)
	return &datasource.PartialFetchError{Details: details}
}

// publicItemFetchError adds only the safe, inventory-derived item class when
// the CLI did not return a machine-readable cause. This keeps the sync log
// actionable without leaking a resource name, path, share URL, or raw CLI
// output.
func publicItemFetchError(item types.WeDriveInventoryItem, err error) string {
	if strings.Contains(strings.ToLower(err.Error()), "offline file has no valid share link") {
		switch item.FailureCode {
		case "auto_share_ui_busy", "auto_share_row_unavailable", "auto_share_action_unavailable":
			return "Agent could not create some share links because the WeCom Drive sharing UI was unavailable"
		case "auto_share_link_not_created":
			return "Agent could not create some share links; check the source user's share permission and tenant sharing policy"
		case "auto_share_unexpected":
			return "Agent could not create some share links because the WeCom Drive sharing operation failed"
		}
	}
	message := publicFetchError(err)
	if message != "Some files could not be fetched from WeCom Drive" {
		return message
	}
	if item.DocID != "" {
		return "Some online documents could not be exported by the configured WeCom CLI identity"
	}
	return "Some offline files with a share link could not be downloaded by the configured WeCom CLI identity"
}

func redact(value string) string {
	return shareURLRedactor.ReplaceAllString(value, "<redacted-drive-share>")
}

type cliRunner struct {
	command     string
	profileRoot string
	timeout     time.Duration
	mu          sync.Mutex
	lastRun     time.Time
}

func newCLIRunner() *cliRunner {
	command := strings.TrimSpace(os.Getenv("WECOM_CLI_COMMAND"))
	if command == "" {
		command = "wecom-cli"
	}
	profileRoot := strings.TrimSpace(os.Getenv("WECOM_CLI_STATE_DIR"))
	if profileRoot == "" {
		// The worker's credential store belongs to the service identity, not
		// the interactive Windows user. LOCAL_STORAGE_BASE_DIR is already the
		// service-writable data root in development and container deployments.
		if storageRoot := strings.TrimSpace(os.Getenv("LOCAL_STORAGE_BASE_DIR")); storageRoot != "" {
			profileRoot = filepath.Join(filepath.Dir(storageRoot), "wecom-cli")
		} else if configRoot, err := os.UserConfigDir(); err == nil {
			profileRoot = filepath.Join(configRoot, "weknora", "wecom-cli")
		} else {
			profileRoot = filepath.Join(os.TempDir(), "weknora-wecom-cli")
		}
	}
	return &cliRunner{command: command, profileRoot: profileRoot, timeout: 2 * time.Minute}
}

func (r *cliRunner) run(ctx context.Context, profileID string, credentials types.WeComCLICredentials, cwd string, args ...string) (map[string]interface{}, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		payload, err := r.runOnce(ctx, profileID, credentials, cwd, args...)
		if err == nil {
			return payload, nil
		}
		lastErr = err
		var cliErr *cliAPIError
		if !errors.As(err, &cliErr) || cliErr.Code != "850005" {
			return nil, err
		}
		if err := waitContext(ctx, time.Duration(attempt+1)*10*time.Second); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("wecom-cli rate limit persisted after retries: %w", lastErr)
}

func (r *cliRunner) runOnce(ctx context.Context, profileID string, credentials types.WeComCLICredentials, cwd string, args ...string) (map[string]interface{}, error) {
	// One administrative CLI identity is shared by multiple knowledge bases.
	// Serialize subprocesses and leave a small gap to avoid burst limits.
	r.mu.Lock()
	defer r.mu.Unlock()
	profileDir, err := r.ensureAuthenticated(ctx, profileID, credentials)
	if err != nil {
		return nil, err
	}
	if delay := 2*time.Second - time.Since(r.lastRun); delay > 0 {
		if err := waitContext(ctx, delay); err != nil {
			return nil, err
		}
	}
	defer func() { r.lastRun = time.Now() }()

	callCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(callCtx, r.command, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "WECOM_CLI_CONFIG_DIR="+profileDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if callCtx.Err() != nil {
			return nil, fmt.Errorf("wecom-cli timed out")
		}
		// Some CLI subcommands exit non-zero while writing a structured API
		// error to stderr/stdout. Preserve only its numeric code so callers can
		// safely distinguish access failures without persisting command output.
		if cliErr := cliErrorFromOutput(stderr.String() + " " + stdout.String()); cliErr != nil {
			return nil, cliErr
		}
		return nil, fmt.Errorf("wecom-cli failed: %s", redact(strings.TrimSpace(stderr.String())))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		return nil, fmt.Errorf("wecom-cli returned invalid JSON")
	}
	if cliErr := payloadError(payload); cliErr != nil {
		return nil, cliErr
	}
	return payload, nil
}

// ensureAuthenticated materializes one encrypted wecom-cli profile per
// tenant-level connection. wecom-cli intentionally keeps credentials in its
// own encrypted credentials.enc and supports WECOM_CLI_CONFIG_DIR isolation;
// its hidden bot-id/secret flags are the non-TTY integration surface.
func (r *cliRunner) ensureAuthenticated(ctx context.Context, profileID string, credentials types.WeComCLICredentials) (string, error) {
	if profileID == "" || filepath.Base(profileID) != profileID || strings.ContainsAny(profileID, `/\\`) {
		return "", errors.New("invalid WeCom CLI profile id")
	}
	profileDir := filepath.Join(r.profileRoot, profileID)
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return "", fmt.Errorf("create WeCom CLI profile: %w", err)
	}
	fingerprint := credentialFingerprint(credentials)
	marker := filepath.Join(profileDir, ".weknora-credential-fingerprint")
	if current, err := os.ReadFile(marker); err == nil && subtle.ConstantTimeCompare(bytes.TrimSpace(current), []byte(fingerprint)) == 1 {
		if info, statErr := os.Stat(filepath.Join(profileDir, "credentials.enc")); statErr == nil && !info.IsDir() {
			return profileDir, nil
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(callCtx, r.command, "auth", "init", "--bot-id", credentials.BotID, "--secret", credentials.Secret)
	cmd.Env = append(os.Environ(), "WECOM_CLI_CONFIG_DIR="+profileDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if callCtx.Err() != nil {
			return "", errors.New("wecom-cli credential initialization timed out")
		}
		detail := strings.TrimSpace(stderr.String() + " " + stdout.String())
		detail = strings.ReplaceAll(redact(detail), credentials.Secret, "<redacted-secret>")
		return "", fmt.Errorf("wecom-cli credential initialization failed: %s", detail)
	}
	if err := os.WriteFile(marker, []byte(fingerprint+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("persist WeCom CLI credential marker: %w", err)
	}
	return profileDir, nil
}

func credentialFingerprint(credentials types.WeComCLICredentials) string {
	sum := sha256.Sum256([]byte(credentials.BotID + "\x00" + credentials.Secret))
	return hex.EncodeToString(sum[:])
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type cliAPIError struct{ Code string }

func (e *cliAPIError) Error() string { return "wecom-cli error code=" + e.Code }

func payloadError(payload map[string]interface{}) error {
	if raw, ok := payload["error"].(map[string]interface{}); ok && len(raw) > 0 {
		return &cliAPIError{Code: fmt.Sprint(raw["code"])}
	}
	if code, ok := payload["errcode"].(float64); ok && code != 0 {
		return &cliAPIError{Code: fmt.Sprint(int(code))}
	}
	return nil
}

func cliErrorFromOutput(output string) error {
	// Current Windows wecom-cli releases can report this quota condition only
	// as a localized sentence, with no errcode/code field. Normalize it to the
	// API code so FetchStream can stop immediately and retain its checkpoint.
	if strings.Contains(output, "当前用户通过机器人获取微盘文件内容已超过当日最大次数限制") {
		return &cliAPIError{Code: "640459"}
	}
	if match := cliErrorCodePattern.FindStringSubmatch(output); len(match) == 2 {
		return &cliAPIError{Code: match[1]}
	}
	if match := cliHTTPStatusPattern.FindStringSubmatch(output); len(match) == 2 {
		return &cliAPIError{Code: "http_" + match[1]}
	}
	return nil
}

func (r *cliRunner) fetch(ctx context.Context, profileID string, credentials types.WeComCLICredentials, item types.WeDriveInventoryItem) (types.FetchedItem, error) {
	base := types.FetchedItem{ExternalID: item.ExternalID, Title: item.Name, URL: "", UpdatedAt: time.Now().UTC(), SourceResourceID: item.SourceID,
		Metadata: map[string]string{"channel": "wecom_drive", "source_path": item.Path}}
	if item.ModifiedAt != nil {
		base.UpdatedAt = *item.ModifiedAt
	}
	switch {
	case strings.HasPrefix(item.DocID, "w2_"), strings.HasPrefix(item.DocID, "w3_"):
		payload, err := r.run(ctx, profileID, credentials, "", "doc", "contents", "get", "--docid", item.DocID, "--content-type", "markdown")
		if err != nil {
			return base, err
		}
		content, _ := payload["content"].(string)
		if strings.TrimSpace(content) == "" {
			return base, errors.New("wecom-cli returned empty document")
		}
		base.Content, base.ContentType, base.FileName = []byte(normalizeText(content)), "text/markdown", markdownName(item.Name)
		return base, nil
	case strings.HasPrefix(item.DocID, "a1_"):
		payload, err := r.run(ctx, profileID, credentials, "", "smartpage", "pages", "get", "--docid", item.DocID, "--content-type", "markdown")
		if err != nil {
			return base, err
		}
		content := smartpageMarkdown(payload)
		if content == "" {
			return base, errors.New("wecom-cli returned empty smart document")
		}
		base.Content, base.ContentType, base.FileName = []byte(content), "text/markdown", markdownName(item.Name)
		return base, nil
	case strings.HasPrefix(item.DocID, "e3_"):
		content, err := r.sheetMarkdown(ctx, profileID, credentials, item.DocID)
		if err != nil {
			return base, err
		}
		base.Content, base.ContentType, base.FileName = []byte(content), "text/markdown", markdownName(item.Name)
		return base, nil
	case item.DocID != "":
		return base, fmt.Errorf("unsupported online document type")
	default:
		return r.download(ctx, profileID, credentials, item, base)
	}
}

func (r *cliRunner) sheetMarkdown(ctx context.Context, profileID string, credentials types.WeComCLICredentials, docID string) (string, error) {
	meta, err := r.run(ctx, profileID, credentials, "", "sheet", "get", "--docid", docID)
	if err != nil {
		return "", err
	}
	sheets, _ := meta["sheets"].([]interface{})
	var parts []string
	for _, raw := range sheets {
		sheet, _ := raw.(map[string]interface{})
		sid, _ := sheet["sheet_id"].(string)
		dataRange, _ := sheet["data_range"].(string)
		title, _ := sheet["title"].(string)
		if sid == "" || dataRange == "" {
			continue
		}
		payload, err := r.run(ctx, profileID, credentials, "", "sheet", "ranges", "get", "--docid", docID, "--sheet-id", sid, "--range", dataRange, "--mode", "csv")
		if err != nil {
			return "", err
		}
		content, _ := payload["content"].(string)
		if content == "" {
			content, _ = payload["csv"].(string)
		}
		if table := csvMarkdown(content); table != "" {
			parts = append(parts, "## "+first(title, sid)+"\n\n"+table)
		}
	}
	if len(parts) == 0 {
		return "", errors.New("wecom-cli returned no readable sheet data")
	}
	return strings.Join(parts, "\n\n") + "\n", nil
}

func (r *cliRunner) download(ctx context.Context, profileID string, credentials types.WeComCLICredentials, item types.WeDriveInventoryItem, base types.FetchedItem) (types.FetchedItem, error) {
	if !shareURLPattern.MatchString(item.ShareURL) {
		return base, errors.New("offline file has no valid share link")
	}
	tmp, err := os.MkdirTemp("", "weknora-wedrive-*")
	if err != nil {
		return base, err
	}
	defer os.RemoveAll(tmp)
	// wecom-cli resolves its authenticated profile relative to its own process
	// context on Windows. Changing cmd.Dir to a disposable download directory
	// therefore loses the initialized session (893201). Keep its working
	// directory stable and use the CLI's explicit output directory instead.
	payload, err := r.run(ctx, profileID, credentials, "", "disk", "files", "download", "--url", item.ShareURL, "--output-dir", tmp)
	if err != nil {
		return base, err
	}
	data, fileName, err := readCLIDownloadPayload(tmp, item.Name, payload)
	if err != nil {
		return base, err
	}
	base.Content, base.ContentType, base.FileName = data, first(item.MimeType, "application/octet-stream"), fileName
	return base, nil
}

func readCLIDownloadPayload(tmp, itemName string, payload map[string]interface{}) ([]byte, string, error) {
	// Some CLI versions write the attachment itself, while others write only a
	// JSON response sidecar containing file_content. Prefer a real attachment.
	if candidate, candidateErr := controlledDownloadPath(tmp, itemName); candidateErr == nil {
		data, err := os.ReadFile(candidate)
		if err != nil {
			return nil, "", errors.New("wecom-cli download file is unavailable")
		}
		return data, filepath.Base(candidate), nil
	}
	// Windows CLI 1.2 writes a JSON response into --output-dir but may report
	// a different process-local file_path. Read only the copy in our own temp
	// directory; never follow the reported path outside that boundary.
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return nil, "", errors.New("wecom-cli download file is unavailable")
	}
	sidecarName := ""
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		if sidecarName != "" {
			return nil, "", errors.New("wecom-cli returned ambiguous download responses")
		}
		sidecarName = entry.Name()
	}
	if sidecarName != "" {
		candidate, err := controlledDownloadPath(tmp, sidecarName)
		if err != nil {
			return nil, "", errors.New("wecom-cli download file is unavailable")
		}
		data, err := os.ReadFile(candidate)
		if err != nil {
			return nil, "", errors.New("wecom-cli download file is unavailable")
		}
		var sidecar map[string]interface{}
		if err := json.Unmarshal(data, &sidecar); err != nil {
			return nil, "", errors.New("wecom-cli returned invalid download response")
		}
		content, _, err := decodeCLIContent(sidecar["file_content"], sidecar["size"])
		if err != nil {
			return nil, "", err
		}
		return content, itemName, nil
	}
	if p, _ := payload["file_path"].(string); p != "" {
		candidate := p
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(tmp, candidate)
		}
		candidate, err := filepath.Abs(candidate)
		if err != nil {
			return nil, "", err
		}
		root, _ := filepath.Abs(tmp)
		rel, relErr := filepath.Rel(root, candidate)
		if relErr != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)) {
			return nil, "", errors.New("wecom-cli returned a path outside its temporary directory")
		}
		validated, err := controlledDownloadPath(tmp, filepath.Base(candidate))
		if err != nil || validated != candidate {
			return nil, "", errors.New("wecom-cli download file is unavailable")
		}
		data, err := os.ReadFile(candidate)
		if err != nil {
			return nil, "", errors.New("wecom-cli download file is unavailable")
		}
		if strings.EqualFold(filepath.Ext(candidate), ".json") {
			var sidecar map[string]interface{}
			if err := json.Unmarshal(data, &sidecar); err != nil {
				return nil, "", errors.New("wecom-cli returned invalid download response")
			}
			content, _, err := decodeCLIContent(sidecar["file_content"], sidecar["size"])
			if err != nil {
				return nil, "", err
			}
			return content, itemName, nil
		}
		return data, filepath.Base(candidate), nil
	}
	content, name, err := decodeCLIContent(payload["file_content"], payload["size"])
	if err != nil {
		return nil, "", err
	}
	if name == "" {
		name = itemName
	}
	return content, name, nil
}

func decodeCLIContent(raw, size interface{}) ([]byte, string, error) {
	content := ""
	fileName := ""
	if obj, ok := raw.(map[string]interface{}); ok {
		content, _ = obj["content"].(string)
		if n, _ := obj["file_name"].(string); n != "" {
			fileName = filepath.Base(n)
		}
	} else {
		content, _ = raw.(string)
	}
	if content == "" {
		return nil, "", errors.New("wecom-cli response contains no file content")
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(content)
	expectedSize, hasSize := numericInt64(size)
	if decodeErr == nil && hasSize && int64(len(decoded)) == expectedSize {
		return decoded, fileName, nil
	}
	return []byte(content), fileName, nil
}

// controlledDownloadPath returns only a regular file directly inside the
// caller-owned CLI output directory. It deliberately rejects traversal and
// symlinks, so a malformed inventory name cannot escape the temporary root.
func controlledDownloadPath(root, fileName string) (string, error) {
	base := filepath.Base(fileName)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", errors.New("invalid downloaded file name")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidate, err := filepath.Abs(filepath.Join(rootAbs, base))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)) {
		return "", errors.New("download path escapes temporary directory")
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("downloaded output is not a regular file")
	}
	return candidate, nil
}

func numericInt64(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}

func smartpageMarkdown(payload map[string]interface{}) string {
	var parts []string
	if title, _ := payload["doc_title"].(string); strings.TrimSpace(title) != "" {
		parts = append(parts, "# "+strings.TrimSpace(title))
	}
	pages, _ := payload["pages"].([]interface{})
	for _, raw := range pages {
		page, _ := raw.(map[string]interface{})
		title, _ := page["page_title"].(string)
		content := ""
		if obj, ok := page["content"].(map[string]interface{}); ok {
			content, _ = obj["markdown_content"].(string)
			if content == "" {
				content, _ = obj["text"].(string)
			}
		}
		if content == "" {
			content, _ = page["content_file_inner"].(string)
		}
		if title != "" {
			parts = append(parts, "## "+title)
		}
		if strings.TrimSpace(content) != "" {
			parts = append(parts, normalizeText(content))
		}
	}
	return strings.Join(parts, "\n\n")
}

func csvMarkdown(value string) string {
	rows, err := csv.NewReader(strings.NewReader(value)).ReadAll()
	if err != nil || len(rows) == 0 {
		return ""
	}
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}
	if width == 0 {
		return ""
	}
	for i := range rows {
		for len(rows[i]) < width {
			rows[i] = append(rows[i], "")
		}
		for j := range rows[i] {
			rows[i][j] = strings.ReplaceAll(strings.TrimSpace(rows[i][j]), "|", "\\|")
		}
	}
	line := func(row []string) string { return "| " + strings.Join(row, " | ") + " |" }
	lines := []string{line(rows[0]), line(repeat("---", width))}
	for _, row := range rows[1:] {
		lines = append(lines, line(row))
	}
	return strings.Join(lines, "\n")
}

func repeat(value string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = value
	}
	return out
}
func normalizeText(v string) string {
	return strings.ReplaceAll(strings.ReplaceAll(v, "\r\n", "\n"), "\r", "\n")
}
func markdownName(v string) string {
	return strings.TrimSuffix(filepath.Base(v), filepath.Ext(v)) + ".md"
}
func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "untitled"
}

// deterministicInventoryOrder is kept local so tests can prove the connector
// never changes behaviour because a database returned rows in another order.
func deterministicInventoryOrder(items []types.WeDriveInventoryItem) {
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
}
