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
  const requests: string[] = []
  const module = { exports: {} as any }
  new Function('require', 'module', 'exports', compiled)((name: string) => {
    if (name === '@/api/knowledge-base') return { async getSourceFile(id: string) {
      requests.push(id)
      return { data: { knowledge_id: id, snapshot_id: 'snapshot-one', file_version_id: 'version-one',
        project_id: '123', commit_sha: 'a'.repeat(40), repository_url: 'https://gitlab.local/team/repo',
        path: 'src/Service.java', encoding: 'utf-8', quality: 'structural', parser_version: 'java-pack-locked',
        content: 'class Service {\r\n String getPushSchedule() { return "<img src=x onerror=alert(1)>"; }\r\n}',
        symbols: [{ kind: 'method', name: 'getPushSchedule', qualified_name: 'Service.getPushSchedule', range: { start_line: 2, end_line: 2 } }] } }
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
    assert.deepEqual(requests, ['file-one'])
    assert.ok(host.textContent?.includes('a'.repeat(40)))
    assert.ok(host.textContent?.includes('<img src=x onerror=alert(1)>'))
    assert.equal(host.querySelector('img'), null)
    assert.equal(host.querySelector('textarea,[contenteditable="true"]'), null)
    const symbol = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.includes('Service.getPushSchedule'))
    assert.ok(symbol)
    symbol.click()
    await nextTick()
    assert.equal(host.querySelector('a')?.getAttribute('href'), `https://gitlab.local/team/repo/-/blob/${'a'.repeat(40)}/src/Service.java#L2-2`)
    assert.ok(host.querySelector('[data-line="2"]')?.classList.contains('selected'))
    props.fileVersionId = 'different-version'
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.querySelector('[role="alert"]'))
    assert.ok(!host.textContent?.includes('<img src=x onerror=alert(1)>'))
  } finally { app.unmount(); host.remove() }
})
