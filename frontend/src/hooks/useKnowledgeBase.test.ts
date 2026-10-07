import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import test from 'node:test'
import ts from 'typescript'
import { ref } from 'vue'

const require = createRequire(import.meta.url)
const compiled = ts.transpileModule(readFileSync(new URL('./useKnowledgeBase.ts', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText

function fixture(list: () => Promise<unknown>) {
  const store = { cardList: ref([]), total: ref(0) }
  const mocks: Record<string, unknown> = {
    pinia: { storeToRefs: () => store },
    '../utils/index': { formatStringDate: () => '' },
    'tdesign-vue-next': {},
    '@/api/knowledge-base/index': { listKnowledgeFiles: list },
    '@/stores/knowledge': { knowledgeStore: () => store },
    '@/stores/ui': {},
    'vue-router': { useRoute: () => ({ params: { kbId: 'kb' } }) },
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
  }
  const module = { exports: {} as any }
  new Function('require', 'module', 'exports', compiled)((name: string) => mocks[name] || require(name), module, module.exports)
  return module.exports.default('kb')
}

test('list failure is visible and a successful retry clears it and shows documents', async () => {
  let fail = true
  const hook = fixture(async () => {
    if (fail) throw new Error('timeout')
    return { data: [{ id: 'source', file_name: 'README.md', updated_at: '2026-10-07' }], total: 1 }
  })
  await hook.getKnowled()
  assert.equal(hook.listLoadError.value, true)
  fail = false
  await hook.getKnowled()
  assert.equal(hook.listLoadError.value, false)
  assert.equal(hook.cardList.value[0].id, 'source')
})

test('an old failed folder request cannot replace a newer successful list', async () => {
  let rejectOld!: (error: Error) => void
  let calls = 0
  const hook = fixture(() => ++calls === 1
    ? new Promise((_, reject) => { rejectOld = reject })
    : Promise.resolve({ data: [{ id: 'new', file_name: 'new.ts' }], total: 1 }))
  const old = hook.getKnowled()
  await hook.getKnowled()
  rejectOld(new Error('old timeout'))
  await old
  assert.equal(hook.listLoadError.value, false)
  assert.equal(hook.cardList.value[0].id, 'new')
})
