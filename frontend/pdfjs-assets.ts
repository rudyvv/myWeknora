import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { readdirSync, readFileSync } from 'node:fs'
import type { Plugin } from 'vite'

/** Keep PDF fonts and decoders local in development and production. */
export function pdfjsAssets(): Plugin {
  const require = createRequire(import.meta.url)
  const root = dirname(require.resolve('pdfjs-dist/package.json'))
  const assets = new Map<string, string>()
  for (const directory of ['cmaps', 'standard_fonts', 'wasm']) {
    for (const entry of readdirSync(join(root, directory), { withFileTypes: true })) {
      if (entry.isFile()) assets.set(`/pdfjs/${directory}/${entry.name}`, join(root, directory, entry.name))
    }
  }
  return {
    name: 'pdfjs-assets',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const path = (req.url || '').split('?', 1)[0]
        const asset = assets.get(path)
        if (!asset) return next()
        res.setHeader('Content-Type', path.endsWith('.wasm') ? 'application/wasm' : 'application/octet-stream')
        res.end(readFileSync(asset))
      })
    },
    generateBundle() {
      for (const [url, path] of assets) this.emitFile({ type: 'asset', fileName: url.slice(1), source: readFileSync(path) })
    },
  }
}
