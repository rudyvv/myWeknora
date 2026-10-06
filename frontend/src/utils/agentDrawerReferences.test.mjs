import assert from 'node:assert/strict'
import test from 'node:test'
import {
  getAgentDrawerReferences,
  getKnowledgeSearchToolReferences,
} from './agentDrawerReferences.ts'
import { buildReferenceList, isNavigableSourceEvidence } from './referenceSources.ts'

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
