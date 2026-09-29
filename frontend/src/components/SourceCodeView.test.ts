import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { JSDOM } from 'jsdom'
import ts from 'typescript'

const dom = new JSDOM('<html><body></body></html>', { url: 'http://localhost/' })
for (const key of ['window', 'document', 'navigator', 'Element', 'HTMLElement', 'SVGElement', 'Node']) {
  Object.defineProperty(globalThis, key, { configurable: true, value: (dom.window as any)[key] })
}
const require = createRequire(import.meta.url)
const { createApp, h, nextTick, reactive } = require('vue') as typeof import('vue')

test('published source is escaped, read-only, and links the selected symbol to the same commit', async () => {
  const path = fileURLToPath(new URL('./SourceCodeView.vue', import.meta.url))
  const { descriptor } = parse(readFileSync(path, 'utf8'), { filename: path })
  const compiled = ts.transpileModule(compileScript(descriptor, { id: path, inlineTemplate: true }).content,
    { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const requests: Array<[string, string | undefined, string | undefined]> = []
  const fileData = { knowledge_id: 'file-one', snapshot_id: 'snapshot-one', file_version_id: 'version-one',
    project_id: '123', commit_sha: 'a'.repeat(40), repository_url: 'https://gitlab.local/team/repo',
    path: 'src/Service.java', encoding: 'utf-8', quality: 'structural', parser_version: 'java-pack-locked',
    content: 'class Service {\r\n String getPushSchedule() { return "<img src=x onerror=alert(1)>"; }\r\n}',
    symbols: [{ kind: 'method', name: 'getPushSchedule', qualified_name: 'Service.getPushSchedule', range: { start_line: 2, end_line: 2 } }],
    facts: [{ kind: 'java_mapper_method', method_name: 'getPushSchedule', quality: 'structural', range: { start_byte: 10, end_byte: 20, start_line: 2, end_line: 2 } }],
    diagnostics: [{ code: 'statement_id_duplicate', message: 'Mapper statement id is duplicated', range: { start_byte: 10, end_byte: 20, start_line: 2, end_line: 2 } }],
    relations: [{ id: 'relation-one', kind: 'mapper_statement', from_file_id: 'file-one', from_version_id: 'version-one', from_path: 'src/Service.java', from_key: 'getPushSchedule', from_range: { start_byte: 10, end_byte: 20, start_line: 2, end_line: 2 }, to_file_id: 'file-one', to_version_id: 'version-one', to_path: 'src/Service.java', to_key: 'statement', to_range: { start_byte: 21, end_byte: 35, start_line: 2, end_line: 2 }, determinacy: 'certain', quality: 'structural' }],
    relations_truncated: true, relations_next_cursor: 'opaque-next' }
  const module = { exports: {} as any }
  new Function('require', 'module', 'exports', compiled)((name: string) => {
    if (name === '@/api/wiki') return { readSourceWikiEvidence() { throw new Error('unexpected Wiki evidence read') } }
    if (name === '@/api/knowledge-base') return { async getSourceFile(id: string, versionID?: string, cursor?: string) {
      requests.push([id, versionID, cursor])
      return { data: cursor ? { ...fileData, relations: [{ ...fileData.relations[0], id: 'relation-two' }], relations_truncated: false, relations_next_cursor: '' } : { ...fileData, knowledge_id: id } }
    } }
    return require(name)
  }, module, module.exports)
  const host = document.createElement('div')
  document.body.append(host)
  const props = reactive({ knowledgeId: 'file-one', fileVersionId: 'version-one' })
  const app = createApp({ render: () => h(module.exports.default, props) })
  app.mount(host)
  try {
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.deepEqual(requests, [['file-one', 'version-one', undefined]])
    assert.ok(host.textContent?.includes('a'.repeat(40)))
    assert.ok(host.textContent?.includes('<img src=x onerror=alert(1)>'))
    assert.ok(host.textContent?.includes('java_mapper_method'))
    assert.ok(host.textContent?.includes('statement_id_duplicate'))
    assert.ok(host.textContent?.includes('Mapper statement id is duplicated'))
    assert.equal(host.querySelector('img'), null)
    assert.equal(host.querySelector('textarea,[contenteditable="true"]'), null)
    const symbol = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.includes('Service.getPushSchedule'))
    assert.ok(symbol)
    symbol.click()
    await nextTick()
    assert.equal(host.querySelector('a')?.getAttribute('href'), `https://gitlab.local/team/repo/-/blob/${'a'.repeat(40)}/src/Service.java#L2-2`)
    assert.ok(host.querySelector('[data-line="2"]')?.classList.contains('selected'))
    const moreRelations = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.includes('读取更多关系'))
    assert.ok(moreRelations)
    moreRelations.click()
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.deepEqual(requests[1], ['file-one', 'version-one', 'opaque-next'])
    assert.equal(host.querySelectorAll('.source-relations li').length, 2)
    props.fileVersionId = 'different-version'
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.querySelector('[role="alert"]'))
    assert.ok(!host.textContent?.includes('<img src=x onerror=alert(1)>'))
  } finally { app.unmount(); host.remove() }
})

test('non-structural source quality is not mislabeled as a syntax error', async () => {
  const path = fileURLToPath(new URL('./SourceCodeView.vue', import.meta.url))
  const { descriptor } = parse(readFileSync(path, 'utf8'), { filename: path })
  const compiled = ts.transpileModule(compileScript(descriptor, { id: path, inlineTemplate: true }).content,
    { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} as any }
  new Function('require', 'module', 'exports', compiled)((name: string) => {
    if (name === '@/api/wiki') return { readSourceWikiEvidence() { throw new Error('unexpected Wiki evidence read') } }
    if (name === '@/api/knowledge-base') return { async getSourceFile(id: string) { return { data: {
      knowledge_id: id, file_version_id: 'version-one', commit_sha: 'a'.repeat(40), repository_url: '', path: 'mapper.xml',
      project_id: '123', encoding: 'utf-8', quality: 'text_fallback', parser_version: 'mybatis-xml', content: '<mapper/>', symbols: [],
      facts: [], diagnostics: [{ code: 'mapper_namespace_missing', message: 'Mapper has no namespace' }], relations: [], relations_truncated: false,
    } } } }
    return require(name)
  }, module, module.exports)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(module.exports.default, { knowledgeId: 'file-one' }) })
  app.mount(host)
  try {
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.textContent?.includes('文本回退'))
    assert.ok(host.textContent?.includes('mapper_namespace_missing'))
    assert.ok(!host.textContent?.includes('存在语法错误'))
  } finally { app.unmount(); host.remove() }
})
