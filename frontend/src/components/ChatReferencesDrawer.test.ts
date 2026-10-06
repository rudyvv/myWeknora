import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { JSDOM } from 'jsdom'
import ts from 'typescript'
import * as referenceSources from '../utils/referenceSources.ts'
import { getGrepChunkToolReferences } from '../utils/agentDrawerReferences.ts'

const dom = new JSDOM('<html><body></body></html>', { url: 'http://localhost/', pretendToBeVisual: true })
for (const key of ['window', 'document', 'navigator', 'Element', 'HTMLElement', 'SVGElement', 'Node']) {
  Object.defineProperty(globalThis, key, { configurable: true, value: (dom.window as any)[key] })
}
const require = createRequire(import.meta.url)
const { createApp, defineComponent, h, nextTick, ref } = require('vue') as typeof import('vue')

function compileDrawer(resolveModule: (name: string) => any) {
  const path = fileURLToPath(new URL('./ChatReferencesDrawer.vue', import.meta.url))
  const { descriptor } = parse(readFileSync(path, 'utf8'), { filename: path })
  const compiled = ts.transpileModule(compileScript(descriptor, { id: path, inlineTemplate: true }).content,
    { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} as any }
  new Function('require', 'module', 'exports', compiled)(resolveModule, module, module.exports)
  return module.exports
}

test('source evidence action passes its immutable version and range to the source-view boundary', async () => {
  const evidence = {
    data_source_id: 'source-1',
    snapshot_id: 'snapshot-1',
    file_version_id: 'version-1',
    project_id: 'project-1',
    commit_sha: 'a'.repeat(40),
    path: 'src/service.ts',
    range: { start_byte: 10, end_byte: 30, start_line: 4, end_line: 5 },
    symbols: ['Service.run'],
    quality: 'structural',
    gitlab_url: 'https://gitlab.example/group/repo/-/blob/main/src/service.ts',
    context: [],
  }
  const drawer = {
    visible: ref(true),
    references: ref([{
      id: 'chunk-1',
      knowledge_id: 'file-1',
      knowledge_title: 'Service',
      knowledge_base_id: 'kb-1',
      content: 'source excerpt',
      source_evidence: evidence,
    }, {
      id: 'chunk-2',
      knowledge_id: 'file-2',
      knowledge_title: 'Unversioned source',
      content: 'unverified excerpt',
      source_evidence: { ...evidence, file_version_id: '' },
    }]),
    highlight: ref(null),
    close() { this.visible.value = false },
  }
  const SourceCodeViewStub = defineComponent({
    props: ['knowledgeId', 'fileVersionId', 'evidenceRange', 'expectedSourceEvidence'],
    setup(props) {
      return () => h('output', {
        'data-knowledge-id': props.knowledgeId,
        'data-file-version-id': props.fileVersionId,
        'data-start-line': props.evidenceRange?.start_line,
        'data-end-line': props.evidenceRange?.end_line,
        'data-evidence-commit': props.expectedSourceEvidence?.commit_sha,
      })
    },
  })
  const component = compileDrawer((name) => {
    if (name === 'vue') return require('vue')
    if (name === 'vue-i18n') return { useI18n: () => ({ t: (key: string) => key }) }
    if (name === 'vue-router') return { useRouter: () => ({ resolve: () => ({ href: '#' }) }) }
    if (name === '@/composables/useChatReferencesDrawer') return { useChatReferencesDrawer: () => drawer }
    if (name === '@/utils/referenceSources') return referenceSources
    if (name === '@/components/SourceCodeView.vue') return SourceCodeViewStub
    return require(name)
  })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(component.default) })
  app.component('t-icon', defineComponent({ setup: () => () => h('span') }))
  app.mount(host)
  try {
    await nextTick()
    const actions = host.querySelectorAll<HTMLButtonElement>('.reference-item__source-action')
    assert.equal(actions.length, 1, 'missing immutable identity must not produce a source action')
    const action = actions[0]
    assert.ok(action)
    assert.match(action.textContent || '', /src\/service\.ts.*L4–5/)
    action.click()
    await nextTick()

    const sourceView = host.querySelector('output')
    assert.ok(sourceView)
    assert.equal(sourceView.getAttribute('data-knowledge-id'), 'file-1')
    assert.equal(sourceView.getAttribute('data-file-version-id'), 'version-1')
    assert.equal(sourceView.getAttribute('data-start-line'), '4')
    assert.equal(sourceView.getAttribute('data-end-line'), '5')
    assert.equal(sourceView.getAttribute('data-evidence-commit'), 'a'.repeat(40))
  } finally {
    app.unmount()
    host.remove()
  }
})

test('source evidence identity rejects a fetched file from another immutable location', () => {
  const identity = {
    data_source_id: 'source-1',
    snapshot_id: 'snapshot-1',
    file_version_id: 'version-1',
    project_id: 'project-1',
    commit_sha: 'b'.repeat(40),
    path: 'src/service.ts',
  }

  assert.equal(referenceSources.sourceEvidenceMatchesFile(identity, identity), true)
  assert.equal(referenceSources.sourceEvidenceMatchesFile(identity, { ...identity, path: 'src/other.ts' }), false)
  assert.equal(referenceSources.sourceEvidenceMatchesFile(identity, { ...identity, data_source_id: undefined }), false)
  assert.equal(referenceSources.isNavigableSourceEvidence('file-1', {
    ...identity,
    range: { start_byte: 0, end_byte: 1, start_line: 1, end_line: 1 },
    quality: 'structural',
    gitlab_url: '',
    symbols: null,
    context: null,
  }), true, 'nil optional evidence collections remain valid when the fixed source identity and range are complete')
})

test('malformed source evidence leaves the references drawer renderable without source actions', async () => {
  const hash = 'a'.repeat(40)
  const validEvidence = {
    data_source_id: 'source-1',
    snapshot_id: 'snapshot-1',
    file_version_id: 'version-1',
    project_id: 'project-1',
    commit_sha: hash,
    path: 'src/service.ts',
    range: { start_byte: 0, end_byte: 8, start_line: 1, end_line: 1 },
    symbols: null,
    quality: 'structural',
    gitlab_url: '',
    context: null,
  }
  const malformedValues = [123, [], {}, [hash]]
  const evidenceFields = [
    'data_source_id', 'snapshot_id', 'file_version_id', 'project_id', 'path', 'quality', 'commit_sha',
  ]
  const malformedEvidenceReferences = evidenceFields.flatMap((field, fieldIndex) =>
    malformedValues.map((value, valueIndex) => ({
      id: `bad-evidence-${fieldIndex}-${valueIndex}`,
      knowledge_id: `bad-file-${fieldIndex}-${valueIndex}`,
      knowledge_title: 'Malformed source evidence',
      source_evidence: { ...validEvidence, [field]: value },
    })),
  )
  const malformedKnowledgeIDs = malformedValues.map((knowledge_id, index) => ({
    id: `bad-knowledge-id-${index}`,
    knowledge_id,
    knowledge_title: 'Malformed knowledge identity',
    source_evidence: validEvidence,
  }))
  const drawer = {
    visible: ref(true),
    references: ref([...malformedEvidenceReferences, ...malformedKnowledgeIDs]),
    highlight: ref(null),
    close() { this.visible.value = false },
  }
  const component = compileDrawer((name) => {
    if (name === 'vue') return require('vue')
    if (name === 'vue-i18n') return { useI18n: () => ({ t: (key: string) => key }) }
    if (name === 'vue-router') return { useRouter: () => ({ resolve: () => ({ href: '#' }) }) }
    if (name === '@/composables/useChatReferencesDrawer') return { useChatReferencesDrawer: () => drawer }
    if (name === '@/utils/referenceSources') return referenceSources
    if (name === '@/components/SourceCodeView.vue') return defineComponent({ setup: () => () => h('output') })
    return require(name)
  })
  const errors: unknown[] = []
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(component.default) })
  app.config.errorHandler = (error) => errors.push(error)
  app.component('t-icon', defineComponent({ setup: () => () => h('span') }))
  app.mount(host)
  try {
    await nextTick()
    assert.deepEqual(errors, [], 'malformed external identities must not abort drawer rendering')
    assert.equal(host.querySelectorAll('.reference-item').length, malformedEvidenceReferences.length + malformedKnowledgeIDs.length)
    assert.equal(host.querySelectorAll('.reference-item__source-action').length, 0)
  } finally {
    app.unmount()
    host.remove()
  }
})

test('title-only grep result keeps its source evidence action in the rendered drawer', async () => {
  const sourceEvidence = {
    data_source_id: 'source-1',
    snapshot_id: 'snapshot-1',
    file_version_id: 'version-1',
    project_id: 'project-1',
    commit_sha: 'a'.repeat(40),
    path: 'src/title-hit.ts',
    range: { start_byte: 0, end_byte: 12, start_line: 2, end_line: 2 },
    symbols: null,
    quality: 'structural',
    gitlab_url: '',
    context: null,
  }
  const references = getGrepChunkToolReferences([{
    chunk_id: 'title-only-hit',
    knowledge_id: 'file-title-hit',
    knowledge_base_id: 'kb-1',
    knowledge_title: 'Title matched source',
    title_match: true,
    match_snippet: '',
    source_evidence: sourceEvidence,
  }])
  const drawer = {
    visible: ref(true),
    references: ref(references),
    highlight: ref(null),
    close() { this.visible.value = false },
  }
  const SourceCodeViewStub = defineComponent({
    props: ['knowledgeId', 'fileVersionId', 'evidenceRange'],
    setup(props) {
      return () => h('output', {
        'data-knowledge-id': props.knowledgeId,
        'data-file-version-id': props.fileVersionId,
        'data-start-line': props.evidenceRange?.start_line,
        'data-end-line': props.evidenceRange?.end_line,
      })
    },
  })
  const component = compileDrawer((name) => {
    if (name === 'vue') return require('vue')
    if (name === 'vue-i18n') return { useI18n: () => ({ t: (key: string) => key }) }
    if (name === 'vue-router') return { useRouter: () => ({ resolve: () => ({ href: '#' }) }) }
    if (name === '@/composables/useChatReferencesDrawer') return { useChatReferencesDrawer: () => drawer }
    if (name === '@/utils/referenceSources') return referenceSources
    if (name === '@/components/SourceCodeView.vue') return SourceCodeViewStub
    return require(name)
  })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(component.default) })
  app.component('t-icon', defineComponent({ setup: () => () => h('span') }))
  app.mount(host)
  try {
    await nextTick()
    const item = host.querySelector('.reference-item')
    assert.ok(item?.textContent?.includes('Title matched source'))
    const action = host.querySelector<HTMLButtonElement>('.reference-item__source-action')
    assert.ok(action)
    assert.match(action.textContent || '', /src\/title-hit\.ts.*L2–2/)
    action.click()
    await nextTick()
    const sourceView = host.querySelector('output')
    assert.equal(sourceView?.getAttribute('data-knowledge-id'), 'file-title-hit')
    assert.equal(sourceView?.getAttribute('data-file-version-id'), 'version-1')
    assert.equal(sourceView?.getAttribute('data-start-line'), '2')
    assert.equal(sourceView?.getAttribute('data-end-line'), '2')
  } finally {
    app.unmount()
    host.remove()
  }
})
