import assert from 'node:assert/strict'
import test from 'node:test'
import {
  getAgentDrawerReferences,
  getKnowledgeSearchToolReferences,
} from './agentDrawerReferences.ts'
import { buildReferenceList, isNavigableSourceEvidence } from './referenceSources.ts'
import * as agentDrawerReferences from './agentDrawerReferences.ts'

test('aggregate document references retain matching source evidence from completed tools', () => {
  const evidence = {
    data_source_id: 'source-1', snapshot_id: 'snapshot-1', file_version_id: 'version-1',
    project_id: 'project-1', commit_sha: 'a'.repeat(40), path: 'src/service.ts',
    range: { start_byte: 0, end_byte: 8, start_line: 1, end_line: 1 },
    symbols: null, quality: 'structural', gitlab_url: '', context: null,
  }
  const aggregate = [{ id: 'chunk-1', knowledge_id: 'file-1', knowledge_base_id: 'kb-1', content: 'Existing snippet' }]
  const tools = [
    { knowledge_id: 'file-1', knowledge_base_id: 'kb-1', source_evidence: evidence },
    { knowledge_id: 'file-1', knowledge_base_id: 'other-kb', source_evidence: { ...evidence, snapshot_id: 'other-snapshot' } },
    { knowledge_id: 'other-file', knowledge_base_id: 'kb-1', source_evidence: { ...evidence, path: 'src/other.ts' } },
  ]
  const references = getAgentDrawerReferences(null, aggregate, [tools], event => event)
  assert.equal(references.length, 1)
  assert.deepEqual(references[0].source_evidence, [evidence])
  assert.equal(references[0].content, 'Existing snippet')
  assert.equal(aggregate[0].source_evidence, undefined, 'input references must not be mutated')
  assert.equal(isNavigableSourceEvidence(references[0].knowledge_id, buildReferenceList(references)[0].sourceEvidence[0]), true)
  const pinned = [{ ...aggregate[0], source_evidence: { ...evidence, snapshot_id: 'pinned-snapshot' } }]
  assert.deepEqual(getAgentDrawerReferences(pinned, aggregate, [tools], event => event), pinned)
})

test('metadata-only source references restore fixed-version navigation after a history reload', () => {
  const evidence = {
    data_source_id: 'source-1', snapshot_id: 'snapshot-1', file_version_id: 'version-1',
    project_id: 'project-1', commit_sha: 'a'.repeat(40), path: 'src/service.ts',
    range: { start_byte: 0, end_byte: 8, start_line: 1, end_line: 1 },
    symbols: null, quality: 'structural', gitlab_url: '', context: null,
  }
  const event = { tool_name: 'list_knowledge_chunks', tool_data: { source_references: [{ chunk_id: 'chunk-1', knowledge_id: 'file-1', knowledge_base_id: 'kb-1', source_evidence: evidence }] } }
  const aggregate = [{ knowledge_id: 'file-1', knowledge_base_id: 'kb-1' }]
  const refs = getAgentDrawerReferences(null, aggregate, [event], () => [])
  assert.deepEqual(buildReferenceList(refs)[0].sourceEvidence, [evidence])
  assert.equal(isNavigableSourceEvidence(refs[0].knowledge_id, buildReferenceList(refs)[0].sourceEvidence[0]), true)
})

test('empty aggregate references fall back to knowledge_search tool_result source evidence', () => {
  const sourceEvidence = {
    data_source_id: 'source-1',
    snapshot_id: 'snapshot-1',
    file_version_id: 'file-version-1',
    project_id: 'project-1',
    commit_sha: 'a'.repeat(40),
    path: 'src/service.ts',
    range: { start_byte: 12, end_byte: 39, start_line: 2, end_line: 3 },
    symbols: ['Service.run'],
    quality: 'structural',
    gitlab_url: 'https://gitlab.example/group/repo/-/blob/main/src/service.ts',
    context: [{ text: 'async run() {}', range: { start_byte: 12, end_byte: 39, start_line: 2, end_line: 3 } }],
    region: { kind: 'script', language: 'ts', quality: 'structural' },
    diagnostics: [{ code: 'source_note', range: { start_byte: 12, end_byte: 39, start_line: 2, end_line: 3 } }],
  }
  const toolResult = {
    type: 'tool_result',
    tool_name: 'knowledge_search',
    tool_call_id: 'call-1',
    tool_data: {
      results: [{
        chunk_id: 'chunk-1',
        knowledge_id: 'knowledge-file-1',
        knowledge_title: 'Service source',
        content: 'async run() {}',
        source_evidence: sourceEvidence,
      }],
    },
  }

  const references = getAgentDrawerReferences([], [], [toolResult], getKnowledgeSearchToolReferences)

  assert.equal(references.length, 1)
  assert.equal(references[0].knowledge_id, 'knowledge-file-1')
  assert.deepEqual(references[0].source_evidence, sourceEvidence)
  const [drawerItem] = buildReferenceList(references)
  assert.deepEqual(drawerItem.sourceEvidence, [sourceEvidence])
  assert.equal(isNavigableSourceEvidence(references[0].knowledge_id, sourceEvidence), true)

  const incompleteEvidence = { ...sourceEvidence, file_version_id: '' }
  assert.equal(isNavigableSourceEvidence(references[0].knowledge_id, incompleteEvidence), false)
})

test('title-only grep matches retain source evidence for the Agent citation drawer', () => {
  const sourceEvidence = {
    data_source_id: 'source-1', snapshot_id: 'snapshot-1', file_version_id: 'version-1',
    project_id: 'project-1', commit_sha: 'a'.repeat(40), path: 'src/service.ts',
    range: { start_byte: 0, end_byte: 8, start_line: 1, end_line: 1 },
    symbols: null, quality: 'structural', gitlab_url: '', context: null,
  }
  const references = agentDrawerReferences.getGrepChunkToolReferences([{
    chunk_id: 'title-only-hit',
    knowledge_id: 'file-1',
    knowledge_base_id: 'kb-1',
    knowledge_title: 'Service source',
    title_match: true,
    match_snippet: '',
    source_evidence: sourceEvidence,
  }])

  assert.equal(references.length, 1)
  assert.equal(references[0].content, '')
  assert.deepEqual(references[0].source_evidence, [sourceEvidence])
  assert.deepEqual(buildReferenceList(references)[0].sourceEvidence, [sourceEvidence])
})
