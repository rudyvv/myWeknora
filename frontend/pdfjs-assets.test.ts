import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { pdfjsAssets } from './pdfjs-assets.ts'

const root = dirname(createRequire(import.meta.url).resolve('pdfjs-dist/package.json'))

test('production PDF assets include the same Chinese CMap, font and decoder bytes as the installed renderer', () => {
  const assets = new Map<string, Uint8Array>()
  const generate = pdfjsAssets().generateBundle
  assert.equal(typeof generate, 'function')
  if (typeof generate !== 'function') throw new Error('Missing build hook')
  generate.call({ emitFile: (asset: { fileName: string; source: Uint8Array }) => assets.set(asset.fileName, asset.source) } as any, {} as any, {} as any, false)
  for (const path of ['cmaps/Adobe-GB1-UCS2.bcmap', 'standard_fonts/LiberationSans-Regular.ttf', 'wasm/openjpeg.wasm']) {
    assert.deepEqual(assets.get(`pdfjs/${path}`), readFileSync(join(root, path)))
  }
})

test('development serves packaged assets and passes unknown paths to the next middleware', () => {
  let middleware: any
  const configure = pdfjsAssets().configureServer
  if (typeof configure !== 'function') throw new Error('Missing development hook')
  configure({ middlewares: { use: (handler: unknown) => { middleware = handler } } } as any)
  let bytes: Uint8Array | undefined
  let mime = ''
  let next = false
  const response = { setHeader: (_name: string, value: string) => { mime = value }, end: (data: Uint8Array) => { bytes = data } }
  middleware({ url: '/pdfjs/wasm/openjpeg.wasm?v=1' }, response, () => { next = true })
  assert.deepEqual(bytes, readFileSync(join(root, 'wasm/openjpeg.wasm')))
  assert.equal(mime, 'application/wasm')
  assert.equal(next, false)
  bytes = undefined
  middleware({ url: '/pdfjs/../package.json' }, response, () => { next = true })
  assert.equal(next, true)
  assert.equal(bytes, undefined)
})
