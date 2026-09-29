<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { readSourceWikiEvidence } from '@/api/wiki'
import { getSourceFile, type SourceFileView, type SourceRange } from '@/api/knowledge-base'

const props = defineProps<{ knowledgeId: string; fileVersionId?: string; wikiEvidence?: { kbId: string; slug: string; id: string; version: number; commitSHA: string }; evidenceRange?: SourceRange }>()
const file = ref<SourceFileView | null>(null)
const loading = ref(false)
const failed = ref(false)
const page = ref(0)
const symbolQuery = ref('')
const selected = ref<SourceRange | null>(null)
let requestGeneration = 0
const pageSize = 200
const lines = computed(() => file.value?.content.split('\n') || [])
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
watch([() => props.knowledgeId, () => props.fileVersionId, () => props.wikiEvidence], async ([id, versionID]) => {
  const generation = ++requestGeneration
  file.value = null
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
        <span>解析：{{ file.quality === 'structural' ? '结构解析' : '存在语法错误' }} · {{ file.encoding }}</span>
        <span>由 GitLab 同步管理，原文只读。</span>
        <a v-if="gitlabURL" :href="gitlabURL" target="_blank" rel="noopener noreferrer">在 GitLab 查看此版本</a>
      </div>
      <div v-if="file.symbols?.length" class="source-symbols">
        <input v-model="symbolQuery" aria-label="搜索源码符号" placeholder="搜索类或方法" />
        <button v-for="symbol in symbols" :key="`${symbol.qualified_name}:${symbol.range.start_byte}`" type="button" @click="selectSymbol(symbol.range)">
          {{ symbol.qualified_name }} · L{{ symbol.range.start_line }}–{{ symbol.range.end_line }}
        </button>
      </div>
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
