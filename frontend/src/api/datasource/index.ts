import { get, post, put, del } from '../../utils/request'

// --- Types ---

export interface DataSource {
  id: string
  tenant_id: number
  knowledge_base_id: string
  name: string
  type: string
  config: any
  sync_schedule: string
  sync_mode: 'incremental' | 'full'
  status: 'active' | 'paused' | 'error'
  conflict_strategy: 'overwrite' | 'skip'
  sync_deletions: boolean
  last_sync_at: string | null
  last_sync_result: any
  error_message: string
  // Single-field "credentials" map from the main response — DataSource
  // credentials are a per-connector atomic set.
  credentials?: { credentials: { configured: boolean } }
  created_at: string
  updated_at: string
  latest_sync_log?: SyncLog
}

/**
 * One user-facing failure sample. The backend sends a stable i18n `code`
 * (+ interpolation `params`) so the UI localises it to the viewer's language;
 * `message` is a fallback when no i18n key exists (old logs decode into it).
 */
export interface SyncItemError {
  title?: string
  code?: string
  params?: Record<string, string>
  message?: string
}

export interface SyncResultDetail {
  source?: {
    snapshot: { id: string; state: string; commit_sha: string; detected_commit_sha?: string; target_commit_sha?: string; publication_checked?: boolean; project_id: string; repository_url: string;
      manifest_complete: boolean; member_count: number; file_count: number; chunk_count: number; error?: string; previous_commit_sha?: string;
      previous_snapshot_id?: string;
      published_at?: string; last_successful_published_at?: string; previous_published_at?: string;
      added_count?: number; changed_count?: number; deleted_count?: number; renamed_count?: number;
      parsed_count?: number; reused_file_count?: number; reused_chunk_count?: number; embedded_chunk_count?: number; reused_vector_count?: number };
    members: Array<{ path: string; status: string; reason: string; source_file_id: string; file_version_id: string; change?: string; previous_path?: string; parse_reused?: boolean }>;
  }
  total?: number
  created?: number
  updated?: number
  deleted?: number
  skipped?: number
  failed?: number
	/** Inventory-backed connectors report these even when no file reached ingestion. */
	inventory_total?: number
	source_unchanged?: number
	source_failed?: number
	source_deferred?: number
  /** Per-item failure samples (capped); localised in the sync-log drawer. */
  errors?: SyncItemError[]
}

export interface SyncLog {
  id: string
  data_source_id: string
  status: 'queued' | 'running' | 'success' | 'partial' | 'failed' | 'canceled'
  started_at: string
  finished_at: string | null
  items_total: number
  items_created: number
  items_updated: number
  items_deleted: number
  items_skipped: number
  items_failed: number
  error_message: string
  result?: SyncResultDetail
}

export interface ConnectorMeta {
  type: string
  name: string
  description: string
  icon: string
  priority: number
  auth_type: string
  capabilities: string[]
}

export interface Resource {
  external_id: string
  name: string
  type: string
  description: string
  url: string
  parent_id?: string
  has_children?: boolean
}

// --- API calls ---

export function getConnectorTypes() {
  return get('/api/v1/datasource/types')
}

export function listDataSources(kbId: string) {
  return get(`/api/v1/datasource?kb_id=${encodeURIComponent(kbId)}`)
}

export function getDataSource(id: string) {
  return get(`/api/v1/datasource/${id}`)
}

export function createDataSource(data: Partial<DataSource>) {
  return post('/api/v1/datasource', data)
}

export function updateDataSource(id: string, data: Partial<DataSource>) {
  return put(`/api/v1/datasource/${id}`, data)
}

export function deleteDataSource(id: string) {
  return del(`/api/v1/datasource/${id}`)
}

export function validateConnection(id: string) {
  return post(`/api/v1/datasource/${id}/validate`, {})
}

// Validate credentials without persisting (during creation or credential replacement).
export function validateCredentials(type: string, credentials: Record<string, any>) {
  return post('/api/v1/datasource/validate-credentials', { type, credentials })
}

// listResources lists selectable resources for a data source. Pass parentId to
// lazily load the direct children of a resource (e.g. expanding a Feishu wiki
// space/node), which avoids traversing the whole tree up front.
export function listResources(id: string, parentId?: string) {
  const query = parentId ? `?parent_id=${encodeURIComponent(parentId)}` : ''
  return get(`/api/v1/datasource/${id}/resources${query}`, { timeout: 120000 })
}

// resolveResourceAncestors returns the ExternalIDs of every parent that must be
// expanded to reveal the given (possibly deeply nested) selections in a lazily
// loaded picker. Used when editing a data source to restore an existing selection.
export function resolveResourceAncestors(id: string, resourceIds: string[]) {
  return post(`/api/v1/datasource/${id}/resource-ancestors`, { resource_ids: resourceIds }, { timeout: 120000 })
}

export function triggerSync(id: string) {
	return post(`/api/v1/datasource/${id}/sync`, {})
}

export interface SourcePreview {
  project_id: string
  branch: string
  commit_sha: string
  rules_version: string
  can_sync: boolean
  files: Array<{ path: string; blob_sha: string; size: number; status: string; reason: string; generated: boolean; encoding?: string }>
  checks: Array<{ name: string; ready: boolean; message: string }>
  warnings: string[]
}

export async function previewSource(id: string, settings: Record<string, unknown>): Promise<SourcePreview> {
  const response: any = await post(`/api/v1/datasource/${id}/source-preview`, { settings }, { timeout: 150000 })
  return response.data ?? response
}

export function pauseDataSource(id: string) {
  return post(`/api/v1/datasource/${id}/pause`, {})
}

export function resumeDataSource(id: string) {
  return post(`/api/v1/datasource/${id}/resume`, {})
}

export function getSyncLogs(id: string, limit = 20, offset = 0) {
  return get(`/api/v1/datasource/${id}/logs?limit=${limit}&offset=${offset}`)
}

// ----------------------------------------------------------------------------
// Data source credential subresource. Unlike the other three resources,
// DataSource exposes a single logical field "credentials" because connector
// auth is a per-connector atomic map. See internal/handler/dto/datasource.go.
// ----------------------------------------------------------------------------

export interface DataSourceCredentialsResponse {
  fields: {
    credentials: { configured: boolean }
  }
}

export async function putDataSourceCredentials(
  id: string,
  credentials: Record<string, unknown>,
): Promise<DataSourceCredentialsResponse> {
  const response: any = await put(`/api/v1/datasource/${id}/credentials`, { credentials })
  return (response.data ?? response) as DataSourceCredentialsResponse
}

export async function deleteDataSourceCredentials(id: string): Promise<void> {
  await del(`/api/v1/datasource/${id}/credentials/credentials`)
}
