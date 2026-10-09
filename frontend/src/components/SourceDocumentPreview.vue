<script setup lang="ts">
import { onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getSourceFile } from '@/api/knowledge-base'
import DocumentPreview from '@/components/document-preview.vue'

const props = defineProps<{ knowledgeId: string; fileVersionId?: string }>()
const { t } = useI18n()
const blob = shallowRef<Blob>()
const fileName = ref('')
const loading = ref(false)
const failed = ref(false)
let generation = 0

async function load() {
  const current = ++generation
  const id = props.knowledgeId
  const version = props.fileVersionId
  blob.value = undefined
  fileName.value = ''
  failed.value = false
  loading.value = !!id
  if (!id) return
  try {
    const { data } = await getSourceFile(id, version)
    if (current !== generation) return
    if (data.knowledge_id !== id || (version && data.file_version_id !== version) || typeof data.content !== 'string') {
      failed.value = true
      return
    }
    // Source content is already decoded by its published parser; raw file
    // bytes may use another encoding, such as GBK.
    blob.value = new Blob([data.content], { type: 'text/plain;charset=utf-8' })
    fileName.value = data.path
  } catch {
    if (current === generation) failed.value = true
  } finally {
    if (current === generation) loading.value = false
  }
}

watch(() => [props.knowledgeId, props.fileVersionId], load, { immediate: true })
onBeforeUnmount(() => { generation++ })
</script>

<template>
  <div class="source-document-preview">
    <div v-if="loading" class="source-preview-status" role="status">
      <t-loading size="small" /><span>{{ t('preview.loading') }}</span>
    </div>
    <div v-else-if="failed" class="source-preview-status" role="alert">
      <span>{{ t('preview.loadFailed') }}</span>
      <t-button size="small" theme="primary" @click="load">{{ t('preview.retry') }}</t-button>
    </div>
    <DocumentPreview v-else-if="blob" :source-blob="blob" :file-name="fileName" file-type="" :active="true" />
  </div>
</template>

<style scoped>
.source-preview-status { display: flex; align-items: center; justify-content: center; gap: 12px; min-height: 160px; }
</style>
