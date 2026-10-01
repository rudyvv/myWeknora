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
  route_path?: string
  http_method?: string
}

export function sourceFactLabel(fact: SourceFactLabelFields): string {
  if (fact.route_path) return `${fact.http_method || 'HTTP'} ${fact.route_path}`
  return fact.qualified_name || fact.method_name || fact.name || fact.statement_id || fact.target_name || fact.kind || ''
}

const relationKindLabels: Record<string, string> = {
  http_route: 'HTTP 请求 → Spring 映射',
  method_call: 'Java 调用 → 声明目标',
  dependency_injection: 'Java 注入依赖',
  type_supertype: 'Java 类型 → 父类型',
  implements_method: '接口方法 → 实现方法',
  mapper_statement: 'Mapper 方法 → XML statement',
  include: 'SQL include',
  result_map: 'resultMap 引用',
  table_access: 'SQL 表访问',
}

export function sourceRelationKindLabel(kind?: string): string {
  return relationKindLabels[kind || ''] || kind || ''
}
