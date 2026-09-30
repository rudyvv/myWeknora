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

function compileSFC(path: string, resolveModule: (name: string) => any = require) {
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
  const requests: string[] = []
  const sourceModule = compileSFC(path, (name: string) => {
    if (name === '@/api/wiki') return { readSourceWikiEvidence() { throw new Error('unexpected Wiki evidence read') } }
    if (name === '@/api/knowledge-base') return { async getSourceFile(id: string) {
      requests.push(id)
      const broken = id === 'file-broken'
      return { data: { knowledge_id: id, snapshot_id: 'snapshot-one', file_version_id: broken ? 'version-broken' : 'version-one',
        project_id: '123', commit_sha: 'a'.repeat(40), repository_url: 'https://gitlab.local/team/repo',
        path: broken ? 'src/syntax_error.py' : 'src/Service.java', encoding: 'utf-8', quality: broken ? 'syntax_error' : 'structural', parser_version: 'java-pack-locked',
        content: broken ? 'async def broken(:\r\n    return "degraded_python_marker 中文😀"\r\n' : 'class Service {\r\n String getPushSchedule() { return "<img src=x onerror=alert(1)>"; }\r\n}',
        symbols: broken ? [] : [{ kind: 'method', name: 'getPushSchedule', qualified_name: 'Service.getPushSchedule', range: { start_line: 2, end_line: 2 },
          region: { kind: 'script', language: 'ts', quality: 'structural', external_source: './api.js', external_status: 'unavailable' } }] } }
    } }
    if (name === '@/components/SourceRegionBadge.vue') return badgeModule
    return require(name)
  })
  const host = document.createElement('div')
  document.body.append(host)
  const props = reactive({ knowledgeId: 'file-one', fileVersionId: 'version-one' })
  const app = createApp({ render: () => h(sourceModule.default, props) })
  app.mount(host)
  try {
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.deepEqual(requests, ['file-one'])
    assert.ok(host.textContent?.includes('a'.repeat(40)))
    assert.ok(host.textContent?.includes('<img src=x onerror=alert(1)>'))
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
    props.fileVersionId = 'different-version'
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.querySelector('[role="alert"]'))
    assert.ok(!host.textContent?.includes('<img src=x onerror=alert(1)>'))
    props.knowledgeId = 'file-broken'
    props.fileVersionId = 'version-broken'
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.textContent?.includes('存在语法错误'))
    assert.ok(host.textContent?.includes('degraded_python_marker 中文😀'))
    assert.ok(host.querySelector('[data-line="1"]'))
    props.fileVersionId = 'different-version'
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.querySelector('[role="alert"]'))
    assert.ok(!host.textContent?.includes('degraded_python_marker 中文😀'))
  } finally { app.unmount(); host.remove() }
})
