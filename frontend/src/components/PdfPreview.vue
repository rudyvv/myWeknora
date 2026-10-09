<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getDocument, GlobalWorkerOptions, type PDFDocumentLoadingTask } from 'pdfjs-dist'
import { EventBus, LinkTarget, PDFLinkService, PDFViewer } from 'pdfjs-dist/web/pdf_viewer.mjs'
import workerUrl from 'pdfjs-dist/build/pdf.worker.min.mjs?url'
import 'pdfjs-dist/web/pdf_viewer.css'

const props = defineProps<{ blob: Blob; fileName: string }>()
const emit = defineEmits<{ error: [error: Error]; ready: [container: HTMLElement] }>()
const { t } = useI18n()
const container = ref<HTMLDivElement>()
const loading = ref(true)
const page = ref(1)
const pageCount = ref(0)
let viewer: PDFViewer | undefined
let linkService: PDFLinkService | undefined
let task: PDFDocumentLoadingTask | undefined
let generation = 0
let observer: ResizeObserver | undefined

GlobalWorkerOptions.workerSrc = workerUrl
const assetBase = `${import.meta.env.BASE_URL}pdfjs/`

async function load() {
  const current = ++generation
  loading.value = true
  page.value = 1
  pageCount.value = 0
  viewer?.setDocument(null)
  linkService?.setDocument(null)
  const oldTask = task
  task = undefined
  await oldTask?.destroy()
  try {
    const data = new Uint8Array(await props.blob.arrayBuffer())
    if (current !== generation) return
    task = getDocument({
      data,
      cMapUrl: `${assetBase}cmaps/`,
      cMapPacked: true,
      standardFontDataUrl: `${assetBase}standard_fonts/`,
      wasmUrl: `${assetBase}wasm/`,
    })
    const pdf = await task.promise
    if (current !== generation) return
    pageCount.value = pdf.numPages
    viewer?.setDocument(pdf)
    linkService?.setDocument(pdf)
  } catch (error) {
    if (current === generation) emit('error', new Error(t('preview.loadFailed')))
  }
}

function goToPage(value: number) {
  if (viewer && value >= 1 && value <= pageCount.value) viewer.currentPageNumber = value
}

onMounted(() => {
  if (!container.value) return
  emit('ready', container.value)
  const eventBus = new EventBus()
  linkService = new PDFLinkService({ eventBus, externalLinkTarget: LinkTarget.BLANK, externalLinkRel: 'noopener noreferrer' })
  viewer = new PDFViewer({ container: container.value, eventBus, linkService })
  linkService.setViewer(viewer)
  eventBus.on('pagesinit', () => {
    if (viewer) viewer.currentScaleValue = 'page-width'
  })
  eventBus.on('pagerendered', (event: { error?: Error }) => {
    loading.value = false
    if (event.error) emit('error', new Error(t('preview.loadFailed')))
  })
  eventBus.on('pagechanging', (event: { pageNumber: number }) => { page.value = event.pageNumber })
  observer = new ResizeObserver(() => {
    if (viewer?.pdfDocument) viewer.currentScaleValue = 'page-width'
  })
  observer.observe(container.value)
  void load()
})

watch(() => props.blob, () => { if (viewer) void load() })
onBeforeUnmount(() => {
  generation++
  observer?.disconnect()
  viewer?.setDocument(null)
  linkService?.setDocument(null)
  void task?.destroy()
})
</script>

<template>
  <div class="pdf-preview">
    <nav class="pdf-preview__pages" :aria-label="fileName">
      <t-button size="small" variant="text" :disabled="page <= 1 || loading" @click="goToPage(page - 1)">
        {{ t('datasource.sourceRun.previousPage') }}
      </t-button>
      <span>{{ page }} / {{ pageCount || '…' }}</span>
      <t-button size="small" variant="text" :disabled="page >= pageCount || loading" @click="goToPage(page + 1)">
        {{ t('datasource.sourceRun.nextPage') }}
      </t-button>
    </nav>
    <div class="pdf-preview__body">
      <div v-if="loading" class="pdf-preview__loading"><t-loading /><span>{{ t('preview.loading') }}</span></div>
      <div ref="container" tabindex="0" :aria-label="fileName" class="pdf-preview__container">
        <div class="pdfViewer" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.pdf-preview { display: flex; flex-direction: column; height: 100%; min-height: 500px; }
.pdf-preview__pages { display: flex; justify-content: center; align-items: center; gap: 12px; padding: 4px; flex-shrink: 0; }
.pdf-preview__body { position: relative; flex: 1; min-height: 460px; }
.pdf-preview__container { position: absolute; inset: 0; overflow: auto; background: var(--td-bg-color-secondarycontainer); }
.pdf-preview__loading { position: absolute; inset: 0; z-index: 1; display: flex; justify-content: center; align-items: center; gap: 8px; background: var(--td-bg-color-container); }
</style>
