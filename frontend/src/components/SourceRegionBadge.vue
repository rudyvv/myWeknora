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
const externalStatusText: Record<NonNullable<SourceRegion['external_status']>, string> = {
  unchecked: '尚未核验',
  rejected: '未关联（目标路径不允许）',
  unavailable: '当前不可读取或未关联',
  resolved: '已关联',
}
const externalStatusLabel = (status: SourceRegion['external_status']) =>
  externalStatusText[status || 'unchecked'] || '状态未标明'
</script>

<template>
  <span class="source-region-badge">
    {{ props.region.kind }}<template v-if="props.region.language"> · {{ props.region.language }}</template>
    · {{ qualityText[props.region.quality] || '质量未标明' }}
    <template v-if="props.region.external_source">
      · src={{ props.region.external_source }} ({{ externalStatusLabel(props.region.external_status) }}<template v-if="props.region.resolved_path">: {{ props.region.resolved_path }}</template>)
    </template>
  </span>
</template>

<style scoped>
.source-region-badge { display: inline-block; margin-left: 4px; opacity: .75; }
</style>
