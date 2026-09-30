import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { JSDOM } from 'jsdom'
import ts from 'typescript'
import { sourceFactLabel, sourceQualityLabel } from '../../../../utils/sourceQuality'

const dom = new JSDOM('<html><body></body></html>', { url: 'http://localhost/' })
for (const key of ['window', 'document', 'navigator', 'Element', 'HTMLElement', 'SVGElement', 'Node']) {
  Object.defineProperty(globalThis, key, { configurable: true, value: (dom.window as any)[key] })
}
const require = createRequire(import.meta.url)
const { createApp, h, nextTick } = require('vue') as typeof import('vue')

test('Agent knowledge tool UI displays bounded source facts, diagnostics and pinned target evidence', async () => {
  const path = fileURLToPath(new URL('./KnowledgeChunksList.vue', import.meta.url))
  const { descriptor } = parse(readFileSync(path, 'utf8'), { filename: path })
  const compiled = ts.transpileModule(compileScript(descriptor, { id: path, inlineTemplate: true }).content,
    { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} as any }
  new Function('require', 'module', 'exports', compiled)((name: string) => {
    if (name === 'vue-i18n') return { useI18n: () => ({ t: () => 'chunks' }) }
    if (name === '@/utils/knowledgeChunksDisplay') return { getKnowledgeChunksSummaryHtml: () => '' }
    if (name === '@/utils/sourceQuality') return { sourceFactLabel, sourceQualityLabel }
    return require(name)
  }, module, module.exports)

  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(module.exports.default, { data: {
    display_type: 'knowledge_chunks_list',
    source_analysis: {
      knowledge_id: 'java-file', snapshot_id: 'snapshot-one', file_version_id: 'java-version',
      sha256: 'a'.repeat(64), path: 'src/PushScheduleMapper.java', quality: 'structural', parser_version: 'java-pack',
      facts: [{ kind: 'java_mapper_method', method_name: 'getPushSchedule', certainty: 'certain', range: { start_line: 3, end_line: 3 } }],
      facts_truncated: false, diagnostics: [{ code: 'statement_id_duplicate', message: 'duplicate mapper ID', range: { start_line: 8, end_line: 8 } }],
      diagnostics_truncated: false,
      relations: [{ id: 'relation-one', kind: 'mapper_statement', from_key: 'demo.Mapper#getPushSchedule', to_key: 'getPushSchedule',
        determinacy: 'certain', quality: 'structural', target_evidence: { knowledge_id: 'xml-file', file_version_id: 'xml-version',
          sha256: 'b'.repeat(64), path: 'src/mapper/PushScheduleMapper.xml', range: { start_line: 5, end_line: 7 }, snippet: '<select id="getPushSchedule">' } }],
      relations_truncated: false,
    },
  } }) })
  ;(app.config.globalProperties as any).$t = () => 'No matching chunks'
  app.mount(host)
  try {
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.textContent?.includes('src/PushScheduleMapper.java'))
    assert.ok(host.textContent?.includes('结构解析'))
    assert.ok(host.textContent?.includes('java_mapper_method'))
    assert.ok(host.textContent?.includes('statement_id_duplicate'))
    assert.ok(host.textContent?.includes('src/mapper/PushScheduleMapper.xml'))
    assert.ok(host.textContent?.includes('b'.repeat(64)))
    assert.ok(host.textContent?.includes('<select id="getPushSchedule">'))
    assert.equal(host.querySelector('select'), null, 'target snippet is rendered as text, not HTML')
  } finally { app.unmount(); host.remove() }
})
