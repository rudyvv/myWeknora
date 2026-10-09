import assert from 'node:assert/strict'
import test from 'node:test'
import { JSDOM } from 'jsdom'
import { renderSourceChunk } from './sourceChunkPreview.ts'

test('source chunks retain code line breaks and display HTML as literal text', () => {
  const content = '<template>\n  <img src="x" onerror="alert(1)">\n</template>\n<script>const value = 1</script>'
  const document = new JSDOM(renderSourceChunk(content, 'vue')).window.document
  assert.equal(document.querySelector('pre code')?.textContent, content)
  assert.equal(document.querySelector('script, img'), null)
})

test('unknown source formats and Markdown-looking code remain literal code', () => {
  const content = '# heading\n* source text *\n<custom-tag>'
  const document = new JSDOM(renderSourceChunk(content, 'unknown')).window.document
  assert.equal(document.querySelector('pre code')?.textContent, content)
  assert.equal(document.querySelector('h1, em, custom-tag'), null)
})
