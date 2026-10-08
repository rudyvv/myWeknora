<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SourcePreview } from '@/api/datasource'

const props = defineProps<{ preview: SourcePreview }>()
const { t } = useI18n()
const search = ref('')
const page = ref(1)
const counts = computed(() => {
  const files = props.preview.files
  return {
    selected: files.filter(file => file.status === 'included').length,
    excluded: files.filter(file => file.status === 'excluded').length,
    unavailable: files.filter(file => file.status !== 'included' && file.status !== 'excluded').length,
  }
})
const filteredFiles = computed(() => props.preview.files.filter(file => file.path.toLowerCase().includes(search.value.toLowerCase())))
const pageFiles = computed(() => filteredFiles.value.slice((page.value - 1) * 50, page.value * 50))
watch(search, () => { page.value = 1 })

function issueMessage(check: SourcePreview['checks'][number]): string {
  if (check.name === 'indexes') {
    if (check.message.startsWith('source embedding model must configure a supported tokenizer') || check.message.startsWith('source embedding tokenizer ')) return t('datasource.gitlab.checkTokenizer')
    if (check.message.includes('max_input_tokens') || check.message.includes('input limit')) return t('datasource.gitlab.checkInputLimit')
    if (check.message.includes('source index budget')) return t('datasource.gitlab.checkFileBudget')
    return t('datasource.gitlab.checkIndexes')
  }
  if (check.name === 'parser') return t('datasource.gitlab.checkParser')
  if (check.name === 'gitlab_branch') return t('datasource.gitlab.checkBranch')
  return t('datasource.gitlab.checkPipeline')
}
const issues = computed(() => props.preview.checks.filter(check => !check.ready).map(issueMessage))
</script>

<template>
  <div class="source-check-result" :class="{ 'source-check-result--blocked': !preview.can_sync }" :role="preview.can_sync ? 'status' : 'alert'">
    <strong>{{ t(preview.can_sync ? 'datasource.gitlab.checkPassed' : 'datasource.gitlab.checkBlocked') }}</strong>
    <p>{{ t('datasource.gitlab.checkCounts', counts) }}</p>
    <ul v-if="issues.length"><li v-for="(issue, index) in issues" :key="index">{{ issue }}</li></ul>
    <p v-for="warning in preview.warnings" :key="warning">{{ warning === 'Wiki is disabled for this knowledge base' ? t('datasource.gitlab.checkWikiDisabled') : warning }}</p>
    <details class="source-check-details">
      <summary>{{ t('datasource.gitlab.checkDetails') }}</summary>
      <p>{{ t('datasource.gitlab.fixedCommit') }} <code>{{ preview.commit_sha }}</code></p>
      <p>{{ t('datasource.gitlab.rulesVersion') }} <code>{{ preview.rules_version }}</code></p>
      <ul><li v-for="check in preview.checks" :key="check.name">{{ check.ready ? '✓' : '⚠' }} {{ check.message }}</li></ul>
      <t-input v-model="search" :placeholder="t('datasource.gitlab.searchPaths')" clearable />
      <div class="source-preview__files">
        <div v-for="file in pageFiles" :key="file.path" class="source-preview__file">
          <code>{{ file.path }}</code><span>{{ file.size }} B · {{ file.status }}{{ file.generated ? ' · ' + t('datasource.gitlab.generated') : '' }}</span>
          <small>{{ file.reason }}</small>
        </div>
      </div>
      <t-pagination v-model="page" :total="filteredFiles.length" :page-size="50" :show-page-size="false" />
    </details>
  </div>
</template>

<style scoped>
.source-check-result { margin: 16px 0; padding: 16px; border-radius: 8px; background: var(--td-bg-color-secondarycontainer); }
.source-check-result strong { color: var(--td-success-color); }
.source-check-result--blocked strong { color: var(--td-error-color); }
.source-check-result p, .source-check-result ul { margin: 8px 0; }
.source-check-details { margin-top: 12px; }
.source-check-details summary { cursor: pointer; color: var(--td-text-color-secondary); }
.source-check-details code { overflow-wrap: anywhere; }
.source-preview__files { max-height: 280px; overflow: auto; }
.source-preview__file { display: grid; gap: 4px; padding: 12px 0; border-bottom: 1px solid var(--td-component-stroke); }
.source-preview__file span, .source-preview__file small { color: var(--td-text-color-secondary); }
</style>
