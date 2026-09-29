<script setup lang="ts">
import type { SourceRegion } from '@/api/knowledge-base'

const props = defineProps<{ region: SourceRegion }>()
const qualityText: Record<SourceRegion['quality'], string> = {
  structural: '结构解析',
  syntax_error: '存在语法错误',
  degraded: '部分内容未解析',
  text_fallback: '保留原文',
  unknown_preprocess: '预处理器未解析',
}
</script>

<template>
  <span class="source-region-badge">
    {{ props.region.kind }}<template v-if="props.region.language"> · {{ props.region.language }}</template>
    · {{ qualityText[props.region.quality] || '质量未标明' }}
    <template v-if="props.region.external_source">
      · src={{ props.region.external_source }} ({{ props.region.external_status || 'unchecked' }}<template v-if="props.region.resolved_path">: {{ props.region.resolved_path }}</template>)
    </template>
  </span>
</template>

<style scoped>
.source-region-badge { display: inline-block; margin-left: 4px; opacity: .75; }
</style>
