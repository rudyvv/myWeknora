import type { WikiPage } from '@/api/wiki'

// Resolve only evidence owned by the displayed page and its published version.
export function resolveWikiSourceEvidence(href: string, kbId: string, page: WikiPage | null, origin: string) {
  if (!page || !kbId || page.knowledge_base_id !== kbId || !Number.isInteger(page.version) || page.version < 1) return null
  try {
    const url = new URL(href, origin)
    if (url.origin !== origin || url.pathname !== `/api/v1/knowledgebase/${kbId}/wiki/source/evidence` ||
      url.searchParams.get('slug') !== page.slug || url.searchParams.get('version') !== String(page.version)) return null
    const id = url.searchParams.get('evidence_id')
    const evidence = page.source_provenance?.evidence.find(item => item.id === id)
    if (!evidence?.knowledge_id || !evidence.file_version_id || !/^[a-f0-9]{40}$/i.test(evidence.commit_sha)) return null
    return { evidence, owner: { kbId, slug: page.slug, id: evidence.id, version: page.version, commitSHA: evidence.commit_sha } }
  } catch { return null }
}
