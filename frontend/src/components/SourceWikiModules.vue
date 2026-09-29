<script setup lang="ts">
import { ref, watch } from 'vue'
import { listDataSources, type DataSource } from '@/api/datasource'
import { generateSourceWikiModule, listSourceWikiAttempts, type SourceWikiAttempt } from '@/api/wiki'
const props = defineProps<{ kbId: string; canEdit?: boolean }>()
const emit = defineEmits<{ (e: 'ready', slug: string): void }>()
const sources = ref<DataSource[]>([])
const attempts = ref<SourceWikiAttempt[]>([])
const sourceID = ref('')
const modulePath = ref('')
const title = ref('')
const busy = ref(false)
const error = ref('')
let generation = 0
let requestGeneration = 0
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
  sources.value = []; attempts.value = []; sourceID.value = ''; modulePath.value = ''; title.value = ''; error.value = ''; busy.value = false
  void load()
}, { immediate: true })
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
