<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { readSourceWikiEvidence } from '@/api/wiki'
import { getSourceFile, type SourceCodeRelation, type SourceFact, type SourceFileView, type SourceRange } from '@/api/knowledge-base'

const props = defineProps<{ knowledgeId: string; fileVersionId?: string; wikiEvidence?: { kbId: string; slug: string; id: string; version: number; commitSHA: string }; evidenceRange?: SourceRange }>()
const file = ref<SourceFileView | null>(null)
const loading = ref(false)
const relationsLoading = ref(false)
const relationsFailed = ref(false)
const failed = ref(false)
const page = ref(0)
const symbolQuery = ref('')
const selected = ref<SourceRange | null>(null)
let requestGeneration = 0
const pageSize = 200
const lines = computed(() => file.value?.content.split('\n') || [])
const sourceQualityLabels: Record<string, string> = {
  structural: '结构解析',
  text_fallback: '文本回退（未识别为结构化源码）',
  partial: '部分解析（部分结构不可用）',
  syntax_error: '语法错误（保留原文与可用证据）',
}
const qualityLabel = computed(() => sourceQualityLabels[file.value?.quality || ''] || `未知解析质量（${file.value?.quality || '未提供'}）`)
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
  return fact.qualified_name || fact.method_name || fact.name || fact.statement_id || fact.target_name || fact.kind
}
function relationLabel(relation: SourceCodeRelation) {
  return relation.to_key ? `${relation.from_key} → ${relation.to_key}` : relation.from_key
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
watch([() => props.knowledgeId, () => props.fileVersionId, () => props.wikiEvidence], async ([id, versionID]) => {
  const generation = ++requestGeneration
  file.value = null
  relationsLoading.value = false
  relationsFailed.value = false
  selected.value = null
  page.value = 0
  failed.value = false
  if (!id) return
  loading.value = true
  try {
    const response: any = props.wikiEvidence ? await readSourceWikiEvidence(props.wikiEvidence.kbId, props.wikiEvidence.slug, props.wikiEvidence.id, props.wikiEvidence.version) : await getSourceFile(id as string, versionID as string | undefined)
    if (generation === requestGeneration) {
      if (versionID && response.data.file_version_id !== versionID) failed.value = true
      else if (props.wikiEvidence && response.data.commit_sha !== props.wikiEvidence.commitSHA) failed.value = true
      else { file.value = response.data; if (props.evidenceRange) selectSymbol(props.evidenceRange) }
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
            <span v-if="fact.dynamic"> · 动态 SQL</span>
            <span v-if="fact.certainty"> · {{ fact.certainty === 'certain' ? '确定' : '不确定' }}</span>
            <span v-if="fact.result_map_refs?.length"> · resultMap: {{ fact.result_map_refs.join(', ') }}</span>
            <span v-if="fact.include_refs?.length"> · include: {{ fact.include_refs.join(', ') }}</span>
          </li>
        </ul>
        <p v-if="file.relations?.length || file.relations_truncated" class="source-analysis-heading">源码关系（{{ file.relations?.length || 0 }}）</p>
        <ul v-if="file.relations?.length" class="source-relations">
          <li v-for="relation in file.relations" :key="relation.id">
            <code>{{ relation.kind }}</code> · {{ relationLabel(relation) }} · {{ relation.determinacy }} / {{ relation.quality }}
            <span v-if="relation.resolution_reason"> · {{ relation.resolution_reason }}</span>
            <button v-if="relation.to_file_id === file.knowledge_id && relation.to_range && relation.to_range.end_byte > relation.to_range.start_byte" type="button" @click="selectSymbol(relation.to_range)">
              跳到目标范围 L{{ relation.to_range.start_line }}–{{ relation.to_range.end_line }}
            </button>
            <span v-else-if="relation.to_file_id"> · {{ relation.to_path }}（关系证据按当前读取范围提供）</span>
          </li>
        </ul>
        <button v-if="file.relations_truncated" type="button" :disabled="relationsLoading" @click="loadMoreRelations">
          {{ relationsLoading ? '正在读取关系…' : '读取更多关系' }}
        </button>
        <p v-if="relationsFailed" role="alert">后续关系读取失败；当前文件证据仍保留，可重试。</p>
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
