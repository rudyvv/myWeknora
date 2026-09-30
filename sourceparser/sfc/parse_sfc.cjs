'use strict'

const { createHash } = require('node:crypto')
const compiler = require('@vue/compiler-sfc')

const COMPILER_VERSION = require('@vue/compiler-sfc/package.json').version
const MAX_SOURCE_BYTES = 16 << 20
const MAX_BLOCKS = 512
const MAX_DIAGNOSTICS = 128

function validPath(path) {
  return typeof path === 'string' && path.length > 0 && Buffer.byteLength(path, 'utf8') <= 4096 &&
    !path.startsWith('/') && !/[\\:\0\r\n]/.test(path) &&
    path.split('/').every(segment => segment !== '..') && path.toLowerCase().endsWith('.vue')
}

function collectBlocks(descriptor, source) {
  const candidates = []
  if (descriptor.template) candidates.push(descriptor.template)
  if (descriptor.script) candidates.push(descriptor.script)
  if (descriptor.scriptSetup) candidates.push(descriptor.scriptSetup)
  candidates.push(...descriptor.styles, ...descriptor.customBlocks)
  if (candidates.length > MAX_BLOCKS) throw new Error('block limit')

  return candidates.map(block => {
    const start = block.start
    const end = block.end
    if (!Number.isSafeInteger(start) || !Number.isSafeInteger(end) || start < 0 || end < start || end > source.length) {
      throw new Error('invalid compiler range')
    }
    if (block.content !== source.slice(start, end)) throw new Error('compiler changed block content')
    const type = typeof block.type === 'string' ? block.type : ''
    if (!type || type.length > 64) throw new Error('invalid block type')
    const rawLanguage = block.lang ?? (block.attrs && block.attrs.lang)
    if (typeof rawLanguage === 'string' && rawLanguage.length > 64) {
      const error = new Error('SFC attribute limit')
      error.code = 'sfc_attribute_limit'
      throw error
    }
    if (type === 'script' && typeof block.src === 'string' && block.src.length > 4096) {
      const error = new Error('SFC attribute limit')
      error.code = 'sfc_attribute_limit'
      throw error
    }
    const lang = typeof rawLanguage === 'string' ? rawLanguage : ''
    const src = type === 'script' && typeof block.src === 'string' ? block.src : ''
    const contentHash = createHash('sha256').update(block.content, 'utf8').digest('hex')
    return { type, lang, src, setup: Boolean(block.setup), content_sha256: contentHash,
      start_utf16: start, end_utf16: end }
  }).sort((left, right) => left.start_utf16 - right.start_utf16 || left.end_utf16 - right.end_utf16)
}

function openingBlockTags(source, blocks) {
  const openings = []
  const stack = []
  const blockTypes = new Set(blocks.map(block => block.type.toLowerCase()))
  const voidTags = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'param', 'source', 'track', 'wbr'])
  let index = 0
  let blockIndex = 0
  while (index < source.length) {
    while (blockIndex < blocks.length && blocks[blockIndex].end_utf16 <= index) blockIndex++
    const body = blocks[blockIndex]
    if (body && index >= body.start_utf16 && index < body.end_utf16) {
      index = body.end_utf16
      continue
    }
    if (source.startsWith('<!--', index)) {
      const commentEnd = source.indexOf('-->', index + 4)
      index = commentEnd < 0 ? source.length : commentEnd + 3
      continue
    }
    if (source[index] === '<') {
      const match = /^<\s*(\/?)\s*([a-z][\w:-]*)(?=[\s/>])/i.exec(source.slice(index))
      if (match) {
        let cursor = index + match[0].length
        let quote = ''
        while (cursor < source.length) {
          const character = source[cursor]
          if (quote) {
            if (character === quote) quote = ''
          } else if (character === '"' || character === "'") {
            quote = character
          } else if (character === '>') {
            break
          }
          cursor++
        }
        const tag = match[2].toLowerCase()
        if (match[1] === '/') {
          let open = stack.length - 1
          while (open >= 0 && stack[open].tag !== tag) open--
          if (open >= 0) {
            const matched = stack[open]
            if (matched.block) {
              matched.block.close_start = index
              matched.block.close_end = Math.min(cursor + 1, source.length)
            }
            stack.length = open
          }
        } else {
          let block = null
          if (stack.length === 0 && (['template', 'script', 'style'].includes(tag) || blockTypes.has(tag))) {
            block = { type: tag, start: index, open_end: Math.min(cursor + 1, source.length),
              close_start: null, close_end: null,
              self_closing: /\/\s*>$/.test(source.slice(index, cursor + 1)) }
            openings.push(block)
            if (openings.length > MAX_BLOCKS) throw new Error('block limit')
          }
          const selfClosing = /\/\s*>$/.test(source.slice(index, cursor + 1))
          if (!selfClosing && !voidTags.has(tag)) stack.push({ tag, block })
        }
        index = Math.min(cursor + 1, source.length)
        continue
      }
    }
    index++
  }
  return openings
}

function attachBlockTagRanges(source, blocks, tags) {
  const used = new Set()
  for (const block of blocks) {
    const match = tags.find((tag, index) => !used.has(index) && tag.type === block.type.toLowerCase() &&
      tag.open_end === block.start_utf16)
    if (!match) continue
    const index = tags.indexOf(match)
    used.add(index)
    block.tag_start_utf16 = match.start
    block.tag_end_utf16 = match.open_end
    if (Number.isSafeInteger(match.close_start) && Number.isSafeInteger(match.close_end)) {
      block.close_start_utf16 = match.close_start
      block.close_end_utf16 = match.close_end
    }
  }
}

function collectTopLevelBlocks(source, tags) {
  return tags.map(tag => {
    const start = tag.open_end
    const end = Number.isSafeInteger(tag.close_start) ? tag.close_start :
      tag.self_closing ? tag.open_end : source.length
    const content = source.slice(start, end)
    const block = { type: tag.type, start_utf16: start, end_utf16: end,
      tag_start_utf16: tag.start, tag_end_utf16: tag.open_end,
      content_sha256: createHash('sha256').update(content, 'utf8').digest('hex') }
    if (Number.isSafeInteger(tag.close_start) && Number.isSafeInteger(tag.close_end)) {
      block.close_start_utf16 = tag.close_start
      block.close_end_utf16 = tag.close_end
    }
    return block
  })
}

function collectDiagnostics(errors, source, blocks, tags) {
  if (!Array.isArray(errors) || errors.length > MAX_DIAGNOSTICS) throw new Error('diagnostic limit')
  const diagnostics = errors.map(error => {
    const start = error && Number.isSafeInteger(error.start) ? error.start : null
    const end = error && Number.isSafeInteger(error.end) ? error.end : null
    return { code: 'vue_sfc_parse_warning', start_utf16: start, end_utf16: end }
  })
  const descriptorCounts = { template: 0, script: 0, style: 0 }
  for (const block of blocks) {
    if (Object.prototype.hasOwnProperty.call(descriptorCounts, block.type)) descriptorCounts[block.type]++
  }
  const openingCounts = { template: [], script: [], style: [] }
  for (const opening of tags) {
    if (openingCounts[opening.type]) openingCounts[opening.type].push(opening.start)
  }
  for (const type of ['template', 'script']) {
    for (const start of openingCounts[type].slice(descriptorCounts[type])) {
      diagnostics.push({ code: 'vue_sfc_duplicate_block', start_utf16: start, end_utf16: null })
    }
  }
  if (diagnostics.length > MAX_DIAGNOSTICS) throw new Error('diagnostic limit')
  return diagnostics
}

function parseSFC(request) {
  if (!request || !validPath(request.path) || typeof request.source !== 'string') throw new Error('invalid request')
  const bytes = Buffer.from(request.source, 'utf8')
  if (bytes.byteLength > MAX_SOURCE_BYTES) throw new Error('source limit')
  const digest = createHash('sha256').update(bytes).digest('hex')
  if (typeof request.sha256 !== 'string' || request.sha256 !== digest) throw new Error('source hash mismatch')

  const parsed = compiler.parse({
    source: request.source,
    filename: request.path,
    sourceMap: false,
    compilerParseOptions: { pad: false, deindent: false, outputSourceRange: true },
  })
  const blocks = collectBlocks(parsed, request.source)
  const tags = openingBlockTags(request.source, blocks)
  attachBlockTagRanges(request.source, blocks, tags)
  return {
    node_version: process.version,
    compiler_version: COMPILER_VERSION,
    path: request.path,
    sha256: digest,
    source_bytes: bytes.byteLength,
    blocks,
    top_level_blocks: collectTopLevelBlocks(request.source, tags),
    diagnostics: collectDiagnostics(parsed.errors, request.source, blocks, tags),
  }
}

function main() {
  if (process.argv.length === 3 && process.argv[2] === '--health') {
    process.stdout.write(JSON.stringify({ node_version: process.version, compiler_version: COMPILER_VERSION, rules_version: 3 }))
    return
  }
  let body
  try {
    body = JSON.parse(require('node:fs').readFileSync(0, 'utf8'))
    process.stdout.write(JSON.stringify(parseSFC(body)))
  } catch (error) {
    const code = error && error.code === 'sfc_attribute_limit' ? 'sfc_attribute_limit' : 'sfc_parse_failed'
    process.stdout.write(JSON.stringify({ error: code }))
    process.exitCode = 1
  }
}

module.exports = { parseSFC }

if (require.main === module) main()
