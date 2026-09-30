'use strict'

const assert = require('node:assert/strict')
const { createHash } = require('node:crypto')
const test = require('node:test')

const { parseSFC } = require('../parse_sfc.cjs')

function request(source, path = 'src/Widget.vue') {
  return {
    path,
    source,
    sha256: createHash('sha256').update(source, 'utf8').digest('hex'),
  }
}

test('Vue 2 block offsets select the exact CRLF and Unicode body slices', () => {
  const source = '<!-- 😀 before -->\r\n' +
    '<template lang="html">\r\n  <div>中文😀</div>\r\n</template>\r\n' +
    '<script lang="ts">\r\nexport default { name: "Widget" }\r\n</script>\r\n' +
    '<style lang="scss" scoped>\r\n.预约 { color: red; }\r\n</style>\r\n' +
    '<i18n lang="json">\r\n{"title":"🧭"}\r\n</i18n>\r\n'
  const parsed = parseSFC(request(source))

  assert.equal(parsed.compiler_version, '2.7.16')
  assert.deepEqual(parsed.blocks.map(block => block.type), ['template', 'script', 'style', 'i18n'])
  const expected = [
    ['template', 'html', '\r\n  <div>中文😀</div>\r\n'],
    ['script', 'ts', '\r\nexport default { name: "Widget" }\r\n'],
    ['style', 'scss', '\r\n.预约 { color: red; }\r\n'],
    ['i18n', 'json', '\r\n{"title":"🧭"}\r\n'],
  ]
  for (const [type, language, body] of expected) {
    const block = parsed.blocks.find(candidate => candidate.type === type)
    assert.equal(block.lang, language)
    assert.equal(source.slice(block.start_utf16, block.end_utf16), body)
  }
})

test('an external script is returned as a literal reference and never loaded', () => {
  const parsed = parseSFC(request('<script src="./api.js"></script>\r\n'))
  assert.equal(parsed.blocks.length, 1)
  assert.equal(parsed.blocks[0].src, './api.js')
  assert.equal(parsed.blocks[0].start_utf16, parsed.blocks[0].end_utf16)
})

test('self-closing top-level blocks retain their zero-width bodies and wrappers', () => {
  for (const [source, type, externalSource] of [
    ['<template/>', 'template', ''],
    ['<template />', 'template', ''],
    ['<script src="./api.js"/>', 'script', './api.js'],
    ['<script src="./api.js" />', 'script', './api.js'],
  ]) {
    const parsed = parseSFC(request(source))
    assert.equal(parsed.blocks.length, 1)
    const block = parsed.blocks[0]
    assert.equal(block.type, type)
    assert.equal(block.start_utf16, block.end_utf16)
    assert.equal(block.src || '', externalSource)
    const topLevel = parsed.top_level_blocks[0]
    assert.equal(topLevel.type, type)
    assert.equal(topLevel.start_utf16, topLevel.end_utf16)
    assert.equal(source.slice(topLevel.tag_start_utf16, topLevel.tag_end_utf16), source)
  }
})

test('block language and external source limits reject without losing authored values', () => {
  const maxLanguage = 'x'.repeat(64)
  assert.equal(parseSFC(request(`<script lang="${maxLanguage}"></script>`)).blocks[0].lang, maxLanguage)
  assert.throws(() => parseSFC(request(`<script lang="${maxLanguage}x"></script>`)),
    error => error.code === 'sfc_attribute_limit')

  const maxSource = './' + 'a'.repeat(4094)
  assert.equal(parseSFC(request(`<script src="${maxSource}"></script>`)).blocks[0].src, maxSource)
  assert.throws(() => parseSFC(request(`<script src="${maxSource}a"></script>`)),
    error => error.code === 'sfc_attribute_limit')
})

test('logical path and content hash are validated before invoking the compiler', () => {
  const input = request('<template><div /></template>')
  assert.throws(() => parseSFC({ ...input, sha256: '0'.repeat(64) }), /hash/i)
  assert.throws(() => parseSFC({ ...input, path: '../secrets.vue' }), /request/i)
  assert.throws(() => parseSFC({ ...input, path: 'https://host/app.vue' }), /request/i)
})

test('malformed descriptor warnings are reduced to bounded source-free diagnostic codes', () => {
  const source = '<template><div>first</template>\n'
  const parsed = parseSFC(request(source))
  assert.ok(parsed.diagnostics.length > 0)
  assert.ok(parsed.diagnostics.every(diagnostic => diagnostic.code === 'vue_sfc_parse_warning'))
  assert.ok(parsed.diagnostics.every(diagnostic => !('message' in diagnostic)))
})

test('duplicate singleton descriptors receive a bounded source-free diagnostic', () => {
  const source = '<template><div>first</div></template>\n' +
    '<template><div>duplicate</div></template>\n'
  const parsed = parseSFC(request(source))
  assert.ok(parsed.diagnostics.some(diagnostic => diagnostic.code === 'vue_sfc_duplicate_block'))
})

test('top-level inventory retains exact duplicate block bodies omitted by the descriptor', () => {
  const source = '<script>const firstBlockMarker = 1;</script>\n' +
    '<script>const secondBlockMarker = 2;</script>\n'
  const parsed = parseSFC(request(source))

  assert.equal(parsed.blocks.length, 1)
  assert.equal(parsed.top_level_blocks.length, 2)
  for (const [index, marker] of ['firstBlockMarker', 'secondBlockMarker'].entries()) {
    const block = parsed.top_level_blocks[index]
    assert.equal(block.type, 'script')
    assert.equal(source.slice(block.start_utf16, block.end_utf16), `const ${marker} = ${index + 1};`)
    assert.equal(source.slice(block.tag_start_utf16, block.tag_end_utf16).startsWith('<script>'), true)
    assert.equal(source.slice(block.close_start_utf16, block.close_end_utf16), '</script>')
    assert.match(block.content_sha256, /^[0-9a-f]{64}$/)
  }
  assert.ok(parsed.diagnostics.some(diagnostic => diagnostic.code === 'vue_sfc_duplicate_block'))
})

test('top-level inventory retains bounded language attributes for recovered blocks', () => {
  const source = '<template><div>first</div></template>\n' +
    '<template lang="pug">section second</template>\n'
  const parsed = parseSFC(request(source))

  assert.equal(parsed.top_level_blocks.length, 2)
  assert.equal(parsed.top_level_blocks[0].lang, '')
  assert.equal(parsed.top_level_blocks[1].lang, 'pug')
})

test('a literal closing script tag is bounded exactly as Vue defines the SFC block', () => {
  const source = '<script>const text = "</script>";\r\n' +
    'export default { name: "A" };\r\n</script>\r\n'
  const parsed = parseSFC(request(source))
  const script = parsed.blocks[0]
  assert.equal(script.type, 'script')
  assert.equal(source.slice(script.start_utf16, script.end_utf16), 'const text = "')
  assert.ok(source.slice(script.end_utf16).includes('export default'))
  assert.equal(parsed.source_bytes, Buffer.byteLength(source, 'utf8'))
})
