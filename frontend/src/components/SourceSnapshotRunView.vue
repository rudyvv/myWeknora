<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SourceRunResult } from '@/api/datasource'
import SourceCodeView from './SourceCodeView.vue'

const props = defineProps<{ result: SourceRunResult; phase?: string }>()
const { t, locale } = useI18n()
const filter = ref('')
const page = ref(0)
const fileID = ref('')
const versionID = ref('')
const pageSize = 50
const unknown = computed(() => t('datasource.sourceRun.unknown'))
const telemetry = computed(() => props.result.telemetry?.schema_version === 1 ? props.result.telemetry : undefined)

const detectedSHA = computed(() => props.result.snapshot.detected_commit_sha ||
  (props.result.snapshot.state === 'published' ? props.result.snapshot.commit_sha : ''))
const targetSHA = computed(() => props.result.snapshot.target_commit_sha ||
  (props.result.snapshot.state === 'failed' ? '' : props.result.snapshot.commit_sha))
const publishedSHA = computed(() => telemetry.value?.published_commit_sha?.trim() ||
  (props.result.snapshot.state === 'published' ? props.result.snapshot.commit_sha : '') ||
  props.result.snapshot.previous_commit_sha || '')
const publicationChecked = computed(() => props.result.snapshot.state === 'published' || props.result.snapshot.publication_checked === true)
const hasPublishedVersion = computed(() => props.result.snapshot.state === 'published' || !!publishedSHA.value || Boolean(
  props.result.snapshot.previous_snapshot_id || props.result.snapshot.previous_published_at ||
  props.result.snapshot.last_successful_published_at
))
const publishedLabel = computed(() => publishedSHA.value || (hasPublishedVersion.value
  ? t('datasource.sourceRun.publishedShaUnknown')
  : publicationChecked.value ? t('datasource.sourceRun.notPublished') : t('datasource.sourceRun.publicationUnknown')))
const lastSuccessfulAt = computed(() => props.result.snapshot.last_successful_published_at ||
  (props.result.snapshot.state === 'published' ? props.result.snapshot.published_at : '') || props.result.snapshot.previous_published_at)
const lastSuccessfulLabel = computed(() => lastSuccessfulAt.value || (hasPublishedVersion.value
  ? t('datasource.sourceRun.lastSuccessUnknown')
  : publicationChecked.value ? t('datasource.sourceRun.noSuccessfulPublication') : t('datasource.sourceRun.publicationUnknown')))

const snapshotStates = new Set(['fetching', 'parsing', 'indexing', 'ready', 'published', 'failed'])
const statusLabel = computed(() => {
  if (props.result.snapshot.state === 'failed') {
    if (hasPublishedVersion.value) return t('datasource.sourceRun.failedRetained')
    if (!publicationChecked.value) return t('datasource.sourceRun.failedPublicationUnknown')
    return t('datasource.sourceRun.failedFirstPublication')
  }
  return snapshotStates.has(props.result.snapshot.state)
    ? t(`datasource.sourceRun.states.${props.result.snapshot.state}`)
    : t('datasource.sourceRun.stateUnknown', { state: props.result.snapshot.state || unknown.value })
})
const phaseStates = new Set(['queued', 'waiting_for_catch_up', 'retry_wait', 'running', 'target_resolved', 'fetching', 'parsing', 'indexing', 'ready', 'published', 'failed', 'canceled', 'superseded'])
const phaseLabel = computed(() => {
  if (!props.phase) return unknown.value
  return phaseStates.has(props.phase)
    ? t(`datasource.sourceRun.phases.${props.phase}`)
    : t('datasource.sourceRun.phaseUnknown', { phase: props.phase })
})

const filtered = computed(() => (props.result.members || []).filter(member => member.path.toLowerCase().includes(filter.value.toLowerCase())))
const members = computed(() => filtered.value.slice(page.value * pageSize, (page.value + 1) * pageSize))

function formatCount(value?: number | null) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return unknown.value
  return new Intl.NumberFormat(locale.value, { maximumFractionDigits: 20 }).format(value)
}

function formatBytes(value?: number | null) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return unknown.value
  if (value < 1024) return `${formatCount(value)} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let size = value
  let unit = -1
  do { size /= 1024; unit++ } while (size >= 1024 && unit < units.length - 1)
  return `${new Intl.NumberFormat(locale.value, { maximumFractionDigits: 1 }).format(size)} ${units[unit]}`
}

function formatDuration(value?: number | null) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return unknown.value
  return t('datasource.sourceRun.milliseconds', { value: formatCount(value) })
}

const durationKeys = ['fetching', 'parsing', 'indexing', 'publishing'] as const
const qualityKeys = ['structural', 'partial', 'syntax_error', 'degraded', 'unknown_preprocess', 'text_fallback'] as const
const storageKeys = ['cache', 'staging', 'original', 'vectors'] as const
const coverageKeys = ['eligible', 'ready', 'stale', 'failed', 'ungenerated', 'deferred'] as const
const modelUsageKeys = ['embedding_calls', 'generation_calls', 'input_tokens', 'output_tokens', 'estimated_input_tokens'] as const

const phaseDurations = computed(() => durationKeys.map(key => ({
  label: t(`datasource.sourceRun.phases.${key}`),
  value: formatDuration(telemetry.value?.phase_duration_ms?.[key]),
})))
const qualityCounts = computed(() => qualityKeys.map(key => ({
  label: t(`datasource.sourceRun.quality.${key}`),
  value: formatCount(telemetry.value?.quality_counts?.[key]),
})))
const storageRows = computed(() => storageKeys.map(key => {
  const usage = telemetry.value?.storage?.[key]
  const measurement = usage && (usage.measurement === 'logical_payload' || usage.measurement === 'physical')
    ? t(`datasource.sourceRun.measurements.${usage.measurement}`)
    : ''
  const limit = usage?.limit_bytes === undefined
    ? t('datasource.sourceRun.limitUnknown')
    : t('datasource.sourceRun.storageLimit', { value: formatBytes(usage.limit_bytes) })
  const value = usage && measurement && typeof usage.used_bytes === 'number' && Number.isFinite(usage.used_bytes) && usage.used_bytes >= 0
    ? t('datasource.sourceRun.storageValue', { used: formatBytes(usage.used_bytes), limit, measurement })
    : unknown.value
  return { label: t(`datasource.sourceRun.storage.${key}`), value }
}))
const modelUsage = computed(() => modelUsageKeys.map(key => ({
  label: t(`datasource.sourceRun.modelUsage.${key}`),
  value: formatCount(telemetry.value?.model_usage?.[key]),
})))
const wikiCoverage = computed(() => coverageKeys.map(key => ({
  label: t(`datasource.sourceRun.coverage.${key}`),
  value: formatCount(telemetry.value?.wiki_coverage?.[key]),
})))

function openMember(member: SourceRunResult['members'][number]) {
  fileID.value = member.source_file_id
  versionID.value = member.file_version_id
}
</script>

<template>
  <section class="source-run" :aria-label="t('datasource.sourceRun.ariaLabel')">
    <strong role="status">{{ statusLabel }}</strong>
    <p class="commit">{{ t('datasource.sourceRun.detectedHead') }}: {{ detectedSHA || t('datasource.sourceRun.headUnavailable') }}</p>
    <p class="commit">{{ t('datasource.sourceRun.targetCommit') }}: {{ targetSHA || t('datasource.sourceRun.targetUnavailable') }}</p>
    <p class="commit">{{ t('datasource.sourceRun.publishedCommit') }}: {{ publishedLabel }}</p>
    <p class="commit">{{ t('datasource.sourceRun.phase') }}: {{ phaseLabel }}</p>
    <p class="commit">{{ t('datasource.sourceRun.lastSuccess') }}: {{ lastSuccessfulLabel }}</p>
    <p>{{ result.snapshot.manifest_complete === true ? t('datasource.sourceRun.manifestComplete') : result.snapshot.manifest_complete === false ? t('datasource.sourceRun.manifestIncomplete') : t('datasource.sourceRun.manifestUnknown') }} · {{ t('datasource.sourceRun.members') }}: {{ formatCount(result.snapshot.member_count) }}</p>
    <p>{{ t('datasource.sourceRun.files') }}: {{ formatCount(result.snapshot.file_count) }} · {{ t('datasource.sourceRun.chunks') }}: {{ formatCount(result.snapshot.chunk_count) }}</p>
    <p>{{ t('datasource.sourceRun.added') }}: {{ formatCount(result.snapshot.added_count) }} · {{ t('datasource.sourceRun.changed') }}: {{ formatCount(result.snapshot.changed_count) }} · {{ t('datasource.sourceRun.deleted') }}: {{ formatCount(result.snapshot.deleted_count) }} · {{ t('datasource.sourceRun.renamed') }}: {{ formatCount(result.snapshot.renamed_count) }}</p>
    <p>{{ t('datasource.sourceRun.parsed') }}: {{ formatCount(result.snapshot.parsed_count) }} · {{ t('datasource.sourceRun.reusedFiles') }}: {{ formatCount(result.snapshot.reused_file_count) }} · {{ t('datasource.sourceRun.reusedChunks') }}: {{ formatCount(result.snapshot.reused_chunk_count) }}</p>
    <p>{{ t('datasource.sourceRun.embeddedChunks') }}: {{ formatCount(result.snapshot.embedded_chunk_count) }} · {{ t('datasource.sourceRun.reusedVectors') }}: {{ formatCount(result.snapshot.reused_vector_count) }}</p>
    <p v-if="result.snapshot.error" role="alert">{{ result.snapshot.error }}</p>

    <section class="telemetry" :aria-label="t('datasource.sourceRun.telemetryTitle')">
      <h4>{{ t('datasource.sourceRun.telemetryTitle') }}</h4>
      <p>{{ t('datasource.sourceRun.selectedBytes') }}: {{ formatBytes(telemetry?.selected_bytes) }}</p>
      <div class="telemetry-grid">
        <div>
          <strong>{{ t('datasource.sourceRun.phaseDurations') }}</strong>
          <ul><li v-for="row in phaseDurations" :key="row.label">{{ row.label }}: {{ row.value }}</li></ul>
        </div>
        <div>
          <strong>{{ t('datasource.sourceRun.qualityTitle') }}</strong>
          <ul><li v-for="row in qualityCounts" :key="row.label">{{ row.label }}: {{ row.value }}</li></ul>
        </div>
        <div>
          <strong>{{ t('datasource.sourceRun.storageTitle') }}</strong>
          <ul><li v-for="row in storageRows" :key="row.label">{{ row.label }}: {{ row.value }}</li></ul>
        </div>
        <div>
          <strong>{{ t('datasource.sourceRun.modelUsageTitle') }}</strong>
          <ul><li v-for="row in modelUsage" :key="row.label">{{ row.label }}: {{ row.value }}</li></ul>
        </div>
        <div>
          <strong>{{ t('datasource.sourceRun.coverageTitle') }}</strong>
          <ul><li v-for="row in wikiCoverage" :key="row.label">{{ row.label }}: {{ row.value }}</li></ul>
        </div>
      </div>
      <p>{{ t('datasource.sourceRun.leaseRecoveries') }}: {{ formatCount(telemetry?.lease_recoveries) }} · {{ t('datasource.sourceRun.cleanupResidue') }}: {{ formatCount(telemetry?.cleanup_residue_count) }}</p>
    </section>

    <input v-model="filter" :aria-label="t('datasource.sourceRun.filterMembers')" :placeholder="t('datasource.sourceRun.filterPlaceholder')" @input="page = 0" />
    <ul>
      <li v-for="member in members" :key="member.path">
        <button v-if="result.snapshot.state === 'published' && member.status === 'parsed'" type="button" @click="openMember(member)">{{ member.path }}</button>
        <span v-else>{{ member.path }}</span>
        <span> · {{ member.status }}{{ member.reason ? ` · ${member.reason}` : '' }}</span>
        <small v-if="member.change">{{ member.change }}{{ member.previous_path ? ` ← ${member.previous_path}` : '' }}{{ member.parse_reused ? ` · ${t('datasource.sourceRun.parseReused')}` : '' }}</small>
        <small v-if="member.file_version_id">{{ t('datasource.sourceRun.version') }} {{ member.file_version_id }}</small>
      </li>
    </ul>
    <nav v-if="filtered.length > pageSize" :aria-label="t('datasource.sourceRun.pagination')">
      <button type="button" :disabled="page === 0" @click="page--">{{ t('datasource.sourceRun.previousPage') }}</button>
      <span>{{ page + 1 }} / {{ Math.ceil(filtered.length / pageSize) }}</span>
      <button type="button" :disabled="(page + 1) * pageSize >= filtered.length" @click="page++">{{ t('datasource.sourceRun.nextPage') }}</button>
    </nav>
    <SourceCodeView v-if="fileID && result.snapshot.state === 'published'" :knowledge-id="fileID" :file-version-id="versionID" />
  </section>
</template>

<style scoped>
.source-run { margin: 12px 0; padding: 12px; border: 1px solid var(--td-component-border); border-radius: 6px; overflow-wrap: anywhere; }
p { margin: 6px 0; }
.commit, small { font-family: monospace; }
ul { padding-left: 16px; max-height: 260px; overflow: auto; }
li { margin: 6px 0; }
small { display: block; color: var(--td-text-color-placeholder); }
button, input { padding: 4px 8px; border: 1px solid var(--td-component-border); border-radius: 4px; color: inherit; background: var(--td-bg-color-container); }
button { cursor: pointer; }
nav { display: flex; justify-content: space-between; margin: 8px 0; }
.telemetry { margin: 12px 0; padding-top: 8px; border-top: 1px solid var(--td-component-border); }
.telemetry h4 { margin: 4px 0 8px; }
.telemetry-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 8px 16px; }
.telemetry-grid ul { max-height: none; margin: 4px 0; }
</style>
