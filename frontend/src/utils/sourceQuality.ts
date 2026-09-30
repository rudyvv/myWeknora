const qualityLabels: Record<string, string> = {
  structural: '结构解析',
  text_fallback: '文本回退（未识别为结构化源码）',
  partial: '部分解析（部分结构不可用）',
  syntax_error: '语法错误（保留原文与可用证据）',
  degraded: '部分内容未解析',
  unknown_preprocess: '预处理器未解析',
}

export function sourceQualityLabel(quality?: string): string {
  return qualityLabels[quality || ''] || `未知解析质量（${quality || '未提供'}）`
}

export interface SourceFactLabelFields {
  kind?: string
  qualified_name?: string
  method_name?: string
  name?: string
  statement_id?: string
  target_name?: string
}

export function sourceFactLabel(fact: SourceFactLabelFields): string {
  return fact.qualified_name || fact.method_name || fact.name || fact.statement_id || fact.target_name || fact.kind || ''
}
