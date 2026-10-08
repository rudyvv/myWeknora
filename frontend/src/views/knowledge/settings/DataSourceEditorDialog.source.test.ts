import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { JSDOM } from 'jsdom'
import ts from 'typescript'

// Set up a DOM before loading Vue/TDesign; their DOM renderer captures document.
const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/', pretendToBeVisual: true })
for (const key of ['window', 'document', 'navigator', 'Element', 'HTMLElement', 'SVGElement', 'Node', 'HTMLInputElement', 'MutationObserver', 'Event', 'MouseEvent', 'KeyboardEvent', 'getComputedStyle', 'localStorage', 'requestAnimationFrame', 'cancelAnimationFrame']) {
  Object.defineProperty(globalThis, key, { configurable: true, value: (dom.window as any)[key] })
}
const require = createRequire(import.meta.url)
const { createApp, h, nextTick, reactive } = require('vue') as typeof import('vue')
const tdesign = require('tdesign-vue-next') as typeof import('tdesign-vue-next')
const editorPath = fileURLToPath(new URL('./DataSourceEditorDialog.vue', import.meta.url))
const sourceRoot = resolve(dirname(editorPath), '../../..')

async function settle() {
  for (let i = 0; i < 4; i++) {
    await nextTick()
    await new Promise<void>(resolve => setImmediate(resolve))
  }
}

async function fixture({ create = false, twoProjects = false, delayedPreview = false, ready = false, previewFails = false } = {}) {
  const calls: Array<{ method: string; args: any[] }> = []
  let releasePreview: (() => void) | undefined
  let storedCredentials = { base_url: 'https://gitlab.example.com', access_token: 'token-A' }
  const record = (method: string, ...args: any[]) => calls.push({ method, args: JSON.parse(JSON.stringify(args)) })
  const api = {
    async createDataSource(data: any) {
      record('createDataSource', data)
      storedCredentials = { ...data.config.credentials }
      return { data: { id: 'source-one' } }
    },
    async previewSource(id: string, settings: any) {
      record('previewSource', id, settings)
      if (delayedPreview) await new Promise<void>(resolve => { releasePreview = resolve })
      if (previewFails) throw new Error('Repository unavailable')
      return { commit_sha: '1111111111111111111111111111111111111111', rules_version: 'v1:fixture', can_sync: ready,
        files: [{ path: 'dist/Business.java', status: 'included', reason: 'selected', generated: false, size: 20 }],
        checks: [{ name: 'parser', ready, message: 'Source parser is unavailable' }], warnings: [] }
    },
    async validateCredentials(type: string, credentials: any) { record('validateCredentials', type, credentials) },
    async putDataSourceCredentials(id: string, credentials: any) {
      record('putDataSourceCredentials', id, credentials)
      storedCredentials = { ...credentials }
    },
    async updateDataSource(id: string, data: any) { record('updateDataSource', id, data) },
    async triggerSync(id: string) { record('triggerSync', id) },
    async deleteDataSource(id: string) { record('deleteDataSource', id) },
  }
  const components = new Map<string, any>()
  function loadComponent(path: string): any {
    if (components.has(path)) return components.get(path)
    const { descriptor } = parse(readFileSync(path, 'utf8'), { filename: path })
    const script = compileScript(descriptor, { id: path, inlineTemplate: true }).content
    const compiled = ts.transpileModule(script, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
    const module = { exports: {} as any }
    const load = (name: string): any => {
      if (name === '@/api/datasource') return api // controlled HTTP/API boundary
      if (name === 'vue-i18n') return { useI18n: () => ({ t: (key: string) => key }) }
      if (name.endsWith('.vue')) return { __esModule: true, default: loadComponent(name.startsWith('@/') ? resolve(sourceRoot, name.slice(2)) : resolve(dirname(path), name)) }
      if (name === './datasourceIcons') return { getDatasourceIconUrl: () => undefined, datasourceIconMap: {} }
      return require(name)
    }
    new Function('require', 'module', 'exports', compiled)(load, module, module.exports)
    components.set(path, module.exports.default)
    return module.exports.default
  }
  const host = document.createElement('div')
  document.body.append(host)
  const props = reactive({ visible: false, kbId: 'kb-one', dataSource: create ? null : {
    id: 'source-one', name: 'GitLab', type: 'gitlab', credentials: { credentials: { configured: true } },
    config: { resource_ids: [], settings: { projects: twoProjects ? [{ project_id: '123', paths: [] }, { project_id: '456', ref: 'main', paths: [] }] : [{ project_id: '123', paths: [] }] } },
    sync_schedule: '0 0 */6 * * *', sync_mode: 'incremental', conflict_strategy: 'overwrite', sync_deletions: true,
  } })
  const app = createApp({ render: () => h(loadComponent(editorPath), { ...props, 'onUpdate:visible': (v: boolean) => { props.visible = v } }) })
  app.use(tdesign.default)
  app.mount(host)
  props.visible = true
  await settle()
  async function click(text: string) {
    const control = Array.from(document.querySelectorAll<HTMLElement>('button,label')).find(el => el.textContent?.trim() === text || el.querySelector('.ds-type-name')?.textContent === text)
    assert.ok(control, `Visible control: ${text}`)
    control.click()
    await settle()
  }
  async function fill(placeholder: string, value: string) {
    const input = Array.from(document.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>('input,textarea')).find(el => el.placeholder === placeholder)
    assert.ok(input, `Visible input: ${placeholder}`)
    input.value = value
    input.dispatchEvent(new dom.window.Event('input', { bubbles: true }))
    input.dispatchEvent(new dom.window.Event('change', { bubbles: true }))
    await settle()
  }
  return { click, fill, calls, async finishPreview() { assert.ok(releasePreview); releasePreview(); await settle() }, storedCredentials: () => storedCredentials, async close() {
    app.unmount()
    await settle()
    document.body.innerHTML = ''
  } }
}

test('Next checks one project, branch and paths before automatically checking source readiness', async () => {
  const f = await fixture({ twoProjects: true, ready: true })
  try {
    await f.click('datasource.next')
    await f.click('datasource.gitlab.sourceMode')
    await f.click('datasource.next')
    assert.ok(document.body.textContent?.includes('datasource.gitlab.sourceSelectionRequired'))
    assert.ok(document.querySelector('.source-preview'), 'resource selection remains visible')
    const projects = document.querySelectorAll('.gitlab-project-row')
    assert.equal(projects.length, 2)
    projects[1].querySelector<HTMLButtonElement>('button')!.click()
    await settle()
    await f.fill('datasource.gitlab.sourceBranchPlaceholder', 'main')
    await f.click('datasource.next')
    assert.ok(document.body.textContent?.includes('datasource.gitlab.sourcePathsRequired'))
    assert.deepEqual(f.calls, [], 'empty paths do not fetch the repository')
    await f.fill('datasource.gitlab.sourcePathsPlaceholder', 'src')
    await f.click('datasource.next')
    assert.equal(document.querySelector('.source-preview'), null, 'editor advances to sync settings')
    assert.ok(document.body.textContent?.includes('datasource.step.strategy'))
    assert.deepEqual(f.calls.map(call => call.method), ['previewSource'])
    assert.ok(document.body.textContent?.includes('datasource.gitlab.checkPassed'))
    assert.equal(document.querySelector('details')?.open, false, 'technical details are collapsed')
  } finally { await f.close() }
})

test('a delayed preview cannot display the previous instance after credentials change', async () => {
  const f = await fixture({ delayedPreview: true, ready: true })
  try {
    await f.click('datasource.next')
    await f.click('datasource.gitlab.sourceMode')
    await f.fill('datasource.gitlab.sourceBranchPlaceholder', 'main')
    await f.fill('datasource.gitlab.sourcePathsPlaceholder', 'src')
    await f.click('datasource.next')
    await f.click('datasource.back')
    await f.click('credential.update')
    await f.fill('https://gitlab.example.com', 'https://other-gitlab.example.com')
    await f.fill('credential.inputPlaceholder', 'token-B')
    await f.click('datasource.testConnection')
    await f.click('datasource.next')
    await f.finishPreview()
    assert.equal(document.querySelector('.source-preview__files'), null, 'stale response must not restore the previous instance manifest')
    assert.ok(!document.body.textContent?.includes('1111111111111111111111111111111111111111'))
    assert.ok(document.querySelector('.source-preview'), 'stale success cannot advance the editor')
  } finally { await f.close() }
})

test('Next blocks an unready source with a summary and collapsed details without saving it', async () => {
  const f = await fixture()
  try {
    await f.click('datasource.next')
    await f.click('datasource.gitlab.sourceMode')
    await f.fill('datasource.gitlab.sourceBranchPlaceholder', 'main')
    await f.fill('datasource.gitlab.sourcePathsPlaceholder', 'src')
    await f.fill('datasource.gitlab.excludePathsHint', 'vendor\nsrc/generated')
    assert.ok(!document.body.textContent?.includes('datasource.gitlab.preview'), 'no separate preview button')
    await f.click('datasource.next')
    assert.ok(document.querySelector('.source-preview'), 'failed check stays on range selection')
    assert.ok(document.body.textContent?.includes('datasource.gitlab.checkParser'))
    assert.equal(document.querySelector('details')?.open, false)
    assert.ok(document.body.textContent?.includes('1111111111111111111111111111111111111111'))
    assert.ok(document.body.textContent?.includes('dist/Business.java'))
    assert.ok(document.body.textContent?.includes('Source parser is unavailable'))
    assert.deepEqual(f.calls, [{ method: 'previewSource', args: ['source-one', { content_mode: 'source', projects: [{ project_id: '123', ref: 'main', paths: ['src'] }], exclude_paths: ['vendor', 'src/generated'] }] }])
    assert.equal(f.storedCredentials().access_token, 'token-A')
    await f.fill('datasource.gitlab.sourceBranchPlaceholder', 'release')
    assert.equal(document.querySelector('.source-preview__files'), null, 'changing a draft invalidates the displayed preview')
  } finally { await f.close() }
})

test('new source keeps the final credentials after automatic checks and returning to the connection step', async () => {
  const f = await fixture({ create: true, ready: true })
  try {
    await f.click('datasource.connector.gitlab')
    await f.fill('datasource.namePlaceholder', 'Fixture source')
    await f.fill('https://gitlab.example.com', 'https://gitlab.example.com')
    await f.fill('credential.inputPlaceholder', 'token-A')
    await f.click('datasource.next')
    await f.click('datasource.gitlab.sourceMode')
    await f.fill('datasource.gitlab.projectIdPlaceholder', '123')
    await f.fill('datasource.gitlab.sourceBranchPlaceholder', 'main')
    await f.fill('datasource.gitlab.sourcePathsPlaceholder', 'src')
    await f.click('datasource.next')
    assert.ok(document.querySelector('.source-preview__files'), 'first preview succeeds and creates a draft')
    await f.click('datasource.back')
    await f.click('datasource.back')
    await f.fill('credential.inputPlaceholder', 'token-B')
    await f.fill('https://gitlab.example.com', 'https://other-gitlab.example.com')
    await f.click('datasource.testConnection')
    await f.click('datasource.next')
    assert.equal(document.querySelector('.source-preview__files'), null, 'credential changes invalidate the previous preview')
    await f.click('datasource.next')
    assert.deepEqual(f.storedCredentials(), { base_url: 'https://other-gitlab.example.com', access_token: 'token-B' }, 'another preview uses the updated draft credentials')
    await f.click('datasource.createAndSync')
    assert.deepEqual(f.storedCredentials(), { base_url: 'https://other-gitlab.example.com', access_token: 'token-B' })
    const saved = f.calls.find(call => call.method === 'updateDataSource')
    assert.ok(saved)
    assert.deepEqual(saved.args[1].config.credentials, {})
  } finally { await f.close() }
})

test('document mode advances without the source check', async () => {
  const f = await fixture()
  try {
    await f.click('datasource.next')
    await f.click('datasource.next')
    assert.equal(document.querySelector('.gitlab-project-list'), null)
    assert.deepEqual(f.calls, [])
  } finally { await f.close() }
})

test('a pending check cannot be submitted twice or advance after the range changes', async () => {
  const f = await fixture({ delayedPreview: true, ready: true })
  try {
    await f.click('datasource.next')
    await f.click('datasource.gitlab.sourceMode')
    await f.fill('datasource.gitlab.sourceBranchPlaceholder', 'main')
    await f.fill('datasource.gitlab.sourcePathsPlaceholder', 'src')
    await f.click('datasource.next')
    assert.ok(document.body.textContent?.includes('datasource.gitlab.checkingHint'))
    await f.click('datasource.gitlab.checking')
    assert.equal(f.calls.filter(call => call.method === 'previewSource').length, 1)
    await f.fill('datasource.gitlab.sourcePathsPlaceholder', 'other')
    await f.finishPreview()
    assert.equal(document.querySelector('.source-check-result'), null)
    assert.ok(document.querySelector('.gitlab-project-list'))
  } finally { await f.close() }
})

test('a failed repository check remains on range selection and can be retried', async () => {
  const f = await fixture({ previewFails: true })
  try {
    await f.click('datasource.next')
    await f.click('datasource.gitlab.sourceMode')
    await f.fill('datasource.gitlab.sourceBranchPlaceholder', 'main')
    await f.fill('datasource.gitlab.sourcePathsPlaceholder', 'src')
    await f.click('datasource.next')
    assert.ok(document.querySelector('.gitlab-project-list'))
    assert.ok(document.body.textContent?.includes('Repository unavailable'))
    await f.click('datasource.next')
    assert.equal(f.calls.filter(call => call.method === 'previewSource').length, 2)
    assert.equal(f.calls.filter(call => call.method === 'updateDataSource' || call.method === 'triggerSync').length, 0)
  } finally { await f.close() }
})
