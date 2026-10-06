import assert from 'node:assert/strict'
import test from 'node:test'

import { countGrepDocuments, groupGrepChunkResults } from './grepResultsGroup.ts'

test('groupGrepChunkResults merges chunks from the same document', () => {
  const grouped = groupGrepChunkResults([
    {
      chunk_id: 'chunk-a',
      knowledge_id: 'doc-1',
      knowledge_base_id: 'kb-1',
      knowledge_title: 'sample-report.pdf',
      match_snippet: 'hit one',
    },
    {
      chunk_id: 'chunk-b',
      knowledge_id: 'doc-1',
      knowledge_base_id: 'kb-1',
      knowledge_title: 'sample-report.pdf',
      match_snippet: 'hit two',
    },
    {
      chunk_id: 'chunk-c',
      knowledge_id: 'doc-2',
      knowledge_base_id: 'kb-1',
      knowledge_title: 'other-report.pdf',
      match_snippet: 'other hit',
    },
  ])

  assert.equal(grouped.length, 2)
  assert.equal(grouped[0].chunk_hit_count, 2)
  assert.equal(grouped[0].chunks.length, 2)
  assert.equal(grouped[0].knowledge_base_id, 'kb-1')
  assert.deepEqual(grouped[0].chunks.map((chunk) => chunk.chunk_id), ['chunk-a', 'chunk-b'])
  assert.equal(grouped[1].chunk_hit_count, 1)
})

test('groupGrepChunkResults keeps FAQ entries separate', () => {
  const grouped = groupGrepChunkResults([
    {
      chunk_id: 'faq-1',
      faq_id: 'faq-1',
      knowledge_id: 'doc-faq',
      knowledge_base_id: 'kb-1',
      knowledge_title: 'FAQ doc',
      chunk_type: 'faq',
      faq_question: 'Question A',
      match_snippet: 'answer a',
    },
    {
      chunk_id: 'faq-2',
      faq_id: 'faq-2',
      knowledge_id: 'doc-faq',
      knowledge_base_id: 'kb-1',
      knowledge_title: 'FAQ doc',
      chunk_type: 'faq',
      faq_question: 'Question B',
      match_snippet: 'answer b',
    },
  ])

  assert.equal(grouped.length, 2)
  assert.equal(grouped[0].title, 'Question A')
  assert.equal(grouped[1].title, 'Question B')
})

test('groupGrepChunkResults keeps each chunk source evidence for the references drawer', () => {
  const sourceEvidence = {
    data_source_id: 'source-1',
    snapshot_id: 'snapshot-1',
    file_version_id: 'version-1',
    project_id: 'project-1',
    commit_sha: 'a'.repeat(40),
    path: 'src/service.ts',
    range: { start_byte: 0, end_byte: 12, start_line: 1, end_line: 1 },
    symbols: null,
    quality: 'structural',
    gitlab_url: '',
    context: null,
  }
  const [group] = groupGrepChunkResults([{
    chunk_id: 'chunk-a',
    knowledge_id: 'doc-1',
    knowledge_base_id: 'kb-1',
    knowledge_title: 'service.ts',
    match_snippet: 'run()',
    source_evidence: sourceEvidence,
  }])

  assert.deepEqual(group.chunks[0].source_evidence, sourceEvidence)
})

test('groupGrepChunkResults retains title-only source evidence hits with an empty snippet', () => {
  const sourceEvidence = {
    data_source_id: 'source-1', snapshot_id: 'snapshot-1', file_version_id: 'version-1',
    project_id: 'project-1', commit_sha: 'a'.repeat(40), path: 'src/service.ts',
    range: { start_byte: 0, end_byte: 8, start_line: 1, end_line: 1 },
    symbols: null, quality: 'structural', gitlab_url: '', context: null,
  }
  const [group] = groupGrepChunkResults([{
    chunk_id: 'chunk-title-hit',
    knowledge_id: 'file-1',
    knowledge_base_id: 'kb-1',
    knowledge_title: 'Service source',
    title_match: true,
    match_snippet: '',
    source_evidence: sourceEvidence,
  }])

  assert.equal(group.title_match, true)
  assert.equal(group.match_snippet, '')
  assert.equal(group.chunks.length, 1)
  assert.equal(group.chunks[0].content, '')
  assert.deepEqual(group.chunks[0].source_evidence, sourceEvidence)
})

test('countGrepDocuments prefers document_count from backend', () => {
  assert.equal(
    countGrepDocuments({
      document_count: 1,
      knowledge_results: [{ knowledge_id: 'doc-1' }, { knowledge_id: 'doc-2' }],
      chunk_results: [{ chunk_id: 'c1', knowledge_id: 'doc-1', knowledge_base_id: 'kb', knowledge_title: 'a' }],
    }),
    1,
  )
})

test('countGrepDocuments prefers knowledge_results length when document_count absent', () => {
  assert.equal(
    countGrepDocuments({
      knowledge_results: [{ knowledge_id: 'doc-1' }, { knowledge_id: 'doc-2' }],
      chunk_results: [{ chunk_id: 'c1', knowledge_id: 'doc-1', knowledge_base_id: 'kb', knowledge_title: 'a' }],
    }),
    2,
  )
})
