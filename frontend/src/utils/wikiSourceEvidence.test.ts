import assert from 'node:assert/strict'
import test from 'node:test'
import { resolveWikiSourceEvidence } from './wikiSourceEvidence'

const evidence = { id: 'e001', knowledge_id: 'file', file_version_id: 'version', commit_sha: 'a'.repeat(40), path: 'src/file.ts', range: { start_line: 2, end_line: 5, start_byte: 4, end_byte: 25 } }
const page: any = { knowledge_base_id: 'kb', slug: 'concept/module', version: 3, source_provenance: { evidence: [evidence] } }
const href = '/api/v1/knowledgebase/kb/wiki/source/evidence?slug=concept%2Fmodule&evidence_id=e001&version=3'

test('Wiki preview source link opens the owner-bound version and exact range', () => {
  const target = resolveWikiSourceEvidence(href, 'kb', page, 'http://localhost:57826')
  assert.deepEqual(target?.owner, { kbId: 'kb', slug: 'concept/module', id: 'e001', version: 3, commitSHA: 'a'.repeat(40) })
  assert.deepEqual(target?.evidence.range, evidence.range)
  assert.equal(target?.evidence.file_version_id, 'version')
})

test('Wiki preview rejects source links from other origins, KBs, pages, versions or evidence IDs', () => {
  for (const candidate of [
    'https://other.example' + href,
    href.replace('/kb/', '/other/'),
    href.replace('concept%2Fmodule', 'concept%2Fother'),
    href.replace('version=3', 'version=2'),
    href.replace('e001', 'e999'),
    href.replace('&version=3', ''),
  ]) assert.equal(resolveWikiSourceEvidence(candidate, 'kb', page, 'http://localhost:57826'), null)
  assert.equal(resolveWikiSourceEvidence(href, 'other', page, 'http://localhost:57826'), null)
  assert.equal(resolveWikiSourceEvidence(href, 'kb', null, 'http://localhost:57826'), null)
  assert.equal(resolveWikiSourceEvidence(href, 'kb', { ...page, source_provenance: { evidence: [{ ...evidence, commit_sha: '' }] } }, 'http://localhost:57826'), null)
})
