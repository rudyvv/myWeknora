import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { JSDOM } from 'jsdom'
import ts from 'typescript'
import { sourceFactLabel, sourceQualityLabel } from '../utils/sourceQuality'

const dom = new JSDOM('<html><body></body></html>', { url: 'http://localhost/' })
for (const key of ['window', 'document', 'navigator', 'Element', 'HTMLElement', 'SVGElement', 'Node']) {
  Object.defineProperty(globalThis, key, { configurable: true, value: (dom.window as any)[key] })
}
const require = createRequire(import.meta.url)
const { createApp, h, nextTick, reactive } = require('vue') as typeof import('vue')
function testRequire(name: string) {
  if (name === '@/utils/sourceQuality') return { sourceFactLabel, sourceQualityLabel }
  return require(name)
}

function compileSFC(path: string, resolveModule: (name: string) => any = testRequire) {
  const { descriptor } = parse(readFileSync(path, 'utf8'), { filename: path })
  const compiled = ts.transpileModule(compileScript(descriptor, { id: path, inlineTemplate: true }).content,
    { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} as any }
  new Function('require', 'module', 'exports', compiled)(resolveModule, module, module.exports)
  return module.exports
}

test('published source is escaped, read-only, and links the selected symbol to the same commit', async () => {
  const path = fileURLToPath(new URL('./SourceCodeView.vue', import.meta.url))
  const badgePath = fileURLToPath(new URL('./SourceRegionBadge.vue', import.meta.url))
  const badgeModule = compileSFC(badgePath)
  const requests: Array<[string, string | undefined, string | undefined]> = []
  let rejectTarget = false
  const fileData = { knowledge_id: 'file-one', snapshot_id: 'snapshot-one', file_version_id: 'version-one',
    project_id: '123', commit_sha: 'a'.repeat(40), repository_url: 'https://gitlab.local/team/repo',
    path: 'src/Service.java', encoding: 'utf-8', quality: 'structural', parser_version: 'java-pack-locked',
    content: 'class Service {\r\n String getPushSchedule() { return "<img src=x onerror=alert(1)>"; }\r\n}',
    symbols: [{ kind: 'method', name: 'getPushSchedule', qualified_name: 'Service.getPushSchedule', range: { start_line: 2, end_line: 2 },
      region: { kind: 'script', language: 'ts', quality: 'structural', external_source: './api.js', external_status: 'unavailable' } }],
    facts: [{ kind: 'java_mapper_method', method_name: 'getPushSchedule', quality: 'structural', range: { start_byte: 10, end_byte: 20, start_line: 2, end_line: 2 } }],
    diagnostics: [{ code: 'statement_id_duplicate', message: 'Mapper statement id is duplicated', range: { start_byte: 10, end_byte: 20, start_line: 2, end_line: 2 } }],
    relations: [
      { id: 'relation-one', kind: 'mapper_statement', from_file_id: 'file-one', from_version_id: 'version-one', from_path: 'src/Service.java', from_key: 'getPushSchedule', from_range: { start_byte: 10, end_byte: 20, start_line: 2, end_line: 2 }, to_file_id: 'file-one', to_version_id: 'version-one', to_path: 'src/Service.java', to_key: 'statement', to_range: { start_byte: 21, end_byte: 35, start_line: 2, end_line: 2 }, determinacy: 'certain', quality: 'structural' },
      { id: 'relation-two', kind: 'mapper_statement', from_file_id: 'file-one', from_version_id: 'version-one', from_path: 'src/Service.java', from_key: 'getPushSchedule', from_range: { start_byte: 10, end_byte: 20, start_line: 2, end_line: 2 }, to_file_id: 'file-xml', to_version_id: 'version-xml', to_path: 'src/mapper/ServiceMapper.xml', to_key: 'statement', to_range: { start_byte: 42, end_byte: 90, start_line: 3, end_line: 3 }, determinacy: 'certain', quality: 'structural' },
      { id: 'relation-uncertain', kind: 'mapper_statement', from_file_id: 'file-one', from_version_id: 'version-one', from_path: 'src/Service.java', from_key: 'possibleMapper', from_range: { start_byte: 10, end_byte: 20, start_line: 2, end_line: 2 }, to_file_id: 'file-xml', to_version_id: 'version-xml', to_path: 'src/mapper/ServiceMapper.xml', to_key: 'ambiguous', to_range: { start_byte: 42, end_byte: 90, start_line: 3, end_line: 3 }, determinacy: 'uncertain', quality: 'partial', resolution_reason: 'ambiguous mapper method' },
    ],
    relations_truncated: true, relations_next_cursor: 'opaque-next' }
  const sourceModule = compileSFC(path, (name: string) => {
    if (name === '@/api/wiki') return { readSourceWikiEvidence() { throw new Error('unexpected Wiki evidence read') } }
    if (name === '@/api/knowledge-base') return { async getSourceFile(id: string, versionID?: string, cursor?: string) {
      requests.push([id, versionID, cursor])
      if (id === 'file-broken') return { data: { ...fileData, knowledge_id: id,
        file_version_id: 'version-broken', path: 'src/syntax_error.py', quality: 'syntax_error',
        content: 'async def broken(:\r\n    return "degraded_python_marker 中文😀"\r\n',
        symbols: [], facts: [], diagnostics: [], relations: [], relations_truncated: false, relations_next_cursor: '' } }
      if (id === 'file-xml') {
        if (rejectTarget) throw new Error('source file is no longer readable')
        return { data: { ...fileData, knowledge_id: id, file_version_id: versionID, path: 'src/mapper/ServiceMapper.xml', content: '<mapper>\n  <sql id="other">SELECT 0</sql>\n  <select id="getPushSchedule">SELECT 1</select>\n</mapper>', relations: [fileData.relations[1]], relations_truncated: false, relations_next_cursor: '' } }
      }
      return { data: cursor ? { ...fileData, relations: [{ ...fileData.relations[0], id: 'relation-two' }], relations_truncated: false, relations_next_cursor: '' } : { ...fileData, knowledge_id: id } }
    } }
    if (name === '@/components/SourceRegionBadge.vue') return badgeModule
    return testRequire(name)
  })
  const host = document.createElement('div')
  document.body.append(host)
  const props = reactive({ knowledgeId: 'file-one', fileVersionId: 'version-one' })
  const app = createApp({ render: () => h(sourceModule.default, props) })
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
    assert.ok(symbol.textContent?.includes('script · ts · 结构解析'))
    assert.ok(symbol.textContent?.includes('当前不可读取或未关联'))
    assert.ok(!symbol.textContent?.includes('unavailable'))
    symbol.click()
    await nextTick()
    assert.equal(host.querySelector('a')?.getAttribute('href'), `https://gitlab.local/team/repo/-/blob/${'a'.repeat(40)}/src/Service.java#L2-2`)
    assert.ok(host.querySelector('[data-line="2"]')?.classList.contains('selected'))
    const uncertainRelation = Array.from(host.querySelectorAll<HTMLLIElement>('.source-relations li')).find(li => li.textContent?.includes('ambiguous mapper method'))
    assert.ok(uncertainRelation)
    assert.equal(uncertainRelation.querySelector('button'), null, 'uncertain endpoints must not be presented as a verified navigation target')
    const moreRelations = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.includes('读取更多关系'))
    assert.ok(moreRelations)
    moreRelations.click()
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.deepEqual(requests[1], ['file-one', 'version-one', 'opaque-next'])
    assert.equal(host.querySelectorAll('.source-relations li').length, 3)
    const crossFile = Array.from(host.querySelectorAll<HTMLButtonElement>('.source-relations button')).find(b => b.textContent?.includes('src/mapper/ServiceMapper.xml'))
    assert.ok(crossFile, 'cross-file relation should offer a fixed-version target-range action')
    crossFile.click()
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.deepEqual(requests[2], ['file-xml', 'version-xml', undefined])
    assert.ok(host.textContent?.includes('src/mapper/ServiceMapper.xml'))
    assert.ok(host.querySelector('[data-line="3"]')?.classList.contains('selected'))
    const reverseCrossFile = Array.from(host.querySelectorAll<HTMLButtonElement>('.source-relations button')).find(b => b.textContent?.includes('src/Service.java'))
    assert.ok(reverseCrossFile, 'the XML endpoint should link back to the related fixed-version Java method')
    reverseCrossFile.click()
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.deepEqual(requests[3], ['file-one', 'version-one', undefined])
    assert.ok(host.textContent?.includes('src/Service.java'))
    assert.ok(host.querySelector('[data-line="2"]')?.classList.contains('selected'))
    const back = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.includes('返回来源文件'))
    assert.ok(back)
    back.click()
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.textContent?.includes('src/mapper/ServiceMapper.xml'))
    const backToJava = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.includes('返回来源文件'))
    assert.ok(backToJava)
    backToJava.click()
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.textContent?.includes('src/Service.java'))
    rejectTarget = true
    const deniedCrossFile = Array.from(host.querySelectorAll<HTMLButtonElement>('.source-relations button')).find(b => b.textContent?.includes('src/mapper/ServiceMapper.xml'))
    assert.ok(deniedCrossFile)
    deniedCrossFile.click()
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.querySelector('[role="alert"]')?.textContent?.includes('目标文件不可读取'))
    assert.ok(host.textContent?.includes('src/Service.java'), 'failed target reads must preserve the source and must not reveal target content')
    assert.ok(!host.textContent?.includes('SELECT 1'))
    props.fileVersionId = 'different-version'
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.querySelector('[role="alert"]'))
    assert.ok(!host.textContent?.includes('<img src=x onerror=alert(1)>'))
    props.knowledgeId = 'file-broken'
    props.fileVersionId = 'version-broken'
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.textContent?.includes('语法错误'))
    assert.ok(host.textContent?.includes('degraded_python_marker 中文😀'))
    assert.ok(host.querySelector('[data-line="1"]'))
    props.fileVersionId = 'different-version'
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.querySelector('[role="alert"]'))
    assert.ok(!host.textContent?.includes('degraded_python_marker 中文😀'))
  } finally { app.unmount(); host.remove() }
})

test('non-structural source quality is not mislabeled as a syntax error', async () => {
  const path = fileURLToPath(new URL('./SourceCodeView.vue', import.meta.url))
  const badgeModule = compileSFC(fileURLToPath(new URL('./SourceRegionBadge.vue', import.meta.url)))
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
    if (name === '@/components/SourceRegionBadge.vue') return badgeModule
    return testRequire(name)
  }, module, module.exports)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(module.exports.default, { knowledgeId: 'file-one' }) })
  app.mount(host)
  try {
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.textContent?.includes('文本回退'))
    assert.ok(host.textContent?.includes('mapper_namespace_missing'))
    assert.ok(!host.textContent?.includes('语法错误'))
  } finally { app.unmount(); host.remove() }
})
