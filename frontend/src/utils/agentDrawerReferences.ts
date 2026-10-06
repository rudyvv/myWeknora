import type { KnowledgeReferenceLike, SourceEvidence } from './referenceSources'

export function getAgentDrawerReferences(
  refsOverride: KnowledgeReferenceLike[] | null | undefined,
  aggregatedReferences: KnowledgeReferenceLike[] | null | undefined,
  events: unknown[] | null | undefined,
  mapToolReferenceItems: (event: any) => KnowledgeReferenceLike[],
): KnowledgeReferenceLike[] {
  const messageReferences = refsOverride?.length ? refsOverride : aggregatedReferences
  if (messageReferences?.length) return messageReferences
  return (events || []).flatMap((event) => mapToolReferenceItems(event))
}

export function getKnowledgeSearchToolReferences(event: any): KnowledgeReferenceLike[] {
  if (event?.tool_name !== 'knowledge_search' && event?.tool_name !== 'search_knowledge') return []
  const toolData = event.tool_data as Record<string, any> | undefined
  if (!toolData) return []
  const results = Array.isArray(toolData.results) ? toolData.results : []
  const knowledgeBaseIds = Array.isArray(toolData.knowledge_base_ids) ? toolData.knowledge_base_ids : []
  const fallbackKnowledgeBaseId = typeof toolData.knowledge_base_id === 'string' && toolData.knowledge_base_id
    ? toolData.knowledge_base_id
    : knowledgeBaseIds.length === 1 && typeof knowledgeBaseIds[0] === 'string' && knowledgeBaseIds[0]
      ? knowledgeBaseIds[0]
      : undefined

  return results
    .filter((item: any) => item?.chunk_id || item?.knowledge_id)
    .map((item: any, index: number) => {
      const sourceEvidence = item.source_evidence
      const hasSourceEvidence = sourceEvidence && typeof sourceEvidence === 'object'
      return {
        id: item.chunk_id || `${item.knowledge_id}-${item.result_index ?? index + 1}`,
        knowledge_id: item.knowledge_id,
        knowledge_title: item.faq_standard_question || item.knowledge_title,
        knowledge_base_id: item.knowledge_base_id || fallbackKnowledgeBaseId,
        chunk_index: item.result_index ?? index + 1,
        chunk_type: item.chunk_type,
        content: item.content || '',
        ...(hasSourceEvidence ? { source_evidence: sourceEvidence as SourceEvidence | SourceEvidence[] } : {}),
      }
    })
}
