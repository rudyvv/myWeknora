<template>
  <!-- Fallback for ToolResultRenderer when used outside AgentStreamDisplay. -->
  <div class="knowledge-chunks-list">
    <div v-if="summaryHtml" class="results-summary-text" v-html="summaryHtml" />
    <div v-else class="empty-state">{{ $t('chat.noMatchFound') }}</div>
    <section v-if="analysis" class="source-analysis" aria-label="源码结构分析">
      <header>
        <strong>{{ analysis.path }}</strong>
        <span>{{ sourceQualityLabel(analysis.quality) }} · {{ analysis.parser_version }}</span>
        <code>SHA {{ analysis.sha256 }}</code>
      </header>
      <div v-if="analysis.diagnostics.length">
        <h4>解析诊断</h4>
        <ul><li v-for="(diagnostic, index) in analysis.diagnostics" :key="`${diagnostic.code}:${diagnostic.range?.start_line ?? index}`">
          <code>{{ diagnostic.code }}</code> · {{ diagnostic.message }}
          <span v-if="diagnostic.range"> · L{{ diagnostic.range.start_line }}–{{ diagnostic.range.end_line }}</span>
        </li></ul>
      </div>
      <div v-if="analysis.facts.length">
        <h4>结构事实（{{ analysis.facts.length }}{{ analysis.facts_truncated ? '+' : '' }}）</h4>
        <ul><li v-for="(fact, index) in analysis.facts" :key="`${fact.kind}:${fact.range?.start_byte ?? index}`">
          <code>{{ fact.kind }}</code> · {{ sourceFactLabel(fact) }}
          <span v-if="fact.range"> · L{{ fact.range.start_line }}–{{ fact.range.end_line }}</span>
          <span v-if="fact.dynamic"> · 动态 SQL</span>
          <span v-if="fact.certainty"> · {{ fact.certainty }}</span>
          <span v-if="fact.reason"> · {{ fact.reason }}</span>
        </li></ul>
      </div>
      <div v-if="analysis.relations.length || analysis.relations_truncated">
        <h4>关系（{{ analysis.relations.length }}{{ analysis.relations_truncated ? '+' : '' }}）</h4>
        <ul><li v-for="relation in analysis.relations" :key="relation.id">
          <code>{{ relation.kind }}</code> · {{ relation.from_key }} → {{ relation.to_key || relation.to_path }}
          · {{ relation.determinacy }} / {{ relation.quality }}
          <span v-if="relation.resolution_reason"> · {{ relation.resolution_reason }}</span>
          <div v-if="relation.target_evidence" class="target-evidence">
            <strong>{{ relation.target_evidence.path }} · L{{ relation.target_evidence.range.start_line }}–{{ relation.target_evidence.range.end_line }}</strong>
            <code>版本 {{ relation.target_evidence.file_version_id }} · SHA {{ relation.target_evidence.sha256 }}</code>
            <pre>{{ relation.target_evidence.snippet }}</pre>
            <span v-if="relation.target_evidence.snippet_truncated">（片段已截短；range 仍是原文完整范围）</span>
          </div>
        </li></ul>
        <p v-if="analysis.relations_truncated" class="continuation-note">已显示当前页；Agent 可用继续令牌读取后续关系。</p>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { useI18n } from 'vue-i18n';
import { getKnowledgeChunksSummaryHtml } from '@/utils/knowledgeChunksDisplay';
import { sourceFactLabel, sourceQualityLabel } from '@/utils/sourceQuality';
import type { KnowledgeChunksListData } from '@/types/tool-results';

const props = defineProps<{
  data: KnowledgeChunksListData;
}>();

const { t } = useI18n();

const summaryHtml = computed(() => getKnowledgeChunksSummaryHtml(t, props.data));
const analysis = computed(() => props.data.source_analysis);
</script>

<style lang="less" scoped>
.knowledge-chunks-list {
  .source-analysis { margin-top: 12px; padding: 10px; border: 1px solid var(--td-component-border); border-radius: 6px; overflow-wrap: anywhere; }
  .source-analysis header { display: flex; flex-direction: column; gap: 3px; }
  .source-analysis h4 { margin: 10px 0 4px; }
  .source-analysis ul { margin: 0; padding-left: 20px; }
  .source-analysis li { margin: 4px 0; }
  .target-evidence { display: flex; flex-direction: column; gap: 4px; margin: 6px 0; padding: 8px; background: var(--td-bg-color-container); border-radius: 4px; }
  .target-evidence pre { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
  .continuation-note { margin: 8px 0 0; color: var(--td-text-color-secondary); }
  .results-summary-text {
    font-size: var(--agent-step-summary-size, 12px);
    font-weight: 400;
    color: var(--td-text-color-secondary);
    line-height: 1.5;

    :deep(strong) {
      color: var(--td-text-color-secondary);
      font-weight: 500;
    }
  }

  .empty-state {
    font-size: 13px;
    color: var(--td-text-color-placeholder);
  }
}
</style>
