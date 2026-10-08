import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

function graphAPI(response: unknown) {
  const module = { exports: {} as any }
  const code = ts.transpileModule(readFileSync(new URL('./index.ts', import.meta.url), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText
  new Function('require', 'module', 'exports', code)(() => ({ get: async () => response }), module, module.exports)
  return module.exports.getWikiGraph
}

test('legacy null edges preserve isolated nodes in overview and ego responses', async () => {
  const nodes = [{ slug: 'concept/module', title: 'Module', page_type: 'concept', link_count: 0 }]
  for (const mode of ['overview', 'ego']) {
    for (const wrapped of [false, true]) {
      const graph = { nodes, edges: null, meta: { mode, total: 1, returned: 1 } }
      const response = wrapped ? { success: true, data: graph } : graph
      const result = await graphAPI(response)('kb', { mode, center: 'concept/module' })
      const data = result.data || result
      assert.deepEqual(data.nodes, nodes)
      assert.deepEqual([...data.edges], [])
      assert.equal(data.meta.mode, mode)
      assert.equal(graph.edges, null, 'normalization should not mutate the response')
    }
  }
})

test('graph normalization preserves real links and metadata', async () => {
  const graph = { nodes: [], edges: [{ source: 'a', target: 'b' }], meta: { mode: 'ego', center: 'a', truncated: true } }
  const result = await graphAPI({ data: graph })('kb')
  assert.deepEqual(result.data, graph)
})
