<script setup lang="ts">
import { ref, watch } from 'vue'
import { listDataSources, type DataSource } from '@/api/datasource'
import { generateSourceWikiModule, listSourceWikiAttempts, listSourceWikiCoverage, type SourceWikiAttempt, type SourceWikiCoverageTopic } from '@/api/wiki'
const props = defineProps<{ kbId: string; canEdit?: boolean }>()
const emit = defineEmits<{ (e: 'ready', slug: string): void }>()
const sources = ref<DataSource[]>([])
const attempts = ref<SourceWikiAttempt[]>([])
const coverage = ref<SourceWikiCoverageTopic[]>([])
const sourceID = ref('')
const modulePath = ref('')
const title = ref('')
const busy = ref(false)
const error = ref('')
let generation = 0
let requestGeneration = 0
let coverageGeneration = 0
async function load() {
  const current = ++generation
  try {
    const [sourceRes, attemptRes]: any[] = await Promise.all([listDataSources(props.kbId), listSourceWikiAttempts(props.kbId)])
    if (current !== generation) return
    sources.value = (sourceRes.data || []).filter((source: DataSource) => source.config?.settings?.content_mode === 'source')
    attempts.value = attemptRes.data || []
    if (!sources.value.some(s => s.id === sourceID.value)) sourceID.value = sources.value[0]?.id || ''
  } catch { if (current === generation) error.value = '暂时无法读取技术卡片状态。' }
}
async function loadCoverage() {
  const current = ++coverageGeneration
  const kbID = props.kbId
  const activeSourceID = sourceID.value
  if (!activeSourceID) { coverage.value = []; return }
  coverage.value = []
  try {
    const response: any = await listSourceWikiCoverage(kbID, activeSourceID)
    if (current !== coverageGeneration || kbID !== props.kbId || activeSourceID !== sourceID.value) return
    coverage.value = response.data || []
  } catch {
    if (current === coverageGeneration && kbID === props.kbId && activeSourceID === sourceID.value) {
      coverage.value = []
      error.value = '暂时无法读取源码主题覆盖清单。'
    }
  }
}
function statusText(status: SourceWikiCoverageTopic['status']) {
  return ({ planned: '计划中', ready: '已就绪', draft: '草稿已保留', failed: '失败', insufficient_evidence: '证据不足', expansion: '待扩展' })[status]
}
async function generate(attempt?: SourceWikiAttempt) {
  if (busy.value || !props.canEdit) return
  const kbID = props.kbId
  const current = ++requestGeneration
  busy.value = true
  error.value = ''
  try {
    const request = attempt ? { source_id: attempt.source_id, module_path: attempt.module_path, title: attempt.title }
      : { source_id: sourceID.value, module_path: modulePath.value, title: title.value }
    const response: any = await generateSourceWikiModule(kbID, request)
    if (current !== requestGeneration || kbID !== props.kbId) return
    if (response.data.status === 'ready') emit('ready', response.data.slug)
    else error.value = response.data.reason || '生成失败，草稿已保留。'
    await load()
  } catch {
    if (current !== requestGeneration || kbID !== props.kbId) return
    error.value = '生成未完成，请刷新查看原因后手动重试。'; await load()
  } finally { if (current === requestGeneration && kbID === props.kbId) busy.value = false }
}
watch(() => props.kbId, () => {
  ++requestGeneration
  ++coverageGeneration
  sources.value = []; attempts.value = []; coverage.value = []; sourceID.value = ''; modulePath.value = ''; title.value = ''; error.value = ''; busy.value = false
  void load()
}, { immediate: true })
watch(sourceID, () => { void loadCoverage() })
</script>

<template>
  <details v-if="sources.length || attempts.length" class="source-wiki-modules">
    <summary>源码技术卡片</summary>
    <form v-if="canEdit" @submit.prevent="generate()">
      <label>仓库<select v-model="sourceID" aria-label="技术卡片仓库"><option v-for="source in sources" :key="source.id" :value="source.id">{{ source.name }}</option></select></label>
      <label>模块目录<input v-model="modulePath" required aria-label="模块目录" placeholder="例如 src/scheduling" /></label>
      <label>主题<input v-model="title" required aria-label="技术卡片主题" placeholder="例如 排班模块职责" /></label>
      <button :disabled="busy || !sourceID || !modulePath || !title" type="submit">{{ busy ? '正在核验证据…' : '生成模块卡片' }}</button>
    </form>
    <p v-if="error" role="alert">{{ error }}</p>
    <section v-if="sourceID" class="coverage" aria-label="源码主题覆盖">
      <h3>主题覆盖清单</h3>
      <p v-if="!coverage.length">此仓库尚无主题骨架记录。</p>
      <ul v-else>
        <li v-for="topic in coverage" :key="topic.topic_key">
          <strong>{{ topic.title }}</strong>
          <span>{{ statusText(topic.status) }}</span>
          <small>{{ topic.kind === 'system' ? '系统概览' : topic.kind === 'module' ? (topic.module_path || '模块') : '业务流程' }} · {{ topic.topic_key }}</small>
          <p v-if="topic.uncertain">关系不确定：{{ topic.uncertainty_reasons.join('；') }}</p>
          <p v-if="topic.reason">{{ topic.reason }}</p>
          <button v-if="topic.status === 'ready' && topic.wiki_slug" type="button" @click="emit('ready', topic.wiki_slug)">阅读卡片</button>
        </li>
      </ul>
    </section>
    <ul>
      <li v-for="attempt in attempts" :key="attempt.id">
        <strong>{{ attempt.title }}</strong> · {{ attempt.module_path }}
        <span>{{ attempt.status === 'ready' ? '已就绪' : attempt.status === 'running' ? '生成中' : '失败，草稿已保留' }}</span>
        <p v-if="attempt.reason">{{ attempt.reason }}</p>
        <small>调用 {{ attempt.calls }} · 预算计入 {{ attempt.tokens }} tokens · 修复 {{ attempt.repairs }}/2</small>
        <button v-if="canEdit && attempt.status === 'failed'" type="button" :disabled="busy" @click="generate(attempt)">手动重试</button>
        <button v-if="attempt.status === 'ready'" type="button" @click="emit('ready', attempt.slug)">阅读卡片</button>
      </li>
    </ul>
  </details>
</template>
<style scoped>
.source-wiki-modules { margin: 8px 12px; font-size: 12px; }
form, label, li { display: flex; flex-direction: column; gap: 6px; }
form { margin: 10px 0; }
ul { padding: 0; list-style: none; max-height: 240px; overflow: auto; }
li { padding: 8px 0; border-bottom: 1px solid var(--td-component-border); }
input, select, button { padding: 5px; border: 1px solid var(--td-component-border); border-radius: 4px; color: inherit; background: var(--td-bg-color-container); }
p { margin: 0; overflow-wrap: anywhere; }
</style>
