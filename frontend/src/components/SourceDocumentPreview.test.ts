import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { JSDOM } from 'jsdom'
import ts from 'typescript'

const dom = new JSDOM('<html><body></body></html>', { url: 'http://localhost/' })
for (const key of ['window', 'document', 'navigator', 'Element', 'HTMLElement', 'SVGElement', 'Node']) {
  Object.defineProperty(globalThis, key, { configurable: true, value: (dom.window as any)[key] })
}
const require = createRequire(import.meta.url)
const { createApp, h, nextTick, reactive, watch } = require('vue') as typeof import('vue')
const { descriptor } = parse(readFileSync(new URL('./SourceDocumentPreview.vue', import.meta.url), 'utf8'))
const compiled = ts.transpileModule(compileScript(descriptor, { id: 'source-preview', inlineTemplate: true }).content,
  { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
async function settle() { await Promise.resolve(); await nextTick(); await Promise.resolve(); await nextTick() }

function fixture(read: (id: string, version?: string) => Promise<any>) {
  const previews: Array<{ blob: Blob; name: string }> = []
  const module = { exports: {} as any }
  new Function('require', 'module', 'exports', compiled)((name: string) => {
    if (name === 'vue-i18n') return { useI18n: () => ({ t: (key: string) => key }) }
    if (name === '@/api/knowledge-base') return { getSourceFile: read }
    if (name === '@/components/document-preview.vue') return {
      props: ['sourceBlob', 'fileName', 'fileType', 'active'],
      setup(props: any) {
        watch(() => props.sourceBlob, blob => { previews.push({ blob, name: props.fileName }) }, { immediate: true })
        return () => h('pre', { 'data-preview': '' }, 'Original content')
      },
    }
    return require(name)
  }, module, module.exports)
  const props = reactive({ knowledgeId: 'file-one', fileVersionId: 'version-one' })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(module.exports.default, props) })
  app.component('t-loading', { render: () => h('span') })
  app.component('t-button', { setup(_props: unknown, { slots }: any) { return () => h('button', slots.default?.()) } })
  app.mount(host)
  return { host, props, previews, close: () => { app.unmount(); host.remove() } }
}

test('document preview uses the decoded pinned source and omits repository and analysis controls', async () => {
  const requests: unknown[] = []
  const content = 'const 名称 = "<tag>";\n// 中文源码\n'
  const view = fixture(async (...args) => {
    requests.push(args)
    return { data: { knowledge_id: 'file-one', file_version_id: 'version-one', path: 'src/settings.js', content,
      encoding: 'gbk', repository_url: 'https://gitlab.local/repo', symbols: [{}], facts: [{}] } }
  })
  try {
    await settle()
    assert.deepEqual(requests, [['file-one', 'version-one']])
    assert.equal(view.previews[0].name, 'src/settings.js')
    assert.equal(await view.previews[0].blob.text(), content)
    assert.ok(view.host.querySelector('[data-preview]'))
    assert.equal(view.host.querySelector('input, a, nav, .source-analysis'), null)
  } finally { view.close() }
})

test('a stale response cannot replace the newly selected file', async () => {
  let resolveOld!: (value: any) => void
  const view = fixture(id => id === 'file-one' ? new Promise(resolve => { resolveOld = resolve })
    : Promise.resolve({ data: { knowledge_id: id, file_version_id: 'version-two', path: 'new.js', content: 'new source' } }))
  try {
    view.props.knowledgeId = 'file-two'
    view.props.fileVersionId = 'version-two'
    await settle()
    resolveOld({ data: { knowledge_id: 'file-one', file_version_id: 'version-one', path: 'old.js', content: 'old source' } })
    await settle()
    assert.deepEqual(view.previews.map(item => item.name), ['new.js'])
  } finally { view.close() }
})

test('a wrong source version is hidden and retry reads the same requested version', async () => {
  let calls = 0
  const view = fixture(async (id, version) => ({ data: { knowledge_id: id,
    file_version_id: ++calls === 1 ? 'wrong-version' : version, path: 'correct.js', content: 'correct source' } }))
  try {
    await settle()
    assert.ok(view.host.querySelector('[role="alert"]'))
    assert.equal(view.previews.length, 0)
    view.host.querySelector<HTMLButtonElement>('button')!.click()
    await settle()
    assert.equal(calls, 2)
    assert.equal(await view.previews[0].blob.text(), 'correct source')
  } finally { view.close() }
})
