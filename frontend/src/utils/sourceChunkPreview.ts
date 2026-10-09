import hljs from 'highlight.js'
import { getHighlightLang } from './filePreview'

/** Source chunks are code, including HTML/Markdown-looking strings. */
export function renderSourceChunk(content: string, fileType: string): string {
  const language = getHighlightLang(fileType)
  const text = typeof content === 'string' ? content : ''
  let highlighted: string
  try {
    highlighted = hljs.highlight(text, { language: hljs.getLanguage(language) ? language : 'plaintext' }).value
  } catch {
    highlighted = hljs.highlight(text, { language: 'plaintext' }).value
  }
  return `<pre class="code-block-pre"><code class="hljs">${highlighted}</code></pre>`
}
