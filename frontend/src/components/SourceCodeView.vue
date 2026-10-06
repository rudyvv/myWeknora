<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { readSourceWikiEvidence } from '@/api/wiki'
import { getSourceFile, type SourceCodeRelation, type SourceFact, type SourceFileView, type SourceRange } from '@/api/knowledge-base'
import SourceRegionBadge from '@/components/SourceRegionBadge.vue'
import { sourceFactLabel, sourceQualityLabel, sourceRelationKindLabel } from '@/utils/sourceQuality'
import { sourceEvidenceMatchesFile, type SourceEvidence } from '@/utils/referenceSources'

const props = defineProps<{
  knowledgeId: string
  fileVersionId?: string
  wikiEvidence?: { kbId: string; slug: string; id: string; version: number; commitSHA: string }
  evidenceRange?: SourceRange
  expectedSourceEvidence?: Pick<SourceEvidence, 'data_source_id' | 'snapshot_id' | 'file_version_id' | 'project_id' | 'commit_sha' | 'path'>
}>()
const file = ref<SourceFileView | null>(null)
const loading = ref(false)
const relationsLoading = ref(false)
const relationsFailed = ref(false)
const targetLoading = ref(false)
const targetFailed = ref(false)
const failed = ref(false)
const page = ref(0)
const symbolQuery = ref('')
const selected = ref<SourceRange | null>(null)
const navigationStack = ref<Array<{ file: SourceFileView; selected: SourceRange | null }>>([])
let requestGeneration = 0
const pageSize = 200
const lines = computed(() => file.value?.content.split('\n') || [])
const qualityLabel = computed(() => sourceQualityLabel(file.value?.quality))
const visibleLines = computed(() => lines.value.slice(page.value * pageSize, (page.value + 1) * pageSize))
const symbols = computed(() => (file.value?.symbols || []).filter(s => s.qualified_name.toLowerCase().includes(symbolQuery.value.toLowerCase())).slice(0, 50))
const gitlabURL = computed(() => {
  if (!file.value || !/^[a-f0-9]{40,64}$/i.test(file.value.commit_sha)) return ''
  try {
    const base = new URL(file.value.repository_url)
    if (!['https:', 'http:'].includes(base.protocol) || base.username || base.password) return ''
    const range = selected.value
    return `${base.origin}${base.pathname.replace(/\/$/, '')}/-/blob/${file.value.commit_sha}/${file.value.path.split('/').map(encodeURIComponent).join('/')}${range ? `#L${range.start_line}-${range.end_line}` : ''}`
  } catch { return '' }
})
function selectSymbol(range: SourceRange) {
  selected.value = range
  page.value = Math.floor((range.start_line - 1) / pageSize)
}
function factLabel(fact: SourceFact) {
  return sourceFactLabel(fact)
}
function relationLabel(relation: SourceCodeRelation) {
  return relation.to_key ? `${relation.from_key} → ${relation.to_key}` : relation.from_key
}
function relationKindLabel(kind: string) {
  return sourceRelationKindLabel(kind)
}
function hasNavigableRange(range?: SourceRange) {
  return !!range && range.end_byte > range.start_byte && range.start_line > 0 && range.end_line >= range.start_line
}
function relationOppositeEndpoint(relation: SourceCodeRelation) {
  const current = file.value
  if (!current || relation.determinacy !== 'certain') return null
  const atFrom = relation.from_file_id === current.knowledge_id && relation.from_version_id === current.file_version_id
  const atTo = relation.to_file_id === current.knowledge_id && relation.to_version_id === current.file_version_id
  // Prefer To for same-file edges, where both ends share the current identity.
  const endpoint = atFrom
    ? { knowledgeId: relation.to_file_id, fileVersionId: relation.to_version_id, path: relation.to_path, range: relation.to_range }
    : atTo
      ? { knowledgeId: relation.from_file_id, fileVersionId: relation.from_version_id, path: relation.from_path, range: relation.from_range }
      : null
  return endpoint && endpoint.knowledgeId && endpoint.fileVersionId && endpoint.path && hasNavigableRange(endpoint.range) ? endpoint : null
}
function relationNavigationLabel(relation: SourceCodeRelation) {
  const endpoint = relationOppositeEndpoint(relation)
  if (!endpoint || !file.value) return ''
  const range = `L${endpoint.range.start_line}–${endpoint.range.end_line}`
  return endpoint.knowledgeId === file.value.knowledge_id && endpoint.fileVersionId === file.value.file_version_id
    ? `跳到目标范围 ${range}`
    : `打开 ${endpoint.path} 关联范围 ${range}`
}
async function openRelationTarget(relation: SourceCodeRelation) {
  const current = file.value
  const endpoint = relationOppositeEndpoint(relation)
  if (!current || !endpoint || targetLoading.value) return
  if (endpoint.knowledgeId === current.knowledge_id && endpoint.fileVersionId === current.file_version_id) {
    selectSymbol(endpoint.range)
    return
  }
  const generation = ++requestGeneration
  targetLoading.value = true
  targetFailed.value = false
  relationsLoading.value = false
  relationsFailed.value = false
  try {
    // Resolve only by the immutable file/version IDs carried by the relation;
    // the backend remains responsible for enforcing published tenant scope.
    const response = await getSourceFile(endpoint.knowledgeId, endpoint.fileVersionId)
    if (generation !== requestGeneration) return
    const target = response.data
    if (target.knowledge_id !== endpoint.knowledgeId || target.file_version_id !== endpoint.fileVersionId || target.path !== endpoint.path || target.snapshot_id !== current.snapshot_id) {
      targetFailed.value = true
      return
    }
    navigationStack.value.push({ file: current, selected: selected.value })
    file.value = target
    selectSymbol(endpoint.range)
  } catch {
    if (generation === requestGeneration) targetFailed.value = true
  } finally {
    if (generation === requestGeneration) targetLoading.value = false
  }
}
function returnToPreviousFile() {
  const previous = navigationStack.value.pop()
  if (!previous) return
  requestGeneration++
  targetLoading.value = false
  targetFailed.value = false
  relationsLoading.value = false
  relationsFailed.value = false
  file.value = previous.file
  selected.value = previous.selected
  page.value = previous.selected ? Math.floor((previous.selected.start_line - 1) / pageSize) : 0
}
async function loadMoreRelations() {
  const current = file.value
  if (!current?.relations_next_cursor || relationsLoading.value) return
  const generation = requestGeneration
  relationsLoading.value = true
  relationsFailed.value = false
  try {
    const response = await getSourceFile(current.knowledge_id, current.file_version_id, current.relations_next_cursor)
    if (generation === requestGeneration && file.value?.file_version_id === current.file_version_id) {
      const seen = new Set((current.relations || []).map(relation => relation.id))
      file.value = {
        ...current,
        relations: [...(current.relations || []), ...(response.data.relations || []).filter(relation => !seen.has(relation.id))],
        relations_truncated: response.data.relations_truncated,
        relations_next_cursor: response.data.relations_next_cursor,
      }
    }
  } catch {
    if (generation === requestGeneration) relationsFailed.value = true
  } finally {
    if (generation === requestGeneration) relationsLoading.value = false
  }
}
watch([() => props.knowledgeId, () => props.fileVersionId, () => props.wikiEvidence, () => props.expectedSourceEvidence], async ([id, versionID]) => {
  const generation = ++requestGeneration
  file.value = null
  relationsLoading.value = false
  relationsFailed.value = false
  targetLoading.value = false
  targetFailed.value = false
  navigationStack.value = []
  selected.value = null
  page.value = 0
  failed.value = false
  if (!id) return
  loading.value = true
  try {
    const response: any = props.wikiEvidence ? await readSourceWikiEvidence(props.wikiEvidence.kbId, props.wikiEvidence.slug, props.wikiEvidence.id, props.wikiEvidence.version) : await getSourceFile(id as string, versionID as string | undefined)
    if (generation === requestGeneration) {
      const sourceFile = response.data as SourceFileView & { data_source_id?: string }
      const expectedEvidence = props.expectedSourceEvidence
      const evidenceMismatch = expectedEvidence && !sourceEvidenceMatchesFile(expectedEvidence, sourceFile)
      if ((versionID && sourceFile.file_version_id !== versionID) || evidenceMismatch) failed.value = true
      else if (props.wikiEvidence && sourceFile.commit_sha !== props.wikiEvidence.commitSHA) failed.value = true
      else { file.value = sourceFile; if (props.evidenceRange) selectSymbol(props.evidenceRange) }
    }
  } catch { if (generation === requestGeneration) failed.value = true }
  finally { if (generation === requestGeneration) loading.value = false }
}, { immediate: true })
</script>

<template>
  <div class="source-code-view">
    <p v-if="loading" role="status">正在读取已发布源码…</p>
    <p v-else-if="failed" role="alert">源码不可读取，请确认同步已发布且仍有访问权限。</p>
    <template v-else-if="file">
      <button v-if="navigationStack.length" type="button" @click="returnToPreviousFile">返回来源文件</button>
      <div class="source-details">
        <strong>{{ file.path }}</strong>
        <span>仓库：{{ file.repository_url }} · 项目 {{ file.project_id }}</span>
        <span>提交：<code>{{ file.commit_sha }}</code></span>
        <span>解析：{{ qualityLabel }} · {{ file.encoding }}</span>
        <span>由 GitLab 同步管理，原文只读。</span>
        <a v-if="gitlabURL" :href="gitlabURL" target="_blank" rel="noopener noreferrer">在 GitLab 查看此版本</a>
      </div>
      <div v-if="file.symbols?.length" class="source-symbols">
        <input v-model="symbolQuery" aria-label="搜索源码符号" placeholder="搜索类或方法" />
        <button v-for="symbol in symbols" :key="`${symbol.qualified_name}:${symbol.range.start_byte}`" type="button" @click="selectSymbol(symbol.range)">
          {{ symbol.qualified_name }} · L{{ symbol.range.start_line }}–{{ symbol.range.end_line }}
          <SourceRegionBadge v-if="symbol.region" :region="symbol.region" />
        </button>
      </div>
      <section v-if="file.facts?.length || file.diagnostics?.length || file.relations?.length || file.relations_truncated" class="source-analysis" aria-label="源码结构分析">
        <h3>结构事实、诊断与关系</h3>
        <p v-if="file.diagnostics?.length" class="source-analysis-heading">解析诊断</p>
        <ul v-if="file.diagnostics?.length" class="source-diagnostics">
          <li v-for="(diagnostic, index) in file.diagnostics" :key="`${diagnostic.code}:${diagnostic.range?.start_byte ?? index}`">
            <code>{{ diagnostic.code }}</code> · {{ diagnostic.message }}
            <span v-if="diagnostic.range"> · L{{ diagnostic.range.start_line }}–{{ diagnostic.range.end_line }}</span>
          </li>
        </ul>
        <p v-if="file.facts?.length" class="source-analysis-heading">结构事实（{{ file.facts.length }}）</p>
        <ul v-if="file.facts?.length" class="source-facts">
          <li v-for="(fact, index) in file.facts" :key="`${fact.kind}:${fact.range?.start_byte ?? index}`">
            <code>{{ fact.kind }}</code> · {{ factLabel(fact) }}
            <span v-if="fact.range"> · L{{ fact.range.start_line }}–{{ fact.range.end_line }}</span>
            <span v-if="fact.dynamic"> · 动态/未解析</span>
            <span v-if="fact.certainty"> · {{ fact.certainty === 'certain' ? '确定' : '不确定' }}</span>
            <span v-if="fact.reason"> · {{ fact.reason }}</span>
            <span v-if="fact.super_types?.length"> · 父类型: {{ fact.super_types.join(', ') }}</span>
            <span v-if="fact.result_map_refs?.length"> · resultMap: {{ fact.result_map_refs.join(', ') }}</span>
            <span v-if="fact.include_refs?.length"> · include: {{ fact.include_refs.join(', ') }}</span>
          </li>
        </ul>
        <p v-if="file.relations?.length || file.relations_truncated" class="source-analysis-heading">源码关系（{{ file.relations?.length || 0 }}）</p>
        <ul v-if="file.relations?.length" class="source-relations">
          <li v-for="relation in file.relations" :key="relation.id">
            <code>{{ relationKindLabel(relation.kind) }}</code> · {{ relationLabel(relation) }} · {{ relation.determinacy }} / {{ relation.quality }}
            <span v-if="relation.resolution_reason"> · {{ relation.resolution_reason }}</span>
            <button v-if="relationOppositeEndpoint(relation)" type="button" :disabled="targetLoading" @click="openRelationTarget(relation)">
              {{ targetLoading ? '正在读取关联文件…' : relationNavigationLabel(relation) }}
            </button>
            <span v-else-if="relation.from_file_id || relation.to_file_id"> · 关系端点不确定或范围不可直接读取</span>
          </li>
        </ul>
        <button v-if="file.relations_truncated" type="button" :disabled="relationsLoading" @click="loadMoreRelations">
          {{ relationsLoading ? '正在读取关系…' : '读取更多关系' }}
        </button>
        <p v-if="relationsFailed" role="alert">后续关系读取失败；当前文件证据仍保留，可重试。</p>
        <p v-if="targetFailed" role="alert">目标文件不可读取；来源文件仍保留，且不会绕过访问限制。</p>
      </section>
      <nav aria-label="源码行分页">
        <button type="button" :disabled="page === 0" @click="page--">上一页</button>
        <span>L{{ page * pageSize + 1 }}–{{ Math.min((page + 1) * pageSize, lines.length) }} / {{ lines.length }} 行</span>
        <button type="button" :disabled="(page + 1) * pageSize >= lines.length" @click="page++">下一页</button>
      </nav>
      <div class="source-lines" aria-label="只读源码">
        <div v-for="(line, index) in visibleLines" :key="page * pageSize + index" :data-line="page * pageSize + index + 1" class="source-line"
          :class="{ selected: selected && page * pageSize + index + 1 >= selected.start_line && page * pageSize + index + 1 <= selected.end_line }">
          <span class="line-number">{{ page * pageSize + index + 1 }}</span><code>{{ line }}</code>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.source-details { display: flex; flex-direction: column; gap: 6px; overflow-wrap: anywhere; }
.source-symbols { display: flex; gap: 6px; flex-wrap: wrap; margin: 12px 0; max-height: 160px; overflow: auto; }
.source-analysis { margin: 16px 0; padding: 12px; border: 1px solid var(--td-component-border); border-radius: 6px; }
.source-analysis h3 { margin: 0 0 8px; font-size: 1rem; }
.source-analysis-heading { margin: 10px 0 4px; font-weight: 600; }
.source-facts, .source-diagnostics, .source-relations { margin: 0; padding-left: 20px; }
.source-facts li, .source-diagnostics li, .source-relations li { margin: 4px 0; overflow-wrap: anywhere; }
nav { display: flex; justify-content: space-between; align-items: center; margin: 12px 0; }
button, input { border: 1px solid var(--td-component-border); border-radius: 4px; padding: 4px 8px; color: inherit; background: var(--td-bg-color-container); }
button { cursor: pointer; }
button:disabled { opacity: .5; cursor: default; }
.source-lines { overflow-x: auto; border: 1px solid var(--td-component-border); border-radius: 6px; }
.source-line { display: flex; min-width: max-content; line-height: 1.6; }
.source-line code { white-space: pre; padding-right: 12px; tab-size: 4; }
.line-number { width: 64px; flex-shrink: 0; text-align: right; padding-right: 12px; color: var(--td-text-color-placeholder); user-select: none; }
.selected { background: var(--td-brand-color-light); }
</style>
